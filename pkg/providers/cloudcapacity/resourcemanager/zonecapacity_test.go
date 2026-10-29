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

package resourcemanager

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

const gib = uint64(1 << 30)

// newTestZone builds a zone on testNodeSettings: 16 CPUs over two NUMA nodes of 16GiB, 1GiB reserved.
func newTestZone(t *testing.T, placement Placement, reservedCPUs ...int) *ZoneCapacity {
	t.Helper()

	topo := lo.Must(topology.DiscoverFromSettings(&testNodeSettings))
	zone, err := NewZoneCapacity(topo, reservedCPUs, testNodeSettings.ReservedMemory, placement)
	require.NoError(t, err)

	return zone
}

func TestZoneCapacityReserveShared(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1))
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available())

	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
	require.Equal(t, Capacity{CPUs: 12, Memory: 23 * gib}, zone.Available())

	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 2, CPUs: 13, Memory: gib}), "not enough CPUs")
	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 3, CPUs: 1, Memory: 24 * gib}), "not enough memory")
	require.Equal(t, Capacity{CPUs: 12, Memory: 23 * gib}, zone.Available())
}

func TestZoneCapacityRelease(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1))
	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 2, Memory: gib}), "already")

	zone.Release(1)
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available())

	zone.Release(1)
	zone.Release(42)
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available())
}

func TestZoneCapacityTrack(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1))
	require.ErrorContains(t, zone.Track(&resources.VMResources{CPUs: 2, Memory: gib}), "VM ID")

	// Over budget: tracked anyway, Available clamps at zero.
	require.NoError(t, zone.Track(&resources.VMResources{ID: 1, CPUs: 20, Memory: 40 * gib}))
	require.Equal(t, Capacity{}, zone.Available())

	// Tracking again replaces the record instead of adding to it.
	require.NoError(t, zone.Track(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
	require.Equal(t, Capacity{CPUs: 12, Memory: 23 * gib}, zone.Available())
}

func TestZoneCapacityTrackOverlappingPinnedVMs(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1), 0)
	require.NoError(t, zone.Track(&resources.VMResources{ID: 1, CPUs: 4, Memory: gib, CPUSet: cpuset.New(0, 1, 2, 3)}))
	require.NoError(t, zone.Track(&resources.VMResources{ID: 2, CPUs: 4, Memory: gib, CPUSet: cpuset.New(2, 3, 4, 5)}))
	// 16 CPUs - reserved 0 - pinned 1-5.
	require.Equal(t, Capacity{CPUs: 10, Memory: 29 * gib}, zone.Available())

	zone.Release(2)
	// CPUs 2 and 3 stay pinned by VM 1.
	require.Equal(t, Capacity{CPUs: 12, Memory: 30 * gib}, zone.Available())
}

func TestZoneCapacitySharedOvercommit(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1.5), 0, 1)
	// 14 unreserved CPUs x 1.5.
	require.Equal(t, 21, zone.Available().CPUs)

	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 20, Memory: gib}))
	require.NoError(t, zone.Track(&resources.VMResources{ID: 2, CPUs: 2, Memory: gib, CPUSet: cpuset.New(2, 3)}))
	// Pinning 2 CPUs shrinks the shared pool to floor(12 x 1.5) = 18 < 20.
	require.Equal(t, 0, zone.Available().CPUs)
	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 3, CPUs: 1, Memory: gib}), "not enough CPUs")
}

func TestZoneCapacityRejectsInvalidRatio(t *testing.T) {
	t.Parallel()

	topo := lo.Must(topology.DiscoverFromSettings(&testNodeSettings))
	for _, ratio := range []float64{0, 0.5, math.NaN(), math.Inf(1)} {
		_, err := NewZoneCapacity(topo, nil, gib, Shared(ratio))
		require.ErrorContains(t, err, "overcommit ratio", "ratio %v", ratio)
	}
}

// pinnedTestSettings is one socket, 8 cores with 2 threads, NUMA node 0 on cores 0-3 and node 1 on cores 4-7.
var pinnedTestSettings = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-3,8-11", MemSize: 16 * gib},
		1: {CPUs: "4-7,12-15", MemSize: 16 * gib},
	},
	ReservedMemory: gib,
}

func newPinnedTestZone(t *testing.T) *ZoneCapacity {
	t.Helper()

	topo := lo.Must(topology.DiscoverFromSettings(&pinnedTestSettings))
	zone, err := NewZoneCapacity(topo, nil, pinnedTestSettings.ReservedMemory, Pinned(logr.Discard()))
	require.NoError(t, err)

	return zone
}

func TestZoneCapacityReservePinned(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)

	op := &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, 4, op.CPUSet.Size())
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(0, 1, 2, 3, 8, 9, 10, 11)), "placed on NUMA node 0: %v", op.CPUSet)
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-3", Memory: 8192, Policy: "bind"},
	}, op.NUMANodes)
	require.Equal(t, Capacity{CPUs: 12, Memory: 23 * gib}, zone.Available())

	zone.Release(1)
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available())
}

func TestZoneCapacityReservePinnedAcrossNUMANodes(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)

	op := &resources.VMResources{ID: 1, CPUs: 10, Memory: 10 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-7", Memory: 8192, Policy: "bind"},
		1: {CPUs: "8-9", Memory: 2048, Policy: "bind"},
	}, op.NUMANodes)

	// Node 1 has 14GiB left, so a 6-CPU VM with 14GiB fits there and nowhere else.
	op = &resources.VMResources{ID: 2, CPUs: 6, Memory: 14 * gib}
	require.NoError(t, zone.Reserve(op))
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)), "placed on NUMA node 1: %v", op.CPUSet)
	require.Equal(t, Capacity{CPUs: 0, Memory: 7 * gib}, zone.Available())
}

func TestZoneCapacityPinnedRespectsZoneMemory(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// A shared VM without NUMA binding takes 20GiB of the zone's 31GiB.
	require.NoError(t, zone.Track(&resources.VMResources{ID: 1, CPUs: 2, Memory: 20 * gib}))

	// Each NUMA node still has 16GiB, but only 11GiB is left in the zone.
	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 2, CPUs: 2, Memory: 12 * gib}), "not enough")
	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 3, CPUs: 2, Memory: 11 * gib}))
}

func TestZoneCapacityOvercommittedNUMANodeDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 1, CPUs: 2, Memory: 20 * gib, CPUSet: cpuset.New(0, 8),
		NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0-1", Memory: 20 * 1024}},
	}))

	op := &resources.VMResources{ID: 2, CPUs: 2, Memory: 4 * gib}
	require.NoError(t, zone.Reserve(op))
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)), "placed on NUMA node 1: %v", op.CPUSet)
}

func TestZoneCapacityPinnedKeepsRoomForSharedVMs(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	require.NoError(t, zone.Track(&resources.VMResources{ID: 1, CPUs: 14, Memory: gib}))

	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 2, CPUs: 4, Memory: gib}), "not enough CPUs")
	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 3, CPUs: 2, Memory: gib}))
	require.Equal(t, 0, zone.Available().CPUs)
}

func TestZoneCapacityPinnedSpansNUMANodesWithoutWholeFit(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Each NUMA node keeps 7 free CPUs and 8.5GiB of free memory.
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 1, CPUs: 1, Memory: 7680 << 20, CPUSet: cpuset.New(3),
		NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0", Memory: 7680}},
	}))
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 2, CPUs: 1, Memory: 7680 << 20, CPUSet: cpuset.New(7),
		NUMANodes: map[int]resources.NUMANodeState{1: {CPUs: "0", Memory: 7680}},
	}))

	// No single node holds 10GiB. Packing takes the 3 whole free cores of node 0 and 2 of node 1,
	// so the 10 CPUs split 6+4 and need only 6GiB and 4GiB.
	op := &resources.VMResources{ID: 3, CPUs: 10, Memory: 10 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-5", Memory: 6144, Policy: "bind"},
		1: {CPUs: "6-9", Memory: 4096, Policy: "bind"},
	}, op.NUMANodes)
}

func TestZoneCapacityPinnedPrefersNodeHoldingWholeVM(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Both NUMA nodes keep 7 free CPUs, but node 0 has only 1GiB of memory left.
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 1, CPUs: 1, Memory: 15 * gib, CPUSet: cpuset.New(0),
		NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0", Memory: 15 * 1024}},
	}))
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 2, CPUs: 1, Memory: gib, CPUSet: cpuset.New(4),
		NUMANodes: map[int]resources.NUMANodeState{1: {CPUs: "0", Memory: 1024}},
	}))

	op := &resources.VMResources{ID: 3, CPUs: 4, Memory: 8 * gib}
	require.NoError(t, zone.Reserve(op))
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)), "placed on NUMA node 1: %v", op.CPUSet)
}

func TestZoneCapacityPinnedPacksFullestNUMANode(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Node 1 is half used, node 0 is empty.
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 1, CPUs: 4, Memory: 4 * gib, CPUSet: cpuset.New(4, 5, 12, 13),
		NUMANodes: map[int]resources.NUMANodeState{1: {CPUs: "0-3", Memory: 4 * 1024}},
	}))

	op := &resources.VMResources{ID: 2, CPUs: 2, Memory: 2 * gib}
	require.NoError(t, zone.Reserve(op))
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)), "placed on NUMA node 1: %v", op.CPUSet)
}

func TestZoneCapacityPinnedPacksByScarcerResource(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Both nodes keep 7 free CPUs; node 0 keeps 15GiB free and node 1 only 5GiB.
	trackNUMAMemoryUse(t, zone, 1024, 11*1024)

	// Node 1 is left with 1/16 of its memory, node 0 with 5/8 of its CPUs: node 1 is the tighter fit.
	op := &resources.VMResources{ID: 1, CPUs: 2, Memory: 4 * gib}
	require.NoError(t, zone.Reserve(op))
	require.True(t, op.CPUSet.IsSubsetOf(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)), "placed on NUMA node 1: %v", op.CPUSet)
}

func TestZoneCapacityFitShared(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, Shared(1))

	fit, err := zone.Fit(resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)
	// 12 of 16 CPUs and 23 of 31GiB stay free; memory is scarcer.
	require.Equal(t, Fit{NUMANodes: 0, Leftover: 23.0 / 31}, fit)
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available(), "fit reserves nothing")

	_, err = zone.Fit(resources.VMResources{ID: 2, CPUs: 17, Memory: gib})
	require.ErrorContains(t, err, "not enough CPUs")
}

func TestZoneCapacityFitPinned(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)

	fit, err := zone.Fit(resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)
	require.Equal(t, Fit{NUMANodes: 1, Leftover: 23.0 / 31}, fit)

	// 10 CPUs cannot stay on one 8-CPU node.
	fit, err = zone.Fit(resources.VMResources{ID: 1, CPUs: 10, Memory: 10 * gib})
	require.NoError(t, err)
	require.Equal(t, Fit{NUMANodes: 2, Leftover: 6.0 / 16}, fit)
	require.Equal(t, Capacity{CPUs: 16, Memory: 31 * gib}, zone.Available(), "fit reserves nothing")

	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
	_, err = zone.Fit(resources.VMResources{ID: 1, CPUs: 2, Memory: gib})
	require.ErrorContains(t, err, "already")
}

func TestZoneCapacityMarksLackOfRoom(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		op     resources.VMResources
		noRoom bool
	}{
		"too many CPUs":        {op: resources.VMResources{ID: 2, CPUs: 13, Memory: gib}, noRoom: true},
		"too much memory":      {op: resources.VMResources{ID: 2, CPUs: 1, Memory: 24 * gib}, noRoom: true},
		"too much NUMA memory": {op: resources.VMResources{ID: 2, CPUs: 1, Memory: 17 * gib}, noRoom: true},
		"already reserved":     {op: resources.VMResources{ID: 1, CPUs: 1, Memory: gib}},
		"no CPUs":              {op: resources.VMResources{ID: 2, Memory: gib}},
		"no memory":            {op: resources.VMResources{ID: 2, CPUs: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			zone := newPinnedTestZone(t)
			require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))

			_, err := zone.Fit(tc.op)
			require.Error(t, err)
			require.Equal(t, tc.noRoom, errors.Is(err, ErrNoRoom), "fit: %v", err)

			op := tc.op
			err = zone.Reserve(&op)
			require.Error(t, err)
			require.Equal(t, tc.noRoom, errors.Is(err, ErrNoRoom), "reserve: %v", err)
		})
	}
}

// trackNUMAMemoryUse pins one CPU on each NUMA node with the given memory bound to it.
func trackNUMAMemoryUse(t *testing.T, zone *ZoneCapacity, node0MiB, node1MiB uint64) {
	t.Helper()

	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 101, CPUs: 1, Memory: node0MiB << 20, CPUSet: cpuset.New(3),
		NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0", Memory: node0MiB}},
	}))
	require.NoError(t, zone.Track(&resources.VMResources{
		ID: 102, CPUs: 1, Memory: node1MiB << 20, CPUSet: cpuset.New(7),
		NUMANodes: map[int]resources.NUMANodeState{1: {CPUs: "0", Memory: node1MiB}},
	}))
}

func TestZoneCapacityPinnedSpreadsSmallVMForMemory(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Each NUMA node has 6GiB free, so a 10GiB VM fits only with one CPU on each.
	trackNUMAMemoryUse(t, zone, 10*1024, 10*1024)

	op := &resources.VMResources{ID: 1, CPUs: 2, Memory: 10 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-0", Memory: 5120, Policy: "bind"},
		1: {CPUs: "1-1", Memory: 5120, Policy: "bind"},
	}, op.NUMANodes)
}

func TestZoneCapacityPinnedRebalancesCPUsToMemory(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	// Node 0 has 4GiB free and node 1 has 12GiB, both with 7 free CPUs.
	trackNUMAMemoryUse(t, zone, 12*1024, 4*1024)

	// Packing puts 6 CPUs and 10.5GiB on node 0; node 0 can back only 2 of 8 CPUs.
	op := &resources.VMResources{ID: 1, CPUs: 8, Memory: 14 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, 6, op.CPUSet.Intersection(cpuset.New(4, 5, 6, 7, 12, 13, 14, 15)).Size(), "6 CPUs on NUMA node 1: %v", op.CPUSet)
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-1", Memory: 3584, Policy: "bind"},
		1: {CPUs: "2-7", Memory: 10752, Policy: "bind"},
	}, op.NUMANodes)
}

func TestZoneCapacityPinnedRejectsWhenNoDistributionFits(t *testing.T) {
	t.Parallel()

	zone := newPinnedTestZone(t)
	trackNUMAMemoryUse(t, zone, 10*1024, 10*1024)

	// 6GiB + 6GiB free cannot back 13GiB however the CPUs are spread.
	require.ErrorContains(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 2, Memory: 13 * gib}), "not enough memory")
	require.Equal(t, Capacity{CPUs: 14, Memory: 11 * gib}, zone.Available())
}

func noVMs(int) bool { return false }

func TestZoneCapacityKeepPending(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	reserve := &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}

	for _, tc := range []struct {
		name   string
		prev   func(*ZoneCapacity)
		next   func(*ZoneCapacity)
		after  time.Duration
		exists func(int) bool
		want   Capacity
	}{
		{
			name: "pending reservation is kept",
			prev: func(z *ZoneCapacity) { require.NoError(t, z.Reserve(reserve)) },
			want: Capacity{CPUs: 12, Memory: 23 * gib},
		},
		{
			name: "VM seen running is counted once",
			prev: func(z *ZoneCapacity) { require.NoError(t, z.Reserve(reserve)) },
			next: func(z *ZoneCapacity) {
				require.NoError(t, z.Track(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
			},
			want: Capacity{CPUs: 12, Memory: 23 * gib},
		},
		{
			name: "released reservation is gone",
			prev: func(z *ZoneCapacity) { require.NoError(t, z.Reserve(reserve)); z.Release(1) },
			want: Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name: "tracked VM no longer running is not kept",
			prev: func(z *ZoneCapacity) {
				require.NoError(t, z.Track(&resources.VMResources{ID: 2, CPUs: 4, Memory: 8 * gib}))
			},
			want: Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name:  "reservation past the grace period without a VM expires",
			prev:  func(z *ZoneCapacity) { require.NoError(t, z.Reserve(reserve)) },
			after: 11 * time.Minute,
			want:  Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name:   "reservation past the grace period with a stopped VM is kept",
			prev:   func(z *ZoneCapacity) { require.NoError(t, z.Reserve(reserve)) },
			after:  11 * time.Minute,
			exists: func(id int) bool { return id == 1 },
			want:   Capacity{CPUs: 12, Memory: 23 * gib},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			now := start
			clock := func() time.Time { return now }

			prev := newTestZone(t, Shared(1))
			prev.now = clock
			tc.prev(prev)

			now = now.Add(tc.after)

			next := newTestZone(t, Shared(1))
			next.now = clock
			if tc.next != nil {
				tc.next(next)
			}

			exists := tc.exists
			if exists == nil {
				exists = noVMs
			}

			next.KeepPending(prev, exists)
			require.Equal(t, tc.want, next.Available())
		})
	}
}

func TestZoneCapacityKeepPendingPinned(t *testing.T) {
	t.Parallel()

	prev := newPinnedTestZone(t)
	op := &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}
	require.NoError(t, prev.Reserve(op))

	next := newPinnedTestZone(t)
	next.KeepPending(prev, noVMs)

	other := &resources.VMResources{ID: 2, CPUs: 12, Memory: gib}
	require.NoError(t, next.Reserve(other))
	require.True(t, other.CPUSet.Intersection(op.CPUSet).IsEmpty(), "kept pinned CPUs %v are not handed out again: %v", op.CPUSet, other.CPUSet)
}
