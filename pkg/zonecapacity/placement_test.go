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

package zonecapacity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	"k8s.io/utils/cpuset"
)

var (
	node0CPUs = cpuset.New(0, 1, 2, 3, 8, 9, 10, 11)
	node1CPUs = cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)
)

// vcpus returns guest vCPUs first..last.
func vcpus(first, last int) cpuset.CPUSet {
	ids := []int{}
	for i := first; i <= last; i++ {
		ids = append(ids, i)
	}

	return cpuset.New(ids...)
}

func reserve(t *testing.T, z *zonecapacity.Zone, p zonecapacity.Policy, vm zonecapacity.VM) zonecapacity.Grant {
	t.Helper()

	grant, err := z.Reserve(p, vm)
	require.NoError(t, err)

	return grant
}

// trackNUMAMemoryUse pins one CPU on each NUMA node with the given memory bound to it.
func trackNUMAMemoryUse(t *testing.T, z *zonecapacity.Zone, node0, node1 uint64) {
	t.Helper()

	require.NoError(t, z.Track(zonecapacity.VM{ID: 101, CPUs: 1, Memory: node0, Affinity: "3", NUMAMemory: map[int]uint64{0: node0}}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 102, CPUs: 1, Memory: node1, Affinity: "7", NUMAMemory: map[int]uint64{1: node1}}))
}

func TestZoneReservePinned(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)

	grant := reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.Equal(t, 4, grant.CPUSet.Size())
	require.True(t, grant.CPUSet.IsSubsetOf(node0CPUs), "placed on NUMA node 0: %v", grant.CPUSet)
	require.Equal(t, map[int]zonecapacity.NUMABinding{0: {VCPUs: vcpus(0, 3), Memory: 8 * gib}}, grant.NUMA)
	require.Equal(t, []string{"affinity=" + grant.CPUSet.String()}, grant.DescriptionTokens)
	require.Equal(t, zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib}, z.Available(static))

	z.Release(1)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(static))
}

func TestZoneReservePinnedAcrossNUMANodes(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)

	grant := reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 10, Memory: 10 * gib})
	require.Equal(t, map[int]zonecapacity.NUMABinding{
		0: {VCPUs: vcpus(0, 7), Memory: 8 * gib},
		1: {VCPUs: vcpus(8, 9), Memory: 2 * gib},
	}, grant.NUMA)

	// Node 1 has 14GiB left, so a 6-CPU VM with 14GiB fits there and nowhere else.
	grant = reserve(t, z, static, zonecapacity.VM{ID: 2, CPUs: 6, Memory: 14 * gib})
	require.True(t, grant.CPUSet.IsSubsetOf(node1CPUs), "placed on NUMA node 1: %v", grant.CPUSet)
	require.Equal(t, zonecapacity.Capacity{CPUs: 0, Memory: 7 * gib}, z.Available(static))
}

func TestZonePinnedRespectsZoneMemory(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// A shared VM without NUMA binding takes 20GiB of the zone's 31GiB.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 2, Memory: 20 * gib}))

	// Each NUMA node still has 16GiB, but only 11GiB is left in the zone.
	_, err := z.Reserve(static, zonecapacity.VM{ID: 2, CPUs: 2, Memory: 12 * gib})
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
	reserve(t, z, static, zonecapacity.VM{ID: 3, CPUs: 2, Memory: 11 * gib})
}

func TestZoneOvercommittedNUMANodeDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 2, Memory: 20 * gib, Affinity: "0,8", NUMAMemory: map[int]uint64{0: 20 * gib}}))

	grant := reserve(t, z, static, zonecapacity.VM{ID: 2, CPUs: 2, Memory: 4 * gib})
	require.True(t, grant.CPUSet.IsSubsetOf(node1CPUs), "placed on NUMA node 1: %v", grant.CPUSet)
}

func TestZonePinnedKeepsRoomForSharedVMs(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 14, Memory: gib}))

	_, err := z.Reserve(static, zonecapacity.VM{ID: 2, CPUs: 4, Memory: gib})
	require.ErrorContains(t, err, "not enough CPUs")
	reserve(t, z, static, zonecapacity.VM{ID: 3, CPUs: 2, Memory: gib})
	require.Equal(t, 0, z.Available(static).CPUs)
}

func TestZonePinnedSpansNUMANodesWithoutWholeFit(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Each NUMA node keeps 7 free CPUs and 8.5GiB of free memory.
	trackNUMAMemoryUse(t, z, 7680<<20, 7680<<20)

	// No single node holds 10GiB. Packing takes the 3 whole free cores of node 0 and 2 of node 1,
	// so the 10 CPUs split 6+4 and need only 6GiB and 4GiB.
	grant := reserve(t, z, static, zonecapacity.VM{ID: 3, CPUs: 10, Memory: 10 * gib})
	require.Equal(t, map[int]zonecapacity.NUMABinding{
		0: {VCPUs: vcpus(0, 5), Memory: 6 * gib},
		1: {VCPUs: vcpus(6, 9), Memory: 4 * gib},
	}, grant.NUMA)
}

func TestZonePinnedPrefersNodeHoldingWholeVM(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Both NUMA nodes keep 7 free CPUs, but node 0 has only 1GiB of memory left.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 1, Memory: 15 * gib, Affinity: "0", NUMAMemory: map[int]uint64{0: 15 * gib}}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 1, Memory: gib, Affinity: "4", NUMAMemory: map[int]uint64{1: gib}}))

	grant := reserve(t, z, static, zonecapacity.VM{ID: 3, CPUs: 4, Memory: 8 * gib})
	require.True(t, grant.CPUSet.IsSubsetOf(node1CPUs), "placed on NUMA node 1: %v", grant.CPUSet)
}

func TestZonePinnedPacksFullestNUMANode(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Node 1 is half used, node 0 is empty.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: 4 * gib, Affinity: "4,5,12,13", NUMAMemory: map[int]uint64{1: 4 * gib}}))

	grant := reserve(t, z, static, zonecapacity.VM{ID: 2, CPUs: 2, Memory: 2 * gib})
	require.True(t, grant.CPUSet.IsSubsetOf(node1CPUs), "placed on NUMA node 1: %v", grant.CPUSet)
}

func TestZonePinnedPacksByScarcerResource(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Both nodes keep 7 free CPUs; node 0 keeps 15GiB free and node 1 only 5GiB.
	trackNUMAMemoryUse(t, z, gib, 11*gib)

	// Node 1 is left with 1/16 of its memory, node 0 with 5/8 of its CPUs: node 1 is the tighter fit.
	grant := reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 2, Memory: 4 * gib})
	require.True(t, grant.CPUSet.IsSubsetOf(node1CPUs), "placed on NUMA node 1: %v", grant.CPUSet)
}

func TestZonePinnedSpreadsSmallVMForMemory(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Each NUMA node has 6GiB free, so a 10GiB VM fits only with one CPU on each.
	trackNUMAMemoryUse(t, z, 10*gib, 10*gib)

	grant := reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 2, Memory: 10 * gib})
	require.Equal(t, map[int]zonecapacity.NUMABinding{
		0: {VCPUs: vcpus(0, 0), Memory: 5 * gib},
		1: {VCPUs: vcpus(1, 1), Memory: 5 * gib},
	}, grant.NUMA)
}

func TestZonePinnedRebalancesCPUsToMemory(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	// Node 0 has 4GiB free and node 1 has 12GiB, both with 7 free CPUs.
	trackNUMAMemoryUse(t, z, 12*gib, 4*gib)

	// Packing puts 6 CPUs and 10.5GiB on node 0; node 0 can back only 2 of 8 CPUs.
	grant := reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 8, Memory: 14 * gib})
	require.Equal(t, 6, grant.CPUSet.Intersection(node1CPUs).Size(), "6 CPUs on NUMA node 1: %v", grant.CPUSet)
	require.Equal(t, map[int]zonecapacity.NUMABinding{
		0: {VCPUs: vcpus(0, 1), Memory: 3584 << 20},
		1: {VCPUs: vcpus(2, 7), Memory: 10752 << 20},
	}, grant.NUMA)
}

func TestZonePinnedRejectsWhenNoDistributionFits(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	trackNUMAMemoryUse(t, z, 10*gib, 10*gib)

	// 6GiB + 6GiB free cannot back 13GiB however the CPUs are spread.
	_, err := z.Reserve(static, zonecapacity.VM{ID: 1, CPUs: 2, Memory: 13 * gib})
	require.ErrorContains(t, err, "not enough memory")
	require.Equal(t, zonecapacity.Capacity{CPUs: 14, Memory: 11 * gib}, z.Available(static))
}
