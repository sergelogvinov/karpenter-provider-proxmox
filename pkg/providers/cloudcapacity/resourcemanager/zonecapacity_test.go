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
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"
)

const gib = uint64(1 << 30)

var (
	sharedPolicy = zonecapacity.Policy{Kind: zonecapacity.PolicySimple}
	staticPolicy = zonecapacity.Policy{Kind: zonecapacity.PolicyStatic}
)

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

// newTestZone builds a zone of 16 CPUs over two NUMA nodes of 16GiB, 1GiB reserved.
func newTestZone(t *testing.T, policy zonecapacity.Policy) *ZoneCapacity {
	t.Helper()

	zone, err := NewZoneCapacity(zonecapacity.Host{
		Topology:       lo.Must(topology.DiscoverFromSettings(&pinnedTestSettings)),
		ReservedMemory: pinnedTestSettings.ReservedMemory,
	}, policy)
	require.NoError(t, err)

	return zone
}

func TestZoneCapacityReserveFillsRequest(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, sharedPolicy)

	op := &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}, op, "shared placement gets no host CPUs, NUMA split or tokens")

	zone = newTestZone(t, staticPolicy)

	op = &resources.VMResources{ID: 1, CPUs: 10, Memory: 10 * gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, map[int]resources.NUMANodeState{
		0: {CPUs: "0-7", Memory: 8192, Policy: "bind"},
		1: {CPUs: "8-9", Memory: 2048, Policy: "bind"},
	}, op.NUMANodes)
	require.Equal(t, []string{"affinity=" + op.CPUSet.String()}, op.DescriptionTokens)

	op = &resources.VMResources{ID: 2, CPUs: 1, Memory: gib}
	require.NoError(t, zone.Reserve(op))
	require.Equal(t, "0-0", op.NUMANodes[1].CPUs, "a single vCPU keeps the range form")
}

func TestVMFromConfig(t *testing.T) {
	t.Parallel()

	tags := qemu.Tags{"karpenter"}

	vm, err := VMFromConfig(&cluster.Resource{VMID: 7, MaxCPU: 4, MaxMem: int64(8 * gib)}, &qemu.Config{
		Name:        "worker-7",
		Affinity:    "0-3",
		Description: "Karpenter managed instance, affinity=0-3",
		Tags:        &tags,
		NUMA: map[int]qemu.NUMA{
			0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"1"}, Memory: new(8192)},
		},
	})
	require.NoError(t, err)
	require.Equal(t, zonecapacity.VM{
		ID: 7, Name: "worker-7", CPUs: 4, Memory: 8 * gib,
		Affinity:    "0-3",
		Description: "Karpenter managed instance, affinity=0-3",
		Tags:        []string{"karpenter"},
		NUMAMemory:  map[int]uint64{1: 8 * gib},
	}, vm)
}

func TestVMFromConfigLeavesAffinityToTheZone(t *testing.T) {
	t.Parallel()

	vm, err := VMFromConfig(&cluster.Resource{VMID: 7, MaxCPU: 2, MaxMem: int64(gib)}, &qemu.Config{Affinity: "0-x"})
	require.NoError(t, err, "a field copy does not parse the affinity")
	require.Equal(t, "0-x", vm.Affinity)

	zone := newTestZone(t, sharedPolicy)
	require.ErrorIs(t, zone.Track(vm), zonecapacity.ErrInvalid)
}

func noVMs(int) bool { return false }

func TestZoneCapacityKeepPending(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	reserve := func(t *testing.T, z *ZoneCapacity) {
		t.Helper()
		require.NoError(t, z.Reserve(&resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}))
	}

	for _, tc := range []struct {
		name   string
		prev   func(*testing.T, *ZoneCapacity)
		next   func(*testing.T, *ZoneCapacity)
		after  time.Duration
		exists func(int) bool
		want   zonecapacity.Capacity
	}{
		{
			name: "pending reservation is kept",
			prev: reserve,
			want: zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib},
		},
		{
			name: "VM seen running is counted once",
			prev: reserve,
			next: func(t *testing.T, z *ZoneCapacity) {
				require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib}))
			},
			want: zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib},
		},
		{
			name: "released reservation is gone",
			prev: func(t *testing.T, z *ZoneCapacity) { reserve(t, z); z.Release(1) },
			want: zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name: "tracked VM no longer running is not kept",
			prev: func(t *testing.T, z *ZoneCapacity) {
				require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 4, Memory: 8 * gib}))
			},
			want: zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name:  "reservation past the grace period without a VM expires",
			prev:  reserve,
			after: 11 * time.Minute,
			want:  zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib},
		},
		{
			name:   "reservation past the grace period with a stopped VM is kept",
			prev:   reserve,
			after:  11 * time.Minute,
			exists: func(id int) bool { return id == 1 },
			want:   zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			now := start
			clock := func() time.Time { return now }

			prev := newTestZone(t, sharedPolicy)
			prev.now = clock
			tc.prev(t, prev)

			now = now.Add(tc.after)

			next := newTestZone(t, sharedPolicy)
			next.now = clock
			if tc.next != nil {
				tc.next(t, next)
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

func TestZoneCapacityHasPending(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, sharedPolicy)
	require.False(t, zone.HasPending())

	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 1, Memory: gib}))
	require.True(t, zone.HasPending())

	require.NoError(t, zone.Track(zonecapacity.VM{ID: 1, CPUs: 1, Memory: gib}))
	require.False(t, zone.HasPending(), "a VM seen running is no longer pending")
}

// TestZoneCapacityTrackIgnoredToken checks that a VM whose cpushare token is ignored for its affinity
// is still tracked: the warning does not keep its reservation pending.
func TestZoneCapacityTrackIgnoredToken(t *testing.T) {
	t.Parallel()

	zone := newTestZone(t, staticPolicy)
	require.NoError(t, zone.Reserve(&resources.VMResources{ID: 1, CPUs: 2, Memory: gib}))

	err := zone.Track(zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, Affinity: "0-1", Description: "cpushare=2"})
	require.ErrorIs(t, err, zonecapacity.ErrTokenIgnored)
	require.False(t, zone.HasPending(), "the VM is tracked")
	require.Equal(t, 14, zone.Available().CPUs)
}

func TestZoneCapacityKeepPendingPinned(t *testing.T) {
	t.Parallel()

	prev := newTestZone(t, staticPolicy)
	op := &resources.VMResources{ID: 1, CPUs: 4, Memory: 8 * gib}
	require.NoError(t, prev.Reserve(op))

	next := newTestZone(t, staticPolicy)
	next.KeepPending(prev, noVMs)

	other := &resources.VMResources{ID: 2, CPUs: 12, Memory: gib}
	require.NoError(t, next.Reserve(other))
	require.True(t, other.CPUSet.Intersection(op.CPUSet).IsEmpty(), "kept pinned CPUs %v are not handed out again: %v", op.CPUSet, other.CPUSet)
}
