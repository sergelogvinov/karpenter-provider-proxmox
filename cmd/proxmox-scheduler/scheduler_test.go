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
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/fakelocal"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// dualNodeTopology is a small 2-NUMA-node, SMT topology used across this
// file's tests: node 0 = {0,1,4,5}, node 1 = {2,3,6,7} (0/4, 1/5, 2/6,
// 3/7 are SMT sibling pairs).
func dualNodeTopology() *topology.Topology {
	return &topology.Topology{ //nolint:modernize // MemTopology is a second embedded field; the suggested elision does not compile
		NumCPUs:      8,
		NumCores:     4,
		NumSockets:   2,
		NumNUMANodes: 2,
		TotalMemory:  16 << 30,
		NUMANodes:    map[int]uint64{0: 8 << 30, 1: 8 << 30},
		CPUDetails: topology.CPUDetails{
			0: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2: {CoreID: 2, SocketID: 1, NUMANodeID: 1},
			3: {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			4: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			5: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			6: {CoreID: 2, SocketID: 1, NUMANodeID: 1},
			7: {CoreID: 3, SocketID: 1, NUMANodeID: 1},
		},
	}
}

// liveTopology returns a single-NUMA-node topology built from the CPU
// ids the test process is actually allowed to run on — used by tests
// that apply an affinity mask via taskset to a real process, so every
// CPU id the planner hands out is guaranteed to both exist on the host
// and be usable by that process. Building this from runtime.NumCPU()
// alone would assume ids 0..n-1, which does not hold under a restricted
// cpuset (e.g. a container pinned to CPUs 4-7).
func liveTopology(t *testing.T) *topology.Topology {
	t.Helper()

	allowed, err := utilsys.GetThreadAffinity(os.Getpid(), os.Getpid())
	require.NoError(t, err)
	require.GreaterOrEqual(t, allowed.Size(), 2, "this test needs at least 2 real CPUs")

	ids := allowed.List()
	details := make(topology.CPUDetails, len(ids))

	for i, cpu := range ids {
		details[cpu] = topology.CPUInfo{CoreID: i, SocketID: 0, NUMANodeID: 0}
	}

	return &topology.Topology{ //nolint:modernize // MemTopology is a second embedded field; the suggested elision does not compile
		NumCPUs:      len(ids),
		NumCores:     len(ids),
		NumSockets:   1,
		NumNUMANodes: 1,
		TotalMemory:  16 << 30,
		NUMANodes:    map[int]uint64{0: 16 << 30},
		CPUDetails:   details,
	}
}

// spawnFakeQemuThread starts a real, short-lived process whose main
// thread's comm contains "CPU" — matching the filter
// utilsys.GetProcessThreads(pid, "CPU") uses to find a QEMU guest's vCPU
// threads — so handleVMStart's thread-affinity logic can be driven
// end-to-end against a real PID instead of a fabricated one. The shell
// renames itself (comm survives that, unlike an exec) and then runs
// sleep as its child; both are killed via their process group on
// cleanup.
func spawnFakeQemuThread(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", "-c", "printf CPU0 > /proc/self/comm; sleep 5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	require.NoError(t, cmd.Start())

	pid := cmd.Process.Pid

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	require.Eventually(t, func() bool {
		threads, err := utilsys.GetProcessThreads(pid, "CPU")

		return err == nil && len(threads) > 0
	}, 2*time.Second, 10*time.Millisecond, "spawned process never got a CPU-named thread")

	return pid
}

// newHandler is NewHandler for a test, which must succeed.
func newHandler(t *testing.T, client *local.Client, tp *topology.Topology, opts ...HandlerOption) *SchedulerHandler {
	t.Helper()

	h, err := NewHandler(client, tp, logr.Discard(), opts...)
	require.NoError(t, err)

	return h
}

// track records a running VM with its config, as a sync does.
func track(t *testing.T, h *SchedulerHandler, vmID, pid int, cfg *qemu.Config) {
	t.Helper()

	require.NoError(t, h.trackVM(vmID, pid, cfg, false))
}

// deadPID is never a real process.
const deadPID = 1<<31 - 1

// TestNewHandlerFailsWithoutZone checks that a handler that cannot plan refuses to start,
// rather than running without placing any VM.
func TestNewHandlerFailsWithoutZone(t *testing.T) {
	_, err := NewHandler(nil, nil, logr.Discard())
	require.Error(t, err, "no topology")

	_, err = NewHandler(nil, dualNodeTopology(), logr.Discard(), WithReservedCPUs(cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8)))
	require.Error(t, err, "more reserved CPUs than the host has")
}

// TestSyncTracksWithoutApplying checks that a sync under --shared-policy=none never runs
// taskset, and takes a pinned VM's affinity as applied, as it always did.
func TestSyncTracksWithoutApplying(t *testing.T) {
	tp := dualNodeTopology()

	f := fakelocal.New(t,
		fakelocal.WithGuest(100, "name: pinned\ncores: 2\naffinity: 0,4\n"),
		fakelocal.WithGuest(101, "name: constrained\ncores: 1\naffinity: 1,5\n"),
		fakelocal.WithGuest(102, "name: shared\ncores: 2\n"),
	)
	h := newHandler(t, f.Client(), tp, WithSharedPolicy(SharedPolicyNone), WithRebalanceDebounce(0))

	h.syncRunning(t.Context(), map[int]int{100: deadPID, 101: deadPID, 102: deadPID})

	for id, want := range map[int]cpuset.CPUSet{100: cpuset.New(0, 4), 101: cpuset.New(), 102: cpuset.New()} {
		vm, ok := h.tracker.get(id)
		require.True(t, ok, "VM %d is tracked", id)
		assert.True(t, vm.applied.Equals(want), "VM %d applied %v, want %v", id, vm.applied, want)
	}

	assert.True(t, busyCPUs(h).Equals(cpuset.New(0, 1, 4, 5)), "got %v", busyCPUs(h))
}

// busyCPUs are the CPUs the handler's plan holds for some VM.
func busyCPUs(h *SchedulerHandler) cpuset.CPUSet {
	return h.plan().Busy
}

// TestHandleVMStopReleasesVM checks that a stopped VM's CPUs are freed while
// another VM's, even overlapping ones, stay held.
func TestHandleVMStopReleasesVM(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,1"})
	track(t, h, 101, 2, &qemu.Config{Cores: new(2), Affinity: "1,5"})
	require.True(t, busyCPUs(h).Equals(cpuset.New(0, 1, 5)), "got %v", busyCPUs(h))

	h.handleVMStop(t.Context(), 100)

	_, ok := h.tracker.get(100)
	assert.False(t, ok)
	assert.True(t, busyCPUs(h).Equals(cpuset.New(1, 5)), "CPU 1 is still VM 101's, CPU 0 is free: got %v", busyCPUs(h))
}

// TestTrackIgnoresOnlyNodeCapacityGuest checks that the node-capacity guest never holds CPUs,
// while a workload VM that also carries the karpenter tag does.
func TestTrackIgnoresOnlyNodeCapacityGuest(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone))

	track(t, h, 999, 1, &qemu.Config{Name: "node-capacity", Cores: new(8), Affinity: "0-7", Tags: &qemu.Tags{"karpenter"}})
	assert.True(t, busyCPUs(h).IsEmpty(), "the node-capacity guest's affinity is never counted: got %v", busyCPUs(h))

	track(t, h, 100, 2, &qemu.Config{Name: "worker-1", Cores: new(2), Affinity: "2,6", Tags: &qemu.Tags{"karpenter"}})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(2, 6)), "a tagged workload VM is counted: got %v", busyCPUs(h))
}

// TestTrackReadsMultiRangeDescriptionAffinity checks that an affinity= description token
// with several ranges is read whole, not cut at its first comma.
func TestTrackReadsMultiRangeDescriptionAffinity(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(4), Description: "Karpenter managed, affinity=1-2,5-6, class=default"})

	plan := h.plan()
	assert.True(t, plan.Changed[100].CPUs.Equals(cpuset.New(1, 2, 5, 6)), "got %v", plan.Changed[100])
	assert.True(t, plan.Changed[100].OneToOne(), "4 cores on 4 CPUs are pinned 1:1")
}

// TestRebalanceTouchesOnlySharedPlacement checks that a rebalance never runs taskset on a
// pinned VM, and that a VM under shared placement whose process has exited is skipped
// and dropped, not retried on every rebalance until its stop event.
func TestRebalanceTouchesOnlySharedPlacement(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyPartition), WithRebalanceDebounce(0))

	track(t, h, 100, deadPID, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	track(t, h, 200, deadPID, &qemu.Config{Cores: new(2)})
	require.Contains(t, h.plan().Changed, 100)
	require.Contains(t, h.plan().Changed, 200)

	h.scheduleRebalance(t.Context())

	_, ok := h.tracker.get(100)
	assert.True(t, ok, "the pinned VM is left to its start path")
	_, ok = h.tracker.get(200)
	assert.False(t, ok, "the dead VM is dropped")
	assert.NotContains(t, h.plan().Changed, 200, "and never retried")
	assert.True(t, busyCPUs(h).Equals(cpuset.New(0, 4)), "the dead VM holds nothing: got %v", busyCPUs(h))
}

// TestScheduleRebalanceNoopWhenPolicyNone confirms the escape hatch: with
// SharedPolicyNone, no VM is ever touched by a rebalance.
func TestScheduleRebalanceNoopWhenPolicyNone(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone), WithRebalanceDebounce(0))

	track(t, h, 100, deadPID, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	track(t, h, 200, deadPID, &qemu.Config{Cores: new(2)})

	h.scheduleRebalance(t.Context())

	for _, id := range []int{100, 200} {
		vm, ok := h.tracker.get(id)
		require.True(t, ok, "VM %d was not touched", id)
		assert.True(t, vm.applied.IsEmpty())
	}
}

// TestHandleVMStartSharedPolicyNoneNeverTouchesAffinity drives
// handleVMStart end-to-end against a real process: with
// --shared-policy=none, an unaffined VM must be left
// exactly as Proxmox started it — no provisional shared-pool mask
// applied — matching the documented "ship dark" escape hatch.
func TestHandleVMStartSharedPolicyNoneNeverTouchesAffinity(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(100, "name: shared-vm\ncores: 1\n"))
	reserved := cpuset.New(tp.CPUDetails.CPUs().List()[0])
	// Reserve a CPU: if handleVMStart masked to the unpinned CPUs despite
	// SharedPolicyNone (the bug this test guards against), the mask
	// would exclude the reserved CPU and so differ from the process's
	// inherited (unrestricted) affinity — a masking bug that always
	// narrows to "every CPU" would otherwise look like a no-op and slip
	// past this test.
	h := newHandler(t, f.Client(), tp, WithSharedPolicy(SharedPolicyNone), WithReservedCPUs(reserved))

	pid := spawnFakeQemuThread(t)

	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	require.NoError(t, err)
	require.NotEmpty(t, threads)

	before, err := utilsys.GetThreadAffinity(pid, threads[0])
	require.NoError(t, err)

	require.NoError(t, h.handleVMStart(t.Context(), 100, pid))

	after, err := utilsys.GetThreadAffinity(pid, threads[0])
	require.NoError(t, err)

	assert.True(t, before.Equals(after), "SharedPolicyNone must never touch an unaffined VM's thread affinity: had %v, now %v", before, after)
}

// TestHandleVMStartMasksSharedVMUnderPartitionPolicy is the mirror
// image: with --shared-policy=partition, an unaffined VM's threads must
// actually be confined to the unpinned CPUs (host CPUs minus reserved
// CPUs) right away, not left on their inherited affinity.
func TestHandleVMStartMasksSharedVMUnderPartitionPolicy(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(100, "name: shared-vm\ncores: 1\n"))
	reserved := cpuset.New(tp.CPUDetails.CPUs().List()[0])
	h := newHandler(t, f.Client(), tp,
		WithSharedPolicy(SharedPolicyPartition),
		WithReservedCPUs(reserved),
	)

	pid := spawnFakeQemuThread(t)

	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	require.NoError(t, err)
	require.NotEmpty(t, threads)

	require.NoError(t, h.handleVMStart(t.Context(), 100, pid))

	after, err := utilsys.GetThreadAffinity(pid, threads[0])
	require.NoError(t, err)

	assert.True(t, after.Equals(tp.CPUDetails.CPUs().Difference(reserved)), "expected the thread masked to all unpinned CPUs, got %v", after)

	vm, ok := h.tracker.get(100)
	require.True(t, ok)
	assert.True(t, vm.applied.IsEmpty(), "the provisional mask is not the VM's partition: the rebalance places it")
}

// TestHandleVMStartConfinesCPUShareVMUnderPolicyNone checks that a CPU share VM is masked to
// its group whatever --shared-policy says, and the group is recorded as applied.
func TestHandleVMStartConfinesCPUShareVMUnderPolicyNone(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(301, "#Karpenter managed instance, cpushare=2\nname: e2\ncores: 1\n"))
	h := newHandler(t, f.Client(), tp, WithSharedPolicy(SharedPolicyNone), WithRebalanceDebounce(0))

	pid := spawnFakeQemuThread(t)

	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	require.NoError(t, err)
	require.NotEmpty(t, threads)

	require.NoError(t, h.handleVMStart(t.Context(), 301, pid))

	group := h.plan().Groups
	require.Equal(t, 1, group.Size(), "a 1-vCPU group")

	after, err := utilsys.GetThreadAffinity(pid, threads[0])
	require.NoError(t, err)
	assert.True(t, after.Equals(group), "expected the thread masked to its group %v, got %v", group, after)

	vm, ok := h.tracker.get(301)
	require.True(t, ok)
	assert.True(t, vm.applied.Equals(group), "the group is applied: %v", vm.applied)
	assert.Empty(t, h.plan().Changed, "the plan is applied")
}

// TestSyncRebuildsCPUShareGroupsFromMasks is docs/scheduler-cpushare.md §10, daemon restart: nothing
// is persisted, so a sync takes the masks the members' threads already have as their groups, and
// keeps them.
func TestSyncRebuildsCPUShareGroupsFromMasks(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t,
		fakelocal.WithGuest(301, "#cpushare=2\nname: e2-a\ncores: 1\n"),
		fakelocal.WithGuest(302, "#cpushare=2\nname: e2-b\ncores: 1\n"),
	)
	h := newHandler(t, f.Client(), tp, WithSharedPolicy(SharedPolicyNone), WithRebalanceDebounce(0))

	// Not the CPU the planner would pick on its own.
	group := cpuset.New(tp.CPUDetails.CPUs().List()[tp.NumCPUs-1])
	running := map[int]int{}

	for _, id := range []int{301, 302} {
		pid := spawnFakeQemuThread(t)

		threads, err := utilsys.GetProcessThreads(pid, "CPU")
		require.NoError(t, err)
		require.NoError(t, utilsys.SetThreadsAffinity(t.Context(), id, pid, threads, group))

		running[id] = pid
	}

	h.syncRunning(t.Context(), running)

	for _, id := range []int{301, 302} {
		vm, ok := h.tracker.get(id)
		require.True(t, ok)
		assert.True(t, vm.applied.Equals(group), "VM %d keeps its group %v, got %v", id, group, vm.applied)
	}

	assert.Empty(t, h.plan().Changed)
}

// TestRebalanceConfinesCPUShareVMsAndLogsNotes checks that a rebalance under --shared-policy=none
// still places CPU share VMs, and logs the groups that do not fit and the accounting drift.
func TestRebalanceConfinesCPUShareVMsAndLogsNotes(t *testing.T) {
	tp := dualNodeTopology()

	var logs []string

	logger := funcr.New(func(prefix, args string) { logs = append(logs, args) }, funcr.Options{})

	h, err := NewHandler(nil, tp, logger, WithSharedPolicy(SharedPolicyNone), WithRebalanceDebounce(0))
	require.NoError(t, err)

	track(t, h, 100, deadPID, &qemu.Config{Cores: new(3), Affinity: "0,1,4"})
	track(t, h, 301, deadPID, &qemu.Config{Cores: new(4), Description: "cpushare=2"})
	track(t, h, 401, deadPID, &qemu.Config{Cores: new(2), Description: "cpushare=4"})
	require.Contains(t, h.plan().Changed, 301)
	require.Contains(t, h.plan().Changed, 401)

	h.scheduleRebalance(t.Context())

	_, ok := h.tracker.get(301)
	assert.False(t, ok, "the rebalance tried to place the dead CPU share VM, and dropped it")

	joined := strings.Join(logs, "\n")
	assert.Contains(t, joined, "no room for 2 CPUs")
	assert.Contains(t, joined, "CPU share groups drift")
}

// TestTrackReentrantStartDoesNotLeakStaleAffinity covers a duplicate start event for a VM
// already tracked: its previous affinity must not stay held once its config has changed.
func TestTrackReentrantStartDoesNotLeakStaleAffinity(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(0, 4)), "got %v", busyCPUs(h))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "1,5"})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(1, 5)), "the old affinity (0,4) must be dropped: got %v", busyCPUs(h))
}

// TestTrackNewPIDForgetsAppliedCPUs checks that a VM restarted with a new PID is planned
// afresh: the cpuset applied to its old process says nothing about the new one.
func TestTrackNewPIDForgetsAppliedCPUs(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	h.tracker.setApplied(100, 1, cpuset.New(0, 4))
	assert.Empty(t, h.plan().Changed)

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.Empty(t, h.plan().Changed, "the same process keeps its applied cpuset")

	track(t, h, 100, 2, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.Contains(t, h.plan().Changed, 100, "a new process must be pinned again")
}

// TestTrackerForgetPIDKeepsRestartedVM checks that dropping a dead process never drops
// the process that replaced it: a rebalance may still hold the old PID.
func TestTrackerForgetPIDKeepsRestartedVM(t *testing.T) {
	tr := newVMTracker()
	tr.track(100, 1, true)
	tr.track(100, 2, true)

	assert.False(t, tr.forgetPID(100, 1), "the old process is already gone")

	vm, ok := tr.get(100)
	require.True(t, ok, "the new process stays tracked")
	assert.Equal(t, 2, vm.pid)

	assert.True(t, tr.forgetPID(100, 2))
	_, ok = tr.get(100)
	assert.False(t, ok)
}

// TestWithSharedPolicyRejectsUnknownValue confirms a typo'd or otherwise
// unrecognized --shared-policy value is ignored, keeping the default
// partition.
func TestWithSharedPolicyRejectsUnknownValue(t *testing.T) {
	tp := dualNodeTopology()

	h := newHandler(t, nil, tp, WithSharedPolicy("parition")) // typo

	assert.True(t, h.partition, "an unrecognized policy must keep the default partition")
}
