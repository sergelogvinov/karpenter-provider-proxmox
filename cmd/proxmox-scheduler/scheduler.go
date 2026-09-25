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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"

	"k8s.io/utils/cpuset"
)

// VMClass classifies a running VM for the purpose of CPU placement, per
// docs/scheduler-shared.md §3.
type VMClass string

const (
	// ClassPinned is a VM whose affinity has one CPU per vCPU
	// (cores == len(affinity)): its threads are pinned 1:1.
	ClassPinned VMClass = "pinned"
	// ClassConstrained is a VM with an affinity narrower than a 1:1
	// pinning (cores != len(affinity)): its threads are masked, but not
	// pinned individually.
	ClassConstrained VMClass = "constrained"
	// ClassShared is a VM with no affinity at all: it is placed by the
	// shared-pool planner (cpumanager.PlanShared).
	ClassShared VMClass = "shared"
	// ClassIgnored is a VM the daemon must never place or count — the
	// Karpenter capacity/topology discovery guest.
	ClassIgnored VMClass = "ignored"
)

// karpenterDiscoveryTag is the tag createProxmoxTopologyDiscoveryVM
// stamps on the node-capacity guest (see proxmox.go); a VM carrying it
// is never a real workload and must be excluded from every pool
// computation, since its affinity typically spans the entire host.
const karpenterDiscoveryTag = "karpenter"

// VMInfo holds information for a VM
type VMInfo struct {
	VMID  int
	PID   int
	Name  string
	Cores int
	Class VMClass

	// AffinitySet is the VM's configured affinity (pinned/constrained
	// classes only) — contributes to the exclusive pool.
	AffinitySet cpuset.CPUSet
	// AssignedSet is the cpuset currently applied to the VM's threads by
	// this daemon: the 1:1 pin for a pinned VM, or the shared-pool slice
	// for a shared VM.
	AssignedSet cpuset.CPUSet
}

// VMTracker holds the tracking information for all VMs
type VMTracker struct {
	// vms maps VM ID to VMInfo for all tracked VMs
	vms map[int]*VMInfo

	// sharedCPUs CPUs that are shared among VMs without affinity assignments
	sharedCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments (the exclusive pool:
	// pinned + constrained VMs only, never the ignored discovery VM)
	usedCPUs cpuset.CPUSet

	mu sync.RWMutex
}

type SchedulerHandler struct {
	client   *local.Client
	topology *topology.Topology
	tracker  *VMTracker

	// reservedCPUs are host-only CPUs, excluded from every pool.
	reservedCPUs cpuset.CPUSet
	// sharedPolicy selects the shared-pool placement strategy; "none"
	// disables it and restores pre-existing behavior.
	sharedPolicy string
	sharedOpts   cpumanager.SharedOptions
	// debounce coalesces a burst of start/stop/sync events into a single
	// rebalance (docs/scheduler-shared.md §6).
	debounce time.Duration

	rebalanceMu    sync.Mutex
	rebalanceTimer *time.Timer

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
		h.sharedOpts.MinWidth = minWidth
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
			vms: make(map[int]*VMInfo),
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

	r.tracker.mu.Lock()
	previous := r.tracker.vms
	r.tracker.usedCPUs = cpuset.New()
	r.tracker.vms = make(map[int]*VMInfo)
	r.tracker.mu.Unlock()

	for vmID, pid := range runningVMs {
		vmConfig, err := loadVMConfig(ctx, r.client, vmID)
		if err != nil {
			r.logger.Error(err, "Failed to load VM config for running VM", "vmID", vmID)

			continue
		}

		// Carry over whatever cpuset the daemon had already applied to
		// this VM before the rebuild, so a resync does not make the
		// following rebalance treat a well-placed shared VM as freshly
		// unplaced and needlessly reshuffle it.
		previousAssigned := cpuset.New()
		if prev, ok := previous[vmID]; ok && prev.PID == pid {
			previousAssigned = prev.AssignedSet
		}

		if err := r.updateVMInfo(vmID, pid, vmConfig, previousAssigned); err != nil {
			r.logger.Error(err, "Failed to update VM info", "vmID", vmID, "pid", pid)

			continue
		}
	}

	r.logVMStatus()

	// The VM set may have changed without a corresponding start/stop
	// event being observed (e.g. events missed while the daemon was
	// down), so a resync always re-evaluates the shared-pool plan too.
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

	if r.topology == nil || len(r.tracker.vms) == 0 {
		return
	}

	allCPUs := r.topology.CPUDetails.CPUs()
	if allCPUs.Size() == 0 {
		return
	}

	r.logger.V(1).Info("Current VM status",
		"totalCPUs", allCPUs.Size(),
		"usedCPUs", r.tracker.usedCPUs.String(),
		"sharedCPUs", r.tracker.sharedCPUs.String(),
	)

	for vmID, vmInfo := range r.tracker.vms {
		r.logger.V(2).Info("VM info",
			"vmID", vmID,
			"name", vmInfo.Name,
			"class", vmInfo.Class,
			"cores", vmInfo.Cores,
			"affinity", vmInfo.AffinitySet.String(),
			"assigned", vmInfo.AssignedSet.String(),
			"pid", vmInfo.PID)
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

// classifyVM classifies a VM per docs/scheduler-shared.md §3. It is a
// pure function of the values loadVMConfig/updateVMInfo already have in
// hand, so it needs no /proc or Proxmox access and is safe to unit test
// on its own.
func classifyVM(cores int, affinity cpuset.CPUSet, tags []string) VMClass {
	if slices.Contains(tags, karpenterDiscoveryTag) {
		return ClassIgnored
	}

	if affinity.IsEmpty() {
		return ClassShared
	}

	if cores == affinity.Size() {
		return ClassPinned
	}

	return ClassConstrained
}

// sharedPool returns the CPUs available for shared (unaffined) VMs:
// every host CPU minus the reserved CPUs minus the exclusive pool —
// the union of every pinned/constrained VM's affinity, i.e. tracker.usedCPUs
func (r *SchedulerHandler) sharedPool() cpuset.CPUSet {
	if r.topology == nil {
		return cpuset.New()
	}

	r.tracker.mu.RLock()
	exclusive := r.tracker.usedCPUs
	r.tracker.mu.RUnlock()

	return r.topology.CPUDetails.CPUs().Difference(r.reservedCPUs).Difference(exclusive)
}

// numaHint returns the NUMA nodes cpus touches, expressed as the union
// of those nodes' full CPU sets — the candidate pool PlanShared narrows
// to before falling back to the whole shared pool.
// An empty cpus yields an empty (i.e. unconstrained) hint.
func numaHint(topo *topology.CPUTopology, cpus cpuset.CPUSet) cpuset.CPUSet {
	if cpus.IsEmpty() {
		return cpuset.New()
	}

	nodes := make(map[int]struct{})

	for _, cpu := range cpus.List() {
		if id, err := topo.CPUNUMANodeID(cpu); err == nil {
			nodes[id] = struct{}{}
		}
	}

	hint := cpuset.New()
	for id := range nodes {
		hint = hint.Union(topo.CPUDetails.CPUsInNUMANodes(id))
	}

	return hint
}

// scheduleRebalance coalesces a burst of start/stop/sync events into a
// single shared-pool rebalance, fired after r.debounce of quiescence.
// A zero debounce rebalances inline, which is what tests want.
func (r *SchedulerHandler) scheduleRebalance(ctx context.Context) {
	if r.sharedPolicy == SharedPolicyNone || r.topology == nil {
		return
	}

	if r.debounce <= 0 {
		r.rebalanceShared(ctx)

		return
	}

	r.rebalanceMu.Lock()
	defer r.rebalanceMu.Unlock()

	if r.rebalanceTimer != nil {
		r.rebalanceTimer.Stop()
	}

	r.rebalanceTimer = time.AfterFunc(r.debounce, func() {
		r.rebalanceShared(ctx)
	})
}

// rebalanceShared recomputes and applies the shared-pool plan for every
// tracked ClassShared VM. It never touches pinned or constrained VMs.
// Best-effort throughout: a failure placing or applying one VM is logged and does not abort the others.
func (r *SchedulerHandler) rebalanceShared(ctx context.Context) {
	if ctx.Err() != nil || r.sharedPolicy == SharedPolicyNone || r.topology == nil {
		return
	}

	pool := r.sharedPool()

	r.tracker.mu.RLock()

	reqs := make([]cpumanager.SharedRequest, 0, len(r.tracker.vms))

	for vmID, info := range r.tracker.vms {
		if info.Class != ClassShared || info.Cores <= 0 {
			continue
		}

		reqs = append(reqs, cpumanager.SharedRequest{
			VMID:     vmID,
			CPUs:     info.Cores,
			NUMAHint: numaHint(&r.topology.CPUTopology, info.AssignedSet),
			Current:  info.AssignedSet,
		})
	}

	r.tracker.mu.RUnlock()

	if len(reqs) == 0 {
		r.applySharedGovernors(pool, cpuset.New())

		return
	}

	plan, err := cpumanager.PlanShared(r.logger, &r.topology.CPUTopology, pool, reqs, r.sharedOpts)
	if err != nil {
		r.logger.Error(err, "Failed to compute shared CPU plan")

		return
	}

	usedShared := cpuset.New()

	for vmID, cpus := range plan {
		usedShared = usedShared.Union(cpus)

		if ctx.Err() != nil {
			return
		}

		r.tracker.mu.RLock()
		info, ok := r.tracker.vms[vmID]
		r.tracker.mu.RUnlock()

		if !ok || info.AssignedSet.Equals(cpus) {
			continue
		}

		if !utilsys.ProcessExists(info.PID) {
			r.logger.V(1).Info("Shared VM process no longer exists, skipping", "vmID", vmID, "pid", info.PID)

			continue
		}

		threads, err := utilsys.GetProcessThreads(info.PID, "CPU")
		if err != nil {
			r.logger.Error(err, "Failed to get threads for shared VM rebalance", "vmID", vmID, "pid", info.PID)

			continue
		}

		if err := utilsys.SetThreadsAffinity(ctx, vmID, info.PID, threads, cpus); err != nil {
			r.logger.Error(err, "Failed to apply shared CPU plan to VM threads", "vmID", vmID)

			continue
		}

		if err := utilsys.SetProcessAffinity(ctx, vmID, info.PID, cpus); err != nil {
			r.logger.Error(err, "Failed to mask shared VM process", "vmID", vmID)
		}

		r.logger.Info("Rebalanced shared VM", "vmID", vmID, "cpus", cpus.String())

		r.tracker.mu.Lock()
		if cur, ok := r.tracker.vms[vmID]; ok {
			cur.AssignedSet = cpus
		}
		r.tracker.mu.Unlock()
	}

	r.applySharedGovernors(pool, usedShared)
}

// applySharedGovernors sweeps the CPU governor across the shared pool
// only: busy on the CPUs some shared VM currently occupies, free on the
// rest. Pinned/constrained VMs' governor state is set directly at
// start/stop time and is left untouched here.
func (r *SchedulerHandler) applySharedGovernors(pool, usedShared cpuset.CPUSet) {
	if usedShared.Size() > 0 && *cpuGovernorBusy != "" {
		if err := utilsys.SetCPUGovernor(0, usedShared.List(), *cpuGovernorBusy); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for shared pool", "governor", *cpuGovernorBusy, "cores", usedShared.String())
		}
	}

	if idle := pool.Difference(usedShared); idle.Size() > 0 && *cpuGovernorFree != "" {
		if err := utilsys.SetCPUGovernor(0, idle.List(), *cpuGovernorFree); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for idle shared pool", "governor", *cpuGovernorFree, "cores", idle.String())
		}
	}
}
