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
	"math"
	"testing"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

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
