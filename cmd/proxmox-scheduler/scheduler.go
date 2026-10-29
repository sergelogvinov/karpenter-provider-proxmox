/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// trackedVM is what the scheduler knows of a running VM beyond its zone record:
// its process, and the cpuset last applied to its threads.
type trackedVM struct {
	pid int
	// applied is the cpuset taskset last set on the VM's threads; empty when none was.
	applied cpuset.CPUSet
}

// VMTracker holds the process and applied cpuset of every running VM.
type VMTracker struct {
	vms map[int]trackedVM

	mu sync.RWMutex
}

// appliedCPUs returns every VM's applied cpuset, for zonecapacity.Zone.Plan.
func (t *VMTracker) appliedCPUs() map[int]cpuset.CPUSet {
	t.mu.RLock()
	defer t.mu.RUnlock()

	applied := make(map[int]cpuset.CPUSet, len(t.vms))

	for id, vm := range t.vms {
		if !vm.applied.IsEmpty() {
			applied[id] = vm.applied
		}
	}

	return applied
}

// setApplied records that cpus were applied to VM id's process pid. It is a no-op
// if the VM has stopped or restarted with another PID since.
func (t *VMTracker) setApplied(id, pid int, cpus cpuset.CPUSet) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if vm, ok := t.vms[id]; ok && vm.pid == pid {
		vm.applied = cpus
		t.vms[id] = vm
	}
}

// pid returns VM id's process.
func (t *VMTracker) pid(id int) (int, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	vm, ok := t.vms[id]

	return vm.pid, ok
}

type SchedulerHandler struct {
	client   *local.Client
	topology *topology.Topology
	tracker  *VMTracker
	// zone records every running VM and plans where their CPUs go; nil without a topology.
	zone *zonecapacity.Zone

	// reservedCPUs are host-only CPUs, excluded from every pool.
	reservedCPUs cpuset.CPUSet
	// sharedPolicy selects the shared-pool placement strategy; "none"
	// leaves floating VMs as Proxmox started them.
	sharedPolicy string
	// sharedMinWidth is the fewest CPUs a floating VM is scaled down to
	// in an oversubscribed shared pool.
	sharedMinWidth int
	// debounce coalesces a burst of start/stop/sync events into a single
	// rebalance (docs/scheduler-shared.md §6).
	debounce time.Duration

	rebalanceMu    sync.Mutex
	rebalanceTimer *time.Timer

	// applyMu serializes planning and applying a plan, so a start and a
	// rebalance never apply plans computed from the same applied cpusets.
	applyMu sync.Mutex

	logger logr.Logger
}

const (
	// pidFileExtension is the file extension for PID files
	pidFileExtension = ".pid"

	// SharedPolicyNone disables shared-pool placement: every unaffined
	// VM is left exactly as Proxmox started it, matching the daemon's
	// behavior before docs/scheduler-shared.md.
	SharedPolicyNone = "none"
	// SharedPolicyPartition confines every unaffined VM to a slice of
	// the shared pool, per docs/scheduler-shared.md §4.
	SharedPolicyPartition = "partition"
)

// HandlerOption configures a SchedulerHandler at construction.
type HandlerOption func(*SchedulerHandler)

// WithReservedCPUs excludes cpus from every pool the handler manages.
func WithReservedCPUs(cpus cpuset.CPUSet) HandlerOption {
	return func(h *SchedulerHandler) {
		h.reservedCPUs = cpus
	}
}

// WithSharedPolicy selects the shared-pool placement strategy.
func WithSharedPolicy(policy string) HandlerOption {
	return func(h *SchedulerHandler) {
		switch policy {
		case "":
			// keep the default
		case SharedPolicyNone, SharedPolicyPartition:
			h.sharedPolicy = policy
		default:
			// An unrecognized value (e.g. a typo) must never silently
			// fall through to full shared-pool placement — that would
			// change production VM affinity on a misconfiguration. Warn
			// and keep whatever the default already is.
			h.logger.Error(fmt.Errorf("unknown shared CPU pool policy %q", policy), "Ignoring unrecognized --shared-policy, keeping the default",
				"policy", policy, "validValues", []string{SharedPolicyNone, SharedPolicyPartition}, "default", h.sharedPolicy)
		}
	}
}

// WithSharedMinWidth sets the minimum CPU width a shared VM is ever
// scaled down to in oversubscribed mode (0 keeps the planner's default
// of one physical core).
func WithSharedMinWidth(minWidth int) HandlerOption {
	return func(h *SchedulerHandler) {
		h.sharedMinWidth = minWidth
	}
}

// WithRebalanceDebounce sets how long the handler waits for a burst of
// start/stop/sync events to settle before recomputing the shared plan.
func WithRebalanceDebounce(d time.Duration) HandlerOption {
	return func(h *SchedulerHandler) {
		h.debounce = d
	}
}

func NewHandler(client *local.Client, topology *topology.Topology, logger logr.Logger, opts ...HandlerOption) *SchedulerHandler {
	h := &SchedulerHandler{
		client:   client,
		topology: topology,
		tracker: &VMTracker{
			vms: make(map[int]trackedVM),
		},
		reservedCPUs: cpuset.New(),
		sharedPolicy: SharedPolicyNone,
		debounce:     2 * time.Second,
		logger:       logger,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}

	if topology != nil {
		zone, err := zonecapacity.New(zonecapacity.Host{Topology: topology, ReservedCPUs: h.reservedCPUs})
		if err != nil {
			logger.Error(err, "Failed to set up zone capacity, CPU placement is disabled")
		} else {
			h.zone = zone
		}
	}

	return h
}

func (r *SchedulerHandler) Reconcile(ctx context.Context, sender reconciler.EventSender, event reconciler.Event) error {
	r.logger.V(4).Info("Processing event", "type", event.Type, "key", event.Key)

	if event.Type == reconciler.FileEvent {
		fsEvent, ok := event.Data.(fsnotify.Event)
		if ok {
			vmIDStr, ok := strings.CutSuffix(fsEvent.Name, pidFileExtension)
			if !ok { // Not a PID file, ignore
				return nil //nolint:nilerr
			}

			vmID, err := strconv.Atoi(filepath.Base(vmIDStr))
			if err != nil || vmID <= 0 { // Filename doesn't contain a valid VM ID, ignore
				return nil //nolint:nilerr
			}

			if fsEvent.Op == fsnotify.Remove {
				r.handleVMStop(ctx, vmID)

				return nil
			}

			pid, err := utilsys.GetPidFromFile(fsEvent.Name)
			if err != nil {
				return fmt.Errorf("failed to read PID from file %s: %w", fsEvent.Name, err)
			}

			err = r.handleVMStart(ctx, vmID, pid)
			if err != nil {
				r.logger.Error(err, "Failed to handle VM start", "vmID", vmID, "pid", pid)

				return err
			}

			return nil
		}
	}

	if event.Type == reconciler.SyncEvent {
		err := r.handleSyncEvent(ctx)
		if err != nil {
			r.logger.Error(err, "Failed to handle sync event")

			return err
		}

		return nil
	}

	return nil
}

// handleSyncEvent processes sync events to track VM information
func (r *SchedulerHandler) handleSyncEvent(ctx context.Context) error {
	r.logger.V(1).Info("Starting VM tracking")

	runningVMs, err := r.getRunningVMs()
	if err != nil {
		return fmt.Errorf("failed to get running VMs: %w", err)
	}

	stopped := []int{}

	r.tracker.mu.RLock()

	for vmID := range r.tracker.vms {
		if _, ok := runningVMs[vmID]; !ok {
			stopped = append(stopped, vmID)
		}
	}

	r.tracker.mu.RUnlock()

	for _, vmID := range stopped {
		r.forgetVM(vmID)
	}

	for vmID, pid := range runningVMs {
		vmConfig, err := loadVMConfig(ctx, r.client, vmID)
		if err != nil {
			// Keep whatever the VM held so far: it still runs.
			r.logger.Error(err, "Failed to load VM config for running VM", "vmID", vmID)

			continue
		}

		if err := r.trackVM(vmID, pid, vmConfig); err != nil {
			r.logger.Error(err, "Failed to track VM", "vmID", vmID, "pid", pid)
		}
	}

	r.logVMStatus()

	// The VM set may have changed without a corresponding start/stop
	// event being observed (e.g. events missed while the daemon was
	// down), so a resync always re-evaluates the plan too.
	r.scheduleRebalance(ctx)

	return nil
}

// getRunningVMs scans the PID directory and returns a map of VM ID to PID for running VMs
func (r *SchedulerHandler) getRunningVMs() (map[int]int, error) {
	runningVMs := make(map[int]int)

	entries, err := os.ReadDir(*watchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read PID directory %s: %w", *watchPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), pidFileExtension) {
			continue
		}

		vmID, pid, err := getVMIDs(filepath.Join(*watchPath, entry.Name()))
		if err != nil {
			r.logger.Error(err, "Failed to get VM IDs from file event", "file", entry.Name())

			continue
		}

		if !utilsys.ProcessExists(pid) {
			continue
		}

		runningVMs[vmID] = pid
	}

	return runningVMs, nil
}

// logVMStatus logs the current status of all tracked VMs
func (r *SchedulerHandler) logVMStatus() {
	r.tracker.mu.RLock()
	defer r.tracker.mu.RUnlock()

	if r.zone == nil || len(r.tracker.vms) == 0 {
		return
	}

	for vmID, vm := range r.tracker.vms {
		r.logger.V(2).Info("VM info", "vmID", vmID, "pid", vm.pid, "applied", vm.applied.String())
	}
}

func getVMIDs(pidFile string) (vmID, pid int, err error) {
	vmIDStr, ok := strings.CutSuffix(pidFile, pidFileExtension)
	if !ok {
		return 0, 0, fmt.Errorf("not a proxmox PID file: %s", pidFile)
	}

	vmID, err = strconv.Atoi(filepath.Base(vmIDStr))
	if err != nil {
		return 0, 0, fmt.Errorf("not a proxmox VM PID file: %s", pidFile)
	}

	pid, err = utilsys.GetPidFromFile(pidFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read PID from file %s: %w", pidFile, err)
	}

	return vmID, pid, nil
}

// scheduleRebalance coalesces a burst of start/stop/sync events into a
// single rebalance, fired after r.debounce of quiescence.
// A zero debounce rebalances inline, which is what tests want.
func (r *SchedulerHandler) scheduleRebalance(ctx context.Context) {
	if r.zone == nil {
		return
	}

	if r.debounce <= 0 {
		r.rebalance(ctx)

		return
	}

	r.rebalanceMu.Lock()
	defer r.rebalanceMu.Unlock()

	if r.rebalanceTimer != nil {
		r.rebalanceTimer.Stop()
	}

	r.rebalanceTimer = time.AfterFunc(r.debounce, func() {
		r.rebalance(ctx)
	})
}

// plan asks the zone where every tracked VM's CPUs go, given what was applied.
func (r *SchedulerHandler) plan() zonecapacity.Plan {
	return r.zone.Plan(r.tracker.appliedCPUs(), zonecapacity.PlanOptions{
		Partition: r.sharedPolicy == SharedPolicyPartition,
		MinWidth:  r.sharedMinWidth,
	})
}

// rebalance plans every tracked VM and applies what changed.
// Best-effort throughout: a failure applying one VM is logged and does not abort the others;
// its applied cpuset stays as it was, so the next rebalance retries it.
func (r *SchedulerHandler) rebalance(ctx context.Context) {
	if ctx.Err() != nil || r.zone == nil {
		return
	}

	r.applyMu.Lock()
	defer r.applyMu.Unlock()

	plan := r.plan()

	for _, note := range plan.Notes {
		r.logger.Info("CPU plan note", "note", note)
	}

	for _, vmID := range slices.Sorted(maps.Keys(plan.Changed)) {
		if ctx.Err() != nil {
			return
		}

		pid, ok := r.tracker.pid(vmID)
		if !ok {
			continue
		}

		cpus := plan.Changed[vmID]

		if err := r.applyCPUs(ctx, vmID, pid, cpus, plan.OneToOne[vmID]); err != nil {
			r.logger.Error(err, "Failed to apply CPU plan to VM", "vmID", vmID, "pid", pid, "cpus", cpus.String())

			continue
		}

		r.logger.Info("Rebalanced VM", "vmID", vmID, "cpus", cpus.String(), "oneToOne", plan.OneToOne[vmID])
	}

	// Governors are swept only under shared-pool placement: without it,
	// floating VMs run anywhere and the CPUs they use are unknown.
	if r.sharedPolicy == SharedPolicyPartition {
		r.applyGovernors(plan.Busy, plan.Idle)
	}
}

// applyCPUs sets cpus on VM vmID's threads, pinning one vCPU per CPU when oneToOne,
// and records them as applied only once taskset succeeded.
func (r *SchedulerHandler) applyCPUs(ctx context.Context, vmID, pid int, cpus cpuset.CPUSet, oneToOne bool) error {
	if !utilsys.ProcessExists(pid) {
		return fmt.Errorf("VM %d process %d no longer exists", vmID, pid)
	}

	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	if err != nil {
		return fmt.Errorf("failed to get threads of VM %d: %w", vmID, err)
	}

	if oneToOne {
		if err := utilsys.PinThreadsToCores(ctx, vmID, pid, threads, cpus.UnsortedList()); err != nil {
			return fmt.Errorf("failed to pin VM %d threads to cores: %w", vmID, err)
		}
	} else {
		if err := utilsys.SetThreadsAffinity(ctx, vmID, pid, threads, cpus); err != nil {
			return fmt.Errorf("failed to mask VM %d threads: %w", vmID, err)
		}

		if err := utilsys.SetProcessAffinity(ctx, vmID, pid, cpus); err != nil {
			r.logger.Error(err, "Failed to mask VM process", "vmID", vmID)
		}
	}

	r.tracker.setApplied(vmID, pid, cpus)

	return nil
}

// applyGovernors sets the busy CPU governor on the CPUs some VM holds,
// and the free one on the rest of the usable CPUs.
func (r *SchedulerHandler) applyGovernors(busy, idle cpuset.CPUSet) {
	if busy.Size() > 0 && *cpuGovernorBusy != "" {
		if err := utilsys.SetCPUGovernor(0, busy.List(), *cpuGovernorBusy); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for busy CPUs", "governor", *cpuGovernorBusy, "cores", busy.String())
		}
	}

	if idle.Size() > 0 && *cpuGovernorFree != "" {
		if err := utilsys.SetCPUGovernor(0, idle.List(), *cpuGovernorFree); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for idle CPUs", "governor", *cpuGovernorFree, "cores", idle.String())
		}
	}
}
