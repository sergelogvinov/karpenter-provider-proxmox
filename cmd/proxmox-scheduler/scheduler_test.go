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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"

	"k8s.io/utils/cpuset"
)

// dualNodeTopology is a small 2-NUMA-node, SMT topology used across this
// file's tests: node 0 = {0,1,4,5}, node 1 = {2,3,6,7} (0/4, 1/5, 2/6,
// 3/7 are SMT sibling pairs).
func dualNodeTopology() *topology.Topology {
	return &topology.Topology{ //nolint:modernize // MemTopology is a second embedded field; the suggested elision does not compile
		CPUTopology: topology.CPUTopology{
			NumCPUs:      8,
			NumCores:     4,
			NumSockets:   2,
			NumNUMANodes: 2,
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
		CPUTopology: topology.CPUTopology{
			NumCPUs:      len(ids),
			NumCores:     len(ids),
			NumSockets:   1,
			NumNUMANodes: 1,
			CPUDetails:   details,
		},
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

func TestClassifyVM(t *testing.T) {
	tests := []struct {
		name     string
		cores    int
		affinity cpuset.CPUSet
		tags     []string
		want     VMClass
	}{
		{"no affinity is shared", 4, cpuset.New(), nil, ClassShared},
		{"1:1 affinity is pinned", 4, cpuset.New(0, 1, 2, 3), nil, ClassPinned},
		{"narrower affinity is constrained", 4, cpuset.New(0, 1), nil, ClassConstrained},
		{"wider affinity is constrained", 2, cpuset.New(0, 1, 2, 3), nil, ClassConstrained},
		{"karpenter tag is always ignored, even with 1:1 affinity", 8, cpuset.New(0, 1, 2, 3, 4, 5, 6, 7), []string{"karpenter"}, ClassIgnored},
		{"unrelated tags do not change classification", 4, cpuset.New(), []string{"env-prod"}, ClassShared},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyVM(tt.cores, tt.affinity, tt.tags))
		})
	}
}

func TestNumaHint(t *testing.T) {
	tp := dualNodeTopology()

	assert.True(t, numaHint(&tp.CPUTopology, cpuset.New()).IsEmpty(), "no current cpuset yields no hint")

	hint := numaHint(&tp.CPUTopology, cpuset.New(1, 5))
	assert.True(t, hint.Equals(cpuset.New(0, 1, 4, 5)), "hint must be the whole NUMA node, not just the cpus passed in: got %v", hint)

	// A cpuset spanning both nodes hints at both.
	spanning := numaHint(&tp.CPUTopology, cpuset.New(1, 2))
	assert.True(t, spanning.Equals(tp.CPUDetails.CPUs()))
}

func TestSharedPoolExcludesReservedAndExclusive(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(),
		WithReservedCPUs(cpuset.New(0)),
		WithSharedPolicy(SharedPolicyNone),
	)

	h.tracker.vms[100] = &VMInfo{VMID: 100, Class: ClassPinned, AffinitySet: cpuset.New(1, 5)}
	h.tracker.usedCPUs = cpuset.New(1, 5)

	pool := h.sharedPool()
	assert.True(t, pool.Equals(cpuset.New(2, 3, 4, 6, 7)), "got %v", pool)
}

// TestHandleVMStopRecomputesTrackerPools drives the fix for the bug:
// stopping one pinned VM must
// not leave another VM's still-exclusive CPUs looking free, and must not
// leave the stopped VM's own CPUs looking used.
func TestHandleVMStopRecomputesTrackerPools(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	h.tracker.vms[100] = &VMInfo{VMID: 100, Class: ClassPinned, AffinitySet: cpuset.New(0, 4)}
	h.tracker.vms[101] = &VMInfo{VMID: 101, Class: ClassPinned, AffinitySet: cpuset.New(1, 5)}
	h.tracker.usedCPUs = cpuset.New(0, 1, 4, 5)
	h.tracker.sharedCPUs = tp.CPUDetails.CPUs().Difference(h.tracker.usedCPUs)

	h.handleVMStop(t.Context(), 100)

	assert.NotContains(t, h.tracker.vms, 100)
	assert.True(t, h.tracker.usedCPUs.Equals(cpuset.New(1, 5)), "VM 101's CPUs must still be marked used, got %v", h.tracker.usedCPUs)
	assert.True(t, h.tracker.sharedCPUs.Equals(cpuset.New(0, 2, 3, 4, 6, 7)), "VM 100's CPUs must be freed back into the shared pool, got %v", h.tracker.sharedCPUs)
}

// TestHandleVMStopKeepsCPUsUsedByAnotherVM covers the case where two
// tracked VMs happen to reference overlapping CPUs (e.g. a stale/racy
// tracker entry): stopping one must never free a CPU another VM still
// exclusively owns.
func TestHandleVMStopKeepsCPUsUsedByAnotherVM(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	h.tracker.vms[100] = &VMInfo{VMID: 100, Class: ClassPinned, AffinitySet: cpuset.New(0, 1)}
	h.tracker.vms[101] = &VMInfo{VMID: 101, Class: ClassConstrained, AffinitySet: cpuset.New(1, 5)}
	h.tracker.usedCPUs = cpuset.New(0, 1, 5)

	h.handleVMStop(t.Context(), 100)

	assert.True(t, h.tracker.usedCPUs.Contains(1), "CPU 1 is still used by VM 101 and must remain in the exclusive pool")
	assert.False(t, h.tracker.usedCPUs.Contains(0), "CPU 0 was only used by the stopped VM and must be freed")
}

func TestUpdateVMInfoNeverCountsIgnoredVMTowardExclusivePool(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	cfg := &qemu.Config{
		Cores:    new(8),
		Affinity: "0-7",
		Tags:     &qemu.Tags{"karpenter"},
	}

	require.NoError(t, h.updateVMInfo(999, 1, cfg, cpuset.New()))

	assert.Equal(t, ClassIgnored, h.tracker.vms[999].Class)
	assert.True(t, h.tracker.usedCPUs.IsEmpty(), "the discovery VM's affinity must never shrink the exclusive/shared pools")
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

	h.tracker.vms[200] = &VMInfo{VMID: 200, PID: deadPID, Class: ClassShared, Cores: 2}

	h.scheduleRebalance(t.Context())

	assert.True(t, h.tracker.vms[200].AssignedSet.IsEmpty(), "apply must be skipped for a dead process, not recorded as done")
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

	h.tracker.vms[200] = &VMInfo{VMID: 200, PID: 1, Class: ClassShared, Cores: 2}

	h.scheduleRebalance(t.Context())

	assert.True(t, h.tracker.vms[200].AssignedSet.IsEmpty())
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
}

// TestUpdateVMInfoReentrantStartDoesNotLeakStaleAffinity covers a
// duplicate/re-entrant call to updateVMInfo for a VMID already in the
// tracker (e.g. a non-Remove fsnotify event racing a start): the VM's
// previous affinity must not remain counted in usedCPUs once its config
// has changed, or those CPUs leak out of the shared pool forever.
func TestUpdateVMInfoReentrantStartDoesNotLeakStaleAffinity(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy(SharedPolicyNone))

	first := &qemu.Config{Cores: new(2), Affinity: "0,4"}
	require.NoError(t, h.updateVMInfo(100, 1, first, cpuset.New()))
	assert.True(t, h.tracker.usedCPUs.Equals(cpuset.New(0, 4)), "got %v", h.tracker.usedCPUs)

	// The VM is reconfigured to a different, disjoint affinity and the
	// daemon observes another start event for the same VMID without an
	// intervening stop.
	second := &qemu.Config{Cores: new(2), Affinity: "1,5"}
	require.NoError(t, h.updateVMInfo(100, 1, second, cpuset.New()))

	assert.True(t, h.tracker.usedCPUs.Equals(cpuset.New(1, 5)), "the VM's old affinity (0,4) must be dropped, not left stacked on top of the new one: got %v", h.tracker.usedCPUs)
}

// TestWithSharedPolicyRejectsUnknownValue confirms a typo'd or otherwise
// unrecognized --shared-policy value never silently activates
// shared-pool placement: it must be ignored, keeping the safe default.
func TestWithSharedPolicyRejectsUnknownValue(t *testing.T) {
	tp := dualNodeTopology()

	h := NewHandler(nil, tp, logr.Discard(), WithSharedPolicy("parition")) // typo

	assert.Equal(t, SharedPolicyNone, h.sharedPolicy, "an unrecognized policy must not be silently treated as partition mode")
}
