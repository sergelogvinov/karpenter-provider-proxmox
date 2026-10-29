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
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// track records a running VM with its config, as a start or a sync does.
func track(t *testing.T, h *SchedulerHandler, vmID, pid int, cfg *qemu.Config) {
	t.Helper()

	require.NoError(t, h.trackVM(vmID, pid, cfg))
}

// busyCPUs are the CPUs the handler's plan holds for some VM.
func busyCPUs(h *SchedulerHandler) cpuset.CPUSet {
	return h.plan().Busy
}

// TestHandleVMStopReleasesVM checks that a stopped VM's CPUs are freed while
// another VM's, even overlapping ones, stay held.
func TestHandleVMStopReleasesVM(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,1"})
	track(t, h, 101, 2, &qemu.Config{Cores: new(2), Affinity: "1,5"})
	require.True(t, busyCPUs(h).Equals(cpuset.New(0, 1, 5)), "got %v", busyCPUs(h))

	h.handleVMStop(t.Context(), 100)

	assert.NotContains(t, h.tracker.vms, 100)
	assert.True(t, busyCPUs(h).Equals(cpuset.New(1, 5)), "CPU 1 is still VM 101's, CPU 0 is free: got %v", busyCPUs(h))
}

// TestTrackIgnoresOnlyNodeCapacityGuest checks that the node-capacity guest never holds CPUs,
// while a workload VM that also carries the karpenter tag does.
func TestTrackIgnoresOnlyNodeCapacityGuest(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	track(t, h, 999, 1, &qemu.Config{Name: "node-capacity", Cores: new(8), Affinity: "0-7", Tags: &qemu.Tags{"karpenter"}})
	assert.True(t, busyCPUs(h).IsEmpty(), "the node-capacity guest's affinity is never counted: got %v", busyCPUs(h))

	track(t, h, 100, 2, &qemu.Config{Name: "worker-1", Cores: new(2), Affinity: "2,6", Tags: &qemu.Tags{"karpenter"}})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(2, 6)), "a tagged workload VM is counted: got %v", busyCPUs(h))
}

// TestTrackReadsMultiRangeDescriptionAffinity checks that an affinity= description token
// with several ranges is read whole, not cut at its first comma.
func TestTrackReadsMultiRangeDescriptionAffinity(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(4), Description: "Karpenter managed, affinity=1-2,5-6, class=default"})

	plan := h.plan()
	assert.True(t, plan.Changed[100].Equals(cpuset.New(1, 2, 5, 6)), "got %v", plan.Changed[100])
	assert.True(t, plan.OneToOne[100], "4 cores on 4 CPUs are pinned 1:1")
}

// TestRebalanceSharedSkipsDeadProcess drives rebalanceShared's apply
// step against a VM whose process has since exited (a stop event racing
// the debounce timer) — it must skip that VM rather than fail the whole
// rebalance, and must leave the tracker's AssignedSet untouched so the
// next rebalance retries it.
func TestRebalanceSharedSkipsDeadProcess(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(),
		WithSharedPolicy(SharedPolicyPartition),
		WithRebalanceDebounce(0), // synchronous, for the test
	)

	const deadPID = 1<<31 - 1 // never a real PID

	track(t, h, 200, deadPID, &qemu.Config{Cores: new(2)})
	require.Contains(t, h.plan().Changed, 200, "the floating VM is planned")

	h.scheduleRebalance(t.Context())

	assert.True(t, h.tracker.vms[200].applied.IsEmpty(), "apply must be skipped for a dead process, not recorded as done")
	assert.Contains(t, h.plan().Changed, 200, "the next rebalance retries it")
}

// TestScheduleRebalanceNoopWhenPolicyNone confirms the escape hatch:
// with SharedPolicyNone, a shared VM is
// never touched at all, even across a synchronous rebalance.
func TestScheduleRebalanceNoopWhenPolicyNone(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(),
		WithSharedPolicy(SharedPolicyNone),
		WithRebalanceDebounce(0),
	)

	track(t, h, 200, 1, &qemu.Config{Cores: new(2)})

	h.scheduleRebalance(t.Context())

	assert.True(t, h.tracker.vms[200].applied.IsEmpty())
	assert.Empty(t, h.plan().Changed)
}

// TestHandleVMStartSharedPolicyNoneNeverTouchesAffinity drives
// handleVMStart end-to-end against a real process: with
// --shared-policy=none (the default), an unaffined VM must be left
// exactly as Proxmox started it — no provisional shared-pool mask
// applied — matching the documented "ship dark" escape hatch.
func TestHandleVMStartSharedPolicyNoneNeverTouchesAffinity(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(100, "name: shared-vm\ncores: 1\n"))
	reserved := cpuset.New(tp.CPUDetails.CPUs().List()[0])
	// Reserve a CPU: if handleVMStart masked to the shared pool despite
	// SharedPolicyNone (the bug this test guards against), the mask
	// would exclude the reserved CPU and so differ from the process's
	// inherited (unrestricted) affinity — a masking bug that always
	// narrows to "every CPU" would otherwise look like a no-op and slip
	// past this test.
	h := NewHandler(f.Client(), tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone), WithReservedCPUs(reserved))

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
// actually be confined to the shared pool (host CPUs minus reserved
// CPUs) right away, not left on their inherited affinity.
func TestHandleVMStartMasksSharedVMUnderPartitionPolicy(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(100, "name: shared-vm\ncores: 1\n"))
	reserved := cpuset.New(tp.CPUDetails.CPUs().List()[0])
	h := NewHandler(f.Client(), tp, logr.Discard(),
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

	assert.True(t, after.Intersection(reserved).IsEmpty(), "the reserved CPU must never be handed to a shared VM, got %v", after)
	assert.True(t, after.IsSubsetOf(tp.CPUDetails.CPUs().Difference(reserved)), "expected the thread masked to the shared pool, got %v", after)
	assert.True(t, after.Equals(h.tracker.vms[100].applied), "the applied cpuset is what taskset set: %v vs %v", after, h.tracker.vms[100].applied)
}

// TestTrackReentrantStartDoesNotLeakStaleAffinity covers a duplicate start event for a VM
// already tracked: its previous affinity must not stay held once its config has changed.
func TestTrackReentrantStartDoesNotLeakStaleAffinity(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(0, 4)), "got %v", busyCPUs(h))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "1,5"})
	assert.True(t, busyCPUs(h).Equals(cpuset.New(1, 5)), "the old affinity (0,4) must be dropped: got %v", busyCPUs(h))
}

// TestTrackNewPIDForgetsAppliedCPUs checks that a VM restarted with a new PID is planned
// afresh: the cpuset applied to its old process says nothing about the new one.
func TestTrackNewPIDForgetsAppliedCPUs(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	h.tracker.setApplied(100, 1, cpuset.New(0, 4))
	assert.Empty(t, h.plan().Changed)

	track(t, h, 100, 1, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.Empty(t, h.plan().Changed, "the same process keeps its applied cpuset")

	track(t, h, 100, 2, &qemu.Config{Cores: new(2), Affinity: "0,4"})
	assert.Contains(t, h.plan().Changed, 100, "a new process must be pinned again")
}

// TestWithSharedPolicyRejectsUnknownValue confirms a typo'd or otherwise
// unrecognized --shared-policy value never silently activates
// shared-pool placement: it must be ignored, keeping the safe default.
func TestWithSharedPolicyRejectsUnknownValue(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy("parition")) // typo

	assert.Equal(t, SharedPolicyNone, h.sharedPolicy, "an unrecognized policy must not be silently treated as partition mode")
}
