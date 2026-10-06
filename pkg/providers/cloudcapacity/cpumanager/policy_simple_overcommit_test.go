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

package cpumanager

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// TestSimpleCPUOvercommit verifies resource accounting and admission for the opt-in policy.
func TestSimpleCPUOvercommitLifecycle(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, 2)
	require.NoError(t, err)
	require.Equal(t, 24, policy.AvailableCPUs())
	// Existing VMs consume the same budget as newly admitted VMs.
	existing := &resources.VMResources{ID: 1, CPUs: 18, Memory: 8 << 30}
	require.NoError(t, policy.AllocateOrUpdate(existing))
	require.Equal(t, 6, policy.AvailableCPUs())
	require.Contains(t, policy.Status(), "CPU: Free: 6,")

	added := &resources.VMResources{ID: 2, CPUs: 6, Memory: 8 << 30}
	require.NoError(t, policy.Allocate(added))
	require.Zero(t, policy.AvailableCPUs())
	require.ErrorContains(t, policy.Allocate(&resources.VMResources{ID: 3, CPUs: 1, Memory: 1 << 30}), "not enough CPUs")
	require.Equal(t, uint64(16<<30), policy.AvailableMemory())
	require.NoError(t, policy.Release(added))
	require.Equal(t, 6, policy.AvailableCPUs())
	require.NoError(t, policy.Release(existing))
	require.Equal(t, 24, policy.AvailableCPUs())
	require.Equal(t, uint64(32<<30), policy.AvailableMemory())
}

func TestSimpleCPUOvercommitReservationsAndAffinity(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, []int{0, 1}, 4<<30, 1.5)
	require.NoError(t, err)
	require.Equal(t, 15, policy.AvailableCPUs())
	// Affinity allocations use real CPU IDs and overlapping masks are counted once.

	first := &resources.VMResources{ID: 1, CPUs: 4, CPUSet: cpuset.New(0, 2, 3, 4), Memory: 4 << 30}
	second := &resources.VMResources{ID: 2, CPUs: 2, CPUSet: cpuset.New(3, 4), Memory: 4 << 30}

	require.NoError(t, policy.AllocateOrUpdate(first))
	require.Equal(t, 10, policy.AvailableCPUs()) // floor((12 - 2 - 3) * 1.5)
	require.NoError(t, policy.AllocateOrUpdate(second))
	require.Equal(t, 10, policy.AvailableCPUs())

	shared := &resources.VMResources{ID: 3, CPUs: 10, Memory: 4 << 30}
	require.NoError(t, policy.Allocate(shared))
	require.Zero(t, policy.AvailableCPUs())
	require.Equal(t, uint64(16<<30), policy.AvailableMemory())
	require.NoError(t, policy.Release(shared))
	require.Equal(t, 10, policy.AvailableCPUs())
}

func TestSimpleCPUOvercommitMemoryRemainsStrict(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 8 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 2<<30, 2)
	require.NoError(t, err)
	require.NoError(t, policy.AllocateOrUpdate(&resources.VMResources{ID: 1, CPUs: 16, Memory: 5 << 30}))
	require.ErrorContains(t, policy.Allocate(&resources.VMResources{ID: 2, CPUs: 1, Memory: 2 << 30}), "not enough memory")
	require.Equal(t, 8, policy.AvailableCPUs())
	require.Equal(t, uint64(1<<30), policy.AvailableMemory())
}

func TestSimpleCPUOvercommitInvalidRatios(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 8 << 30}
	for _, ratio := range []float64{0, -1, 0.5, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64} {
		_, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, ratio)
		require.Error(t, err)
	}
}

func TestSimpleCPUOvercommitAffinityRelease(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, []int{0, 1}, 0, 2)
	require.NoError(t, err)

	pinned := &resources.VMResources{ID: 1, CPUs: 3, CPUSet: cpuset.New(0, 2, 3), Memory: 4 << 30}
	require.NoError(t, policy.AllocateOrUpdate(pinned))
	require.Equal(t, 16, policy.AvailableCPUs())

	shared := &resources.VMResources{ID: 2, CPUs: 14, Memory: 4 << 30}
	require.NoError(t, policy.Allocate(shared))
	require.Equal(t, 2, policy.AvailableCPUs())
	require.NoError(t, policy.Release(pinned))
	require.Equal(t, 6, policy.AvailableCPUs())
	require.Contains(t, policy.Status(), "Common: [2-11]")
	require.NoError(t, policy.Release(shared))
	require.Equal(t, 20, policy.AvailableCPUs())
}

// TestSimpleCPUOvercommitOverlappingRelease keeps shared affinity owned until its last VM leaves.
func TestSimpleCPUOvercommitOverlappingRelease(t *testing.T) {
	t.Parallel()

	for _, firstID := range []int{1, 2} {
		t.Run(fmt.Sprintf("release VM %d first", firstID), func(t *testing.T) {
			t.Parallel()

			topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
			policy, err := NewSimplePolicyWithCPUOvercommit(topo, []int{0, 1}, 0, 2)
			require.NoError(t, err)

			first := &resources.VMResources{ID: 1, CPUs: 3, CPUSet: cpuset.New(0, 3, 4), Memory: 4 << 30}
			second := &resources.VMResources{ID: 2, CPUs: 2, CPUSet: cpuset.New(3, 4), Memory: 4 << 30}

			require.NoError(t, policy.AllocateOrUpdate(first))
			require.NoError(t, policy.AllocateOrUpdate(second))

			require.NoError(t, policy.AllocateOrUpdate(first)) // Re-observation is idempotent.
			require.Equal(t, uint64(24<<30), policy.AvailableMemory())

			shared := &resources.VMResources{ID: 3, CPUs: 16, Memory: 4 << 30}
			require.NoError(t, policy.Allocate(shared))
			require.NoError(t, policy.Release(&resources.VMResources{ID: firstID}))
			require.Zero(t, policy.AvailableCPUs())
			require.Contains(t, policy.Status(), "Static: [3-4]")
			require.ErrorContains(t, policy.Allocate(&resources.VMResources{ID: 4, CPUs: 1, Memory: 1 << 30}), "not enough CPUs")
			require.NoError(t, policy.Release(&resources.VMResources{ID: firstID})) // Duplicate deletion is harmless.
			require.NoError(t, policy.Release(&resources.VMResources{ID: 99}))      // An unknown VM cannot free usage.
			require.Equal(t, uint64(24<<30), policy.AvailableMemory())
			require.NoError(t, policy.Release(&resources.VMResources{ID: 3 - firstID}))
			require.Equal(t, 4, policy.AvailableCPUs())
			require.Contains(t, policy.Status(), "Static: []")
			require.Contains(t, policy.Status(), "Reserved: [0-1]")
		})
	}
}

// TestSimpleCPUOvercommitRatioReduction reconstructs all usage after a smaller CPU budget is configured.
func TestSimpleCPUOvercommitRatioReduction(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	before, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, 2)
	require.NoError(t, err)

	first := &resources.VMResources{ID: 1, CPUs: 10, Memory: 8 << 30}
	second := &resources.VMResources{ID: 2, CPUs: 8, Memory: 8 << 30}

	require.NoError(t, before.Allocate(first))
	require.NoError(t, before.Allocate(second))

	after, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, 1)
	require.NoError(t, err)
	require.NoError(t, after.AllocateOrUpdate(first))
	require.NoError(t, after.AllocateOrUpdate(second))
	require.NoError(t, after.AllocateOrUpdate(first))
	require.Zero(t, after.AvailableCPUs())
	require.Equal(t, uint64(16<<30), after.AvailableMemory())
	require.Contains(t, after.Status(), "CPU: Free: 0,")
	require.Contains(t, after.Status(), "Mem: 16384M")
	require.ErrorContains(t, after.Allocate(&resources.VMResources{ID: 3, CPUs: 1, Memory: 1 << 30}), "not enough CPUs")
	require.NoError(t, after.Release(second))
	require.Equal(t, 2, after.AvailableCPUs())
	require.Equal(t, uint64(24<<30), after.AvailableMemory())
	require.NoError(t, after.Allocate(&resources.VMResources{ID: 3, CPUs: 2, Memory: 1 << 30}))
}

// TestSimpleCPUOvercommitObservedMemory records over-budget memory without permitting new memory overcommit.
func TestSimpleCPUOvercommitObservedMemory(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 8 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 2<<30, 2)
	require.NoError(t, err)
	require.NoError(t, policy.AllocateOrUpdate(&resources.VMResources{ID: 1, CPUs: 2, Memory: 4 << 30}))
	require.NoError(t, policy.AllocateOrUpdate(&resources.VMResources{ID: 2, CPUs: 2, Memory: 4 << 30}))
	require.Zero(t, policy.AvailableMemory())
	require.Equal(t, 20, policy.AvailableCPUs())
	require.ErrorContains(t, policy.Allocate(&resources.VMResources{ID: 3, CPUs: 1, Memory: 1 << 30}), "not enough memory")
	require.NoError(t, policy.Release(&resources.VMResources{ID: 2}))
	require.Equal(t, uint64(2<<30), policy.AvailableMemory())
}

// TestSimpleCPUOvercommitUpdates replaces each VM's prior CPU, affinity, and memory snapshot.
func TestSimpleCPUOvercommitUpdates(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	policy, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, 2)
	require.NoError(t, err)

	first := &resources.VMResources{ID: 1, CPUs: 2, CPUSet: cpuset.New(3, 4), Memory: 4 << 30}
	second := &resources.VMResources{ID: 2, CPUs: 2, CPUSet: cpuset.New(3, 4), Memory: 4 << 30}

	require.NoError(t, policy.AllocateOrUpdate(first))
	require.NoError(t, policy.AllocateOrUpdate(second))

	first.CPUSet = cpuset.New(5)
	first.CPUs = 1
	first.Memory = 8 << 30

	require.Equal(t, 20, policy.AvailableCPUs()) // Caller mutation cannot change the recorded snapshot.

	require.NoError(t, policy.AllocateOrUpdate(first))
	require.Equal(t, 18, policy.AvailableCPUs())
	require.Equal(t, uint64(20<<30), policy.AvailableMemory())

	first.CPUSet = cpuset.New()

	require.NoError(t, policy.AllocateOrUpdate(first))
	require.Equal(t, 19, policy.AvailableCPUs())
	require.NoError(t, policy.Release(&resources.VMResources{ID: 1, CPUs: 99, CPUSet: cpuset.New(3, 4), Memory: 31 << 30}))
	require.Equal(t, 20, policy.AvailableCPUs()) // Release uses the recorded state, not stale metadata.
	require.Equal(t, uint64(28<<30), policy.AvailableMemory())
}

// TestSimpleCPUOvercommitIdentity confirms explicit policy selection and preserves legacy simple admission.
func TestSimpleCPUOvercommitIdentity(t *testing.T) {
	t.Parallel()

	topo := &topology.Topology{CPUTopology: *topoDualSocketHT, TotalMemory: 32 << 30}
	simple, err := NewSimplePolicy(topo, nil, 0)
	require.NoError(t, err)
	require.Equal(t, "simple", simple.Name())
	require.Equal(t, 12, simple.AvailableCPUs())
	require.ErrorContains(t, simple.Allocate(&resources.VMResources{ID: 1, CPUs: 18, Memory: 8 << 30}), "not enough CPUs")

	overcommit, err := NewSimplePolicyWithCPUOvercommit(topo, nil, 0, 2)
	require.NoError(t, err)
	require.Equal(t, "simple-with-cpu-overcommit", overcommit.Name())
	require.NoError(t, overcommit.Allocate(&resources.VMResources{ID: 1, CPUs: 18, Memory: 8 << 30}))
	require.ErrorContains(t, overcommit.Allocate(&resources.VMResources{ID: 1, CPUs: 1, Memory: 1 << 30}), "already allocated")
}
