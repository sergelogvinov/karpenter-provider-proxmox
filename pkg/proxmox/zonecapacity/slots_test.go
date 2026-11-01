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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/settings"
)

// numaExampleHost is the host of docs/scheduler-numa.md §9: 2 sockets with one NUMA node each,
// 16 cores / 32 threads and 128GiB per node, 4GiB reserved. CPU 32 is the SMT sibling of CPU 0.
var numaExampleHost = settings.NodeSettings{
	NumSockets:      2,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-15,32-47", MemSize: 128 * gib},
		1: {CPUs: "16-31,48-63", MemSize: 128 * gib},
	},
	ReservedMemory: 4 * gib,
}

// numaExampleSlots are the slots of docs/scheduler-numa.md §9 with CPUs 0 and 32 reserved, all free.
var numaExampleSlots = []zonecapacity.SlotOffer{
	{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 30, Memory: 126 * gib}, Free: 1},
	{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 32, Memory: 126 * gib}, Free: 1},
	{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 14, Memory: 63 * gib}, Free: 2},
	{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 16, Memory: 63 * gib}, Free: 2},
	{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 6, Memory: 31 * gib}, Free: 4},
	{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 8, Memory: 31 * gib}, Free: 4},
}

// TestZoneSlotsWorkedExample is docs/scheduler-numa.md §9: six slot shapes, node 0 smaller by the reserved core.
func TestZoneSlotsWorkedExample(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	require.Equal(t, numaExampleSlots, z.Slots(static))
}

// TestZoneSlotsSharedPlacement checks that shared placement offers no NUMA slot.
func TestZoneSlotsSharedPlacement(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	require.Empty(t, z.Slots(shared))
	require.Empty(t, z.Slots(overcommitAt(2)))
}

// freeOf returns the free slots of each shape, by fraction and CPUs.
func freeOf(offers []zonecapacity.SlotOffer) map[[2]int]int {
	free := map[[2]int]int{}
	for _, o := range offers {
		free[[2]int{o.Shape.Fraction, o.Shape.CPUs}] = o.Free
	}

	return free
}

// TestZoneSlotsPinnedCPU checks that a pinned CPU takes every slot it touches, and only those.
func TestZoneSlotsPinnedCPU(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	// CPU 16 is the first core of NUMA node 1.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 1, Memory: gib, Affinity: "16"}))

	require.Equal(t, map[[2]int]int{
		{1, 30}: 1, {1, 32}: 0,
		{2, 14}: 2, {2, 16}: 1,
		{4, 6}: 4, {4, 8}: 3,
	}, freeOf(z.Slots(static)))
}

// TestZoneSlotsMemoryPoorNode is docs/scheduler-numa.md §10: free slot CPUs on a node without the memory.
func TestZoneSlotsMemoryPoorNode(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	// A VM pinned to node 0 with 70GiB bound to node 1 leaves node 1 56GiB: one quarter's worth,
	// and no half or whole node. Node 0 loses its whole slot and the slots around CPU 1.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 1, Memory: 70 * gib, Affinity: "1", NUMAMemory: map[int]uint64{1: 70 * gib}}))

	require.Equal(t, map[[2]int]int{
		{1, 30}: 0, {1, 32}: 0,
		{2, 14}: 1, {2, 16}: 0,
		{4, 6}: 3, {4, 8}: 1,
	}, freeOf(z.Slots(static)))
}

// TestZoneSlotsCPUShareGroupsFillZone is docs/scheduler-numa.md §10: slot CPUs are unpinned and the memory
// is there, but the CPU share groups leave no room to move them off the slot.
func TestZoneSlotsCPUShareGroupsFillZone(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	// Node 0 is pinned whole, so only node 1's 32 CPUs are unpinned.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 30, Memory: gib, Affinity: "1-15,33-47"}))
	require.Equal(t, 1, freeOf(z.Slots(static))[[2]int{1, 32}])

	// A group of 4 seats and 8 vCPUs takes 8 of them: 24 CPUs of room, one half or three quarters.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 8, Memory: gib, Description: "cpushare=4"}))

	require.Equal(t, map[[2]int]int{
		{1, 30}: 0, {1, 32}: 0,
		{2, 14}: 0, {2, 16}: 1,
		{4, 6}: 0, {4, 8}: 3,
	}, freeOf(z.Slots(static)))
}

// TestZoneSlotsHugePagePool checks that a zone with a hugepage pool sizes slots on the pool, with no host reserve.
func TestZoneSlotsHugePagePool(t *testing.T) {
	t.Parallel()

	host := numaExampleHost
	host.HugePageSize = 2 << 20
	host.NUMANodes = settings.NUMANodes{
		0: {CPUs: "0-15,32-47", MemSize: 100 * gib},
		1: {CPUs: "16-31,48-63", MemSize: 100 * gib},
	}

	z := newZone(t, host, 0, 32)

	require.Equal(t, []zonecapacity.SlotOffer{
		{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 30, Memory: 100 * gib}, Free: 1},
		{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 32, Memory: 100 * gib}, Free: 1},
		{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 14, Memory: 50 * gib}, Free: 2},
		{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 16, Memory: 50 * gib}, Free: 2},
		{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 6, Memory: 25 * gib}, Free: 4},
		{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 8, Memory: 25 * gib}, Free: 4},
	}, z.Slots(static))
}

// TestZoneSlotsSnapToUncoreCache checks that slot edges snap to an uncore cache boundary when the size
// allows: 7 usable cores, cores 0-3 on one cache and 4-6 on the other, give halves 0-2 and 4-6, so core 3
// lies outside both.
func TestZoneSlotsSnapToUncoreCache(t *testing.T) {
	t.Parallel()

	z := newZone(t, settings.NodeSettings{
		NumSockets:      1,
		NumThreads:      1,
		NumUncoreCaches: 2,
		NUMANodes:       settings.NUMANodes{0: {CPUs: "0-7", MemSize: 9 * gib}},
	}, 7)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 1, Memory: gib, Affinity: "3"}))

	require.Equal(t, 2, freeOf(z.Slots(static))[[2]int{2, 3}])
}

// TestZoneSlotsSmallNode checks that a fraction that would round down to no core is not generated,
// and that a host without SMT carves single-thread cores.
func TestZoneSlotsSmallNode(t *testing.T) {
	t.Parallel()

	z := newZone(t, settings.NodeSettings{
		NumSockets: 1,
		NumThreads: 1,
		NUMANodes:  settings.NUMANodes{0: {CPUs: "0-3", MemSize: 9 * gib}},
	}, 0)

	// 3 usable cores: a whole of 3, halves of 1, no quarter.
	require.Equal(t, []zonecapacity.SlotOffer{
		{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 3, Memory: 9 * gib}, Free: 1},
		{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 1, Memory: 4 * gib}, Free: 2},
	}, z.Slots(static))
}

// slotVM asks for a strict NUMA slot of shape.
func slotVM(id int, shape zonecapacity.SlotShape) zonecapacity.VM {
	return zonecapacity.VM{ID: id, CPUs: shape.CPUs, Memory: shape.Memory, Slot: &shape}
}

// TestZoneReserveSlotWholeNode checks that a whole slot pins the VM 1:1 to every usable CPU of its
// NUMA node, with one guest NUMA node bound to it holding all the slot's memory.
func TestZoneReserveSlotWholeNode(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)

	grant, err := z.Reserve(static, slotVM(1, numaExampleSlots[0].Shape))
	require.NoError(t, err)

	require.Equal(t, mustParse(t, "1-15,33-47"), grant.CPUSet)
	require.Equal(t, map[int]zonecapacity.NUMABinding{
		0: {VCPUs: mustParse(t, "0-29"), Memory: 126 * gib},
	}, grant.NUMA)
	require.Equal(t, []string{"affinity=1-15,33-47"}, grant.DescriptionTokens)
	require.Zero(t, grant.HugePageSize, "a zone without a hugepage pool launches without hugepages")
}

// TestZoneReserveSlotHalvesFillNode checks that halves land in aligned slots, lowest first, that two halves
// fill their node, and that a quarter goes into a free half.
func TestZoneReserveSlotHalvesFillNode(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	half := numaExampleSlots[3].Shape // 16 CPUs on node 1

	first, err := z.Reserve(static, slotVM(1, half))
	require.NoError(t, err)
	require.Equal(t, mustParse(t, "16-23,48-55"), first.CPUSet)
	require.Equal(t, map[int]zonecapacity.NUMABinding{1: {VCPUs: mustParse(t, "0-15"), Memory: 63 * gib}}, first.NUMA)

	quarter, err := z.Reserve(static, slotVM(2, numaExampleSlots[5].Shape)) // 8 CPUs on node 1
	require.NoError(t, err)
	require.Equal(t, mustParse(t, "24-27,56-59"), quarter.CPUSet, "the first quarter of the free half")
	z.Release(2)

	second, err := z.Reserve(static, slotVM(3, half))
	require.NoError(t, err)
	require.Equal(t, mustParse(t, "24-31,56-63"), second.CPUSet)

	require.Equal(t, map[[2]int]int{
		{1, 30}: 1, {1, 32}: 0,
		{2, 14}: 2, {2, 16}: 0,
		{4, 6}: 4, {4, 8}: 0,
	}, freeOf(z.Slots(static)), "two halves fill node 1")
}

// TestZoneReserveSlotLastSlotTaken checks that a slot request with no free slot left fails for lack
// of room, and never falls back to CPUs outside a slot.
func TestZoneReserveSlotLastSlotTaken(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	whole := numaExampleSlots[1].Shape // all of node 1

	_, err := z.Reserve(static, slotVM(1, whole))
	require.NoError(t, err)

	_, err = z.Fit(static, slotVM(0, whole))
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom)

	_, err = z.Reserve(static, slotVM(2, whole))
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom, "node 0 has no slot of this shape")

	// Node 0 has 30 free CPUs and the memory for 16 of them, but no free half of 16 CPUs.
	_, err = z.Reserve(static, slotVM(3, numaExampleSlots[3].Shape))
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom, "a slot VM never lands outside its slot")
}

// TestZoneReserveSlotCPUShareGroupsFillZone checks that a slot whose CPUs are unpinned is not reserved
// while the CPU share groups would have no room left to move off it.
func TestZoneReserveSlotCPUShareGroupsFillZone(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 30, Memory: gib, Affinity: "1-15,33-47"}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 8, Memory: gib, Description: "cpushare=4"}))

	_, err := z.Reserve(static, slotVM(3, numaExampleSlots[1].Shape))
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom)

	_, err = z.Reserve(static, slotVM(3, numaExampleSlots[3].Shape))
	require.NoError(t, err, "a half still leaves the group room")
}

// TestZoneReserveSlotHugePagePool checks that a zone with a hugepage pool launches slot VMs with hugepages
// of the pool's page size, and other pinned VMs without.
func TestZoneReserveSlotHugePagePool(t *testing.T) {
	t.Parallel()

	host := numaExampleHost
	host.HugePageSize = 2 << 20

	z := newZone(t, host, 0, 32)

	grant, err := z.Reserve(static, slotVM(1, zonecapacity.SlotShape{Fraction: 1, CPUs: 32, Memory: 128 * gib}))
	require.NoError(t, err)
	require.Equal(t, uint64(2<<20), grant.HugePageSize)

	grant, err = z.Reserve(static, zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib})
	require.NoError(t, err)
	require.Zero(t, grant.HugePageSize)
}

// TestZoneReserveSlotShapeDrifted checks that a slot request of a shape the zone does not carve, such as
// a NUMA type listed before the zone's topology or host reserve changed, is a lack of room, not invalid.
func TestZoneReserveSlotShapeDrifted(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)

	for _, shape := range []zonecapacity.SlotShape{
		{Fraction: 1, CPUs: 32, Memory: 120 * gib},
		{Fraction: 2, CPUs: 15, Memory: 63 * gib},
		{Fraction: 4, CPUs: 32, Memory: 126 * gib},
	} {
		_, err := z.Fit(static, slotVM(1, shape))
		require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
		require.NotErrorIs(t, err, zonecapacity.ErrInvalid)

		_, err = z.Reserve(static, slotVM(1, shape))
		require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
		require.NotErrorIs(t, err, zonecapacity.ErrInvalid)
	}
}

// TestZoneReserveSlotRejected checks the slot requests a zone never takes.
func TestZoneReserveSlotRejected(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	shape := numaExampleSlots[1].Shape

	_, err := z.Reserve(shared, slotVM(1, shape))
	require.ErrorIs(t, err, zonecapacity.ErrNotOffered)

	vm := slotVM(1, shape)
	vm.CPUs = 8
	_, err = z.Reserve(static, vm)
	require.ErrorIs(t, err, zonecapacity.ErrInvalid, "the VM must be the size of its slot")

	vm = slotVM(1, shape)
	vm.CPUShare = 2
	_, err = z.Reserve(static, vm)
	require.ErrorIs(t, err, zonecapacity.ErrInvalid)
}

// TestZoneSlotVMTracksAsPinned is the round trip: a VM launched from a slot grant, tracked by a fresh
// zone, is a pinned VM covering its slot.
func TestZoneSlotVMTracksAsPinned(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	vm := slotVM(1, numaExampleSlots[3].Shape)

	grant, err := z.Reserve(static, vm)
	require.NoError(t, err)

	synced := newZone(t, numaExampleHost, 0, 32)
	require.NoError(t, synced.Track(zonecapacity.VM{
		ID: vm.ID, CPUs: vm.CPUs, Memory: vm.Memory,
		Description: grant.DescriptionTokens[0], NUMAMemory: grant.NUMAMemory(),
	}))

	require.Equal(t, z.Slots(static), synced.Slots(static))
	require.Equal(t, z.Available(static), synced.Available(static))
	require.Equal(t, 1, freeOf(synced.Slots(static))[[2]int{2, 16}])
}
