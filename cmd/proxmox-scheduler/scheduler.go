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
	"slices"
	"sync"
	"time"

	"github.com/go-logr/logr"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/piddir"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

var _ piddir.Handler = (*SchedulerHandler)(nil)

type SchedulerHandler struct {
	client  *local.Client
	tracker *VMTracker
	// zone records every running VM and plans where their CPUs go.
	zone *zonecapacity.Zone

	// reservedCPUs are host-only CPUs, never given to a VM under shared placement.
	reservedCPUs cpuset.CPUSet
	// partition divides the ungrouped CPUs among the VMs under shared
	// placement (--shared-policy=partition); otherwise they are left as
	// Proxmox started them, or masked to all the ungrouped CPUs while a
	// CPU share group exists. CPU share VMs are confined to their groups
	// either way.
	partition bool
	// sharedMinWidth is the fewest CPUs a VM under shared placement is
	// scaled down to when the unpinned CPUs are oversubscribed.
	sharedMinWidth int
	// debounce coalesces a burst of start/stop/sync events into a single
	// rebalance (docs/scheduler-shared.md §6).
	debounce time.Duration

	rebalanceMu    sync.Mutex
	rebalanceTimer *time.Timer

	// groupCPUs are the CPUs the CPU share groups held at the last rebalance,
	// so CPUs a group gives up get the free governor. Guarded by applyMu.
	groupCPUs cpuset.CPUSet

	// applyMu serializes planning and applying a plan, so a start, a stop and a
	// rebalance never act on plans computed from the same applied cpusets.
	applyMu sync.Mutex

	logger logr.Logger
}

const (
	// SharedPolicyNone disables the partition: every VM under shared
	// placement is left exactly as Proxmox started it, matching the
	// daemon's behavior before docs/scheduler-shared.md.
	SharedPolicyNone = "none"
	// SharedPolicyPartition confines every VM under shared placement to
	// its partition of the unpinned CPUs, per docs/scheduler-shared.md §4.
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

// WithSharedPolicy selects the shared placement strategy.
func WithSharedPolicy(policy string) HandlerOption {
	return func(h *SchedulerHandler) {
		switch policy {
		case "":
			// keep the default
		case SharedPolicyNone:
			h.partition = false
		case SharedPolicyPartition:
			h.partition = true
		default:
			// An unrecognized value (e.g. a typo) must never silently
			// fall through to partitioning — that would change production
			// VM affinity on a misconfiguration. Warn and keep whatever
			// the default already is.
			h.logger.Error(fmt.Errorf("unknown shared CPU pool policy %q", policy), "Ignoring unrecognized --shared-policy, keeping the default",
				"policy", policy, "validValues", []string{SharedPolicyNone, SharedPolicyPartition}, "partition", h.partition)
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

// NewHandler returns a handler for the host's topology. It fails if the zone
// capacity cannot be set up, as the handler could then place no VM.
func NewHandler(client *local.Client, topology *topology.Topology, logger logr.Logger, opts ...HandlerOption) (*SchedulerHandler, error) {
	h := &SchedulerHandler{
		client:       client,
		tracker:      newVMTracker(),
		reservedCPUs: cpuset.New(),
		groupCPUs:    cpuset.New(),
		partition:    true,
		debounce:     2 * time.Second,
		logger:       logger,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}

	if topology == nil {
		return nil, fmt.Errorf("a CPU topology is required")
	}

	zone, err := zonecapacity.New(zonecapacity.Host{Topology: topology, ReservedCPUs: h.reservedCPUs})
	if err != nil {
		return nil, fmt.Errorf("failed to set up zone capacity: %w", err)
	}

	h.zone = zone

	return h, nil
}

// VMsRunning tracks exactly the VMs in running (VM ID to PID). It never runs taskset
// itself: a pinned VM's affinity is taken as applied, as Proxmox started it with it.
func (r *SchedulerHandler) VMsRunning(ctx context.Context, running map[int]int) {
	r.applyMu.Lock()

	for _, vmID := range r.tracker.notIn(running) {
		r.forgetVM(vmID)
	}

	r.applyMu.Unlock()

	for vmID, pid := range running {
		vmConfig, err := loadVMConfig(ctx, r.client, vmID)
		if err != nil {
			// Keep whatever the VM held so far: it still runs.
			r.logger.Error(err, "Failed to load VM config for running VM", "vmID", vmID)

			continue
		}

		r.applyMu.Lock()
		err = r.trackVM(vmID, pid, vmConfig, false)
		r.applyMu.Unlock()

		if err != nil {
			r.logger.Error(err, "Failed to track VM", "vmID", vmID, "pid", pid)
		}
	}

	r.applyMu.Lock()

	for vmID, a := range r.plan().Changed {
		vm, ok := r.tracker.get(vmID)
		if !ok {
			continue
		}

		switch a.Placement {
		case zonecapacity.PlacementPinned:
			r.tracker.setApplied(vmID, vm.pid, a.CPUs)
		case zonecapacity.PlacementCPUShare:
			// Nothing is persisted: members with equal masks form a group again.
			if mask, ok := threadsMask(vm.pid); ok && vm.applied.IsEmpty() {
				r.tracker.setApplied(vmID, vm.pid, mask)
			}
		case zonecapacity.PlacementShared, zonecapacity.PlacementConstrained:
			// Placed by the start path and the rebalance.
		}
	}

	r.applyMu.Unlock()

	r.logVMStatus()

	// The VM set may have changed without a corresponding start/stop
	// event being observed (e.g. events missed while the daemon was
	// down), so a resync always re-evaluates the partition too.
	r.scheduleRebalance(ctx)
}

// threadsMask is the CPU mask every vCPU thread of process pid has; false when the
// threads cannot be read or have different masks.
func threadsMask(pid int) (cpuset.CPUSet, bool) {
	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	if err != nil || len(threads) == 0 {
		return cpuset.New(), false
	}

	mask := cpuset.New()

	for i, tid := range threads {
		cpus, err := utilsys.GetThreadAffinity(pid, tid)
		if err != nil || (i > 0 && !cpus.Equals(mask)) {
			return cpuset.New(), false
		}

		mask = cpus
	}

	return mask, true
}

// logVMStatus logs the current status of all tracked VMs
func (r *SchedulerHandler) logVMStatus() {
	for vmID, vm := range r.tracker.all() {
		r.logger.V(2).Info("VM info", "vmID", vmID, "pid", vm.pid, "applied", vm.applied.String())
	}
}

// scheduleRebalance coalesces a burst of start/stop/sync events into a
// single rebalance, fired after r.debounce of quiescence. It runs whatever
// --shared-policy says: CPU share groups are always planned.
// A zero debounce rebalances inline, which is what tests want.
func (r *SchedulerHandler) scheduleRebalance(ctx context.Context) {
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
		Partition: r.partition,
		MinWidth:  r.sharedMinWidth,
	})
}

// rebalance applies the plan to every CPU share VM and every VM under shared
// placement whose cpuset changed. Pinned and constrained VMs are placed when they
// start and never touched here. Best-effort throughout: a failure applying one VM
// is logged and does not abort the others.
func (r *SchedulerHandler) rebalance(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// A dead VM's CPUs were still in this plan; plan again without it.
	if r.applyPlan(ctx) {
		r.scheduleRebalance(ctx)
	}
}

// applyPlan plans and applies the CPU share groups and the ungrouped CPUs under applyMu.
// It reports whether it dropped a VM whose process had died.
func (r *SchedulerHandler) applyPlan(ctx context.Context) (droppedDead bool) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()

	plan := r.plan()
	r.logNotes(plan)

	for _, vmID := range slices.Sorted(maps.Keys(plan.Changed)) {
		a := plan.Changed[vmID]
		if a.Placement != zonecapacity.PlacementShared && a.Placement != zonecapacity.PlacementCPUShare {
			continue
		}

		if ctx.Err() != nil {
			return droppedDead
		}

		vm, ok := r.tracker.get(vmID)
		if !ok {
			continue
		}

		if !utilsys.ProcessExists(vm.pid) {
			// Its stop event is on the way; until then it must not hold CPUs nor be retried.
			r.logger.V(1).Info("VM process no longer exists, skipping", "vmID", vmID, "pid", vm.pid)

			if r.tracker.forgetPID(vmID, vm.pid) {
				r.zone.Release(vmID)

				droppedDead = true
			}

			continue
		}

		if err := r.applyCPUs(ctx, vmID, vm.pid, a); err != nil {
			r.logger.Error(err, "Failed to apply CPU plan to VM", "vmID", vmID)

			continue
		}

		if a.Placement == zonecapacity.PlacementCPUShare {
			r.logger.Info("Seated VM in its CPU share group", "vmID", vmID, "cpus", a.CPUs.String())
			r.steerIRQs(ctx, vmID, a.CPUs)

			continue
		}

		r.logger.Info("Rebalanced shared VM", "vmID", vmID, "cpus", a.CPUs.String())
	}

	r.setGovernors(plan)

	return droppedDead
}

// setGovernors sets the governors of the unpinned CPUs after a rebalance, per
// --shared-policy. The governors of pinned and constrained VMs are set when they
// start and stop.
func (r *SchedulerHandler) setGovernors(plan zonecapacity.Plan) {
	if r.partition {
		r.setPartitionGovernors(plan)
	} else {
		r.setGroupGovernors(plan)
	}

	r.groupCPUs = plan.Groups
}

// setPartitionGovernors sets the busy governor on the unpinned CPUs some CPU share
// group or partition holds, and the free governor on the rest of them.
func (r *SchedulerHandler) setPartitionGovernors(plan zonecapacity.Plan) {
	busy := plan.Busy.Intersection(plan.Unpinned)

	r.setGovernor(0, busy, *cpuGovernorBusy)
	r.setGovernor(0, plan.Unpinned.Difference(busy), *cpuGovernorFree)
}

// setGroupGovernors governs only the CPU share groups, as VMs under shared placement
// are not partitioned: the busy governor on the CPUs the groups hold, and the free
// governor on the unpinned CPUs they held at the last rebalance and gave up.
func (r *SchedulerHandler) setGroupGovernors(plan zonecapacity.Plan) {
	r.setGovernor(0, plan.Groups, *cpuGovernorBusy)
	r.setGovernor(0, r.groupCPUs.Intersection(plan.Unpinned).Difference(plan.Groups), *cpuGovernorFree)
}

// steerIRQs steers the passthrough devices' IRQs of VM vmID to cpus, reading its config.
func (r *SchedulerHandler) steerIRQs(ctx context.Context, vmID int, cpus cpuset.CPUSet) {
	if r.client == nil {
		return
	}

	vmConfig, err := loadVMConfig(ctx, r.client, vmID)
	if err != nil {
		r.logger.Error(err, "Failed to load VM config to steer its IRQs", "vmID", vmID)

		return
	}

	r.steerDeviceIRQs(vmID, vmConfig, cpus)
}

// applyCPUs sets a's CPUs on VM vmID's threads and records them as applied once
// taskset succeeded. A pinned VM gets one vCPU thread per CPU; any other VM's threads
// and process are masked to the CPUs.
func (r *SchedulerHandler) applyCPUs(ctx context.Context, vmID, pid int, a zonecapacity.Assignment) error {
	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	if err != nil {
		return fmt.Errorf("failed to get threads of VM %d: %w", vmID, err)
	}

	if a.OneToOne() {
		if err := utilsys.PinThreadsToCores(ctx, vmID, pid, threads, a.CPUs.List()); err != nil {
			return fmt.Errorf("failed to pin VM %d threads to cores: %w", vmID, err)
		}
	} else {
		if err := utilsys.SetThreadsAffinity(ctx, vmID, pid, threads, a.CPUs); err != nil {
			return fmt.Errorf("failed to mask VM %d threads: %w", vmID, err)
		}

		if err := utilsys.SetProcessAffinity(ctx, vmID, pid, a.CPUs); err != nil {
			r.logger.Error(err, "Failed to mask VM process", "vmID", vmID)
		}
	}

	r.tracker.setApplied(vmID, pid, a.CPUs)

	return nil
}

// logNotes logs what a plan could not do.
func (r *SchedulerHandler) logNotes(plan zonecapacity.Plan) {
	for _, note := range plan.Notes {
		r.logger.Info("CPU plan note", "note", note)
	}
}

// setGovernor sets the CPU governor on cpus, a no-op if either is empty.
// vmID 0 is a sweep over many VMs' CPUs, logged only at debug level.
func (r *SchedulerHandler) setGovernor(vmID int, cpus cpuset.CPUSet, governor string) {
	if cpus.IsEmpty() || governor == "" {
		return
	}

	log := r.logger
	if vmID == 0 {
		log = log.V(1)
	}

	log.Info("VM governing CPUs", "vmID", vmID, "governor", governor, "cores", cpus.String())

	if err := utilsys.SetCPUGovernor(vmID, cpus.List(), governor); err != nil {
		r.logger.Error(err, "Failed to set CPU governor", "vmID", vmID, "governor", governor, "cores", cpus.String())
	}
}
