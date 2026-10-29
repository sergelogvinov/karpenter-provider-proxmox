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

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

const gib = uint64(1 << 30)

var (
	shared = zonecapacity.Policy{Kind: zonecapacity.PolicySimple}
	static = zonecapacity.Policy{Kind: zonecapacity.PolicyStatic}
)

// overcommitAt is the CPU overcommit policy at ratio.
func overcommitAt(ratio float64) zonecapacity.Policy {
	return zonecapacity.Policy{Kind: zonecapacity.PolicyOvercommit, CPUOvercommitRatio: ratio}
}

// sharedHost is 16 CPUs over two NUMA nodes of 16GiB, CPUs 0-7 on node 0, 1GiB reserved.
var sharedHost = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-7", MemSize: 16 * gib},
		1: {CPUs: "8-15", MemSize: 16 * gib},
	},
	ReservedMemory: gib,
}

// pinnedHost is one socket, 8 cores with 2 threads, NUMA node 0 on cores 0-3 and node 1 on cores 4-7.
var pinnedHost = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-3,8-11", MemSize: 16 * gib},
		1: {CPUs: "4-7,12-15", MemSize: 16 * gib},
	},
	ReservedMemory: gib,
}

func newZone(t *testing.T, ns settings.NodeSettings, reservedCPUs ...int) *zonecapacity.Zone {
	t.Helper()

	z, err := zonecapacity.New(zonecapacity.Host{
		Topology:       lo.Must(topology.DiscoverFromSettings(&ns)),
		ReservedCPUs:   cpuset.New(reservedCPUs...),
		ReservedMemory: ns.ReservedMemory,
		HugePageSize:   ns.HugePageSize,
	})
	require.NoError(t, err)

	return z
}

func TestZoneReserveShared(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared))

	grant, err := z.Reserve(shared, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)
	require.Equal(t, zonecapacity.Grant{}, grant, "a shared VM gets no CPUs, NUMA binding or tokens")
	require.Equal(t, zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib}, z.Available(shared))

	_, err = z.Reserve(shared, zonecapacity.VM{ID: 2, CPUs: 13, Memory: gib})
	require.ErrorContains(t, err, "not enough CPUs")
	_, err = z.Reserve(shared, zonecapacity.VM{ID: 3, CPUs: 1, Memory: 24 * gib})
	require.ErrorContains(t, err, "not enough memory")
	require.Equal(t, zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib}, z.Available(shared))
}

func TestZoneRelease(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	_, err := z.Reserve(shared, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)

	_, err = z.Reserve(shared, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib})
	require.ErrorIs(t, err, zonecapacity.ErrExists)

	z.Release(1)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared))

	z.Release(1)
	z.Release(42)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared))
}

func TestZoneTrack(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	require.ErrorIs(t, z.Track(zonecapacity.VM{CPUs: 2, Memory: gib}), zonecapacity.ErrInvalid)

	// Over budget: tracked anyway, Available clamps at zero.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 20, Memory: 40 * gib}))
	require.Equal(t, zonecapacity.Capacity{}, z.Available(shared))

	// Tracking again replaces the record instead of adding to it.
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib}))
	require.Equal(t, zonecapacity.Capacity{CPUs: 12, Memory: 23 * gib}, z.Available(shared))
}

func TestZoneTrackOverlappingPinnedVMs(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost, 0)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: gib, Affinity: "0-3"}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 4, Memory: gib, Affinity: "2-5"}))
	// 16 CPUs - reserved 0 - pinned 1-5.
	require.Equal(t, zonecapacity.Capacity{CPUs: 10, Memory: 29 * gib}, z.Available(shared))

	z.Release(2)
	// CPUs 2 and 3 stay pinned by VM 1.
	require.Equal(t, zonecapacity.Capacity{CPUs: 12, Memory: 30 * gib}, z.Available(shared))
}

func TestZoneSharedOvercommit(t *testing.T) {
	t.Parallel()

	overcommit := overcommitAt(1.5)

	z := newZone(t, sharedHost, 0, 1)
	// 14 unreserved CPUs x 1.5.
	require.Equal(t, 21, z.Available(overcommit).CPUs)

	_, err := z.Reserve(overcommit, zonecapacity.VM{ID: 1, CPUs: 20, Memory: gib})
	require.NoError(t, err)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, Affinity: "2,3"}))
	// Pinning 2 CPUs shrinks the shared pool to floor(12 x 1.5) = 18 < 20.
	require.Equal(t, 0, z.Available(overcommit).CPUs)

	_, err = z.Reserve(overcommit, zonecapacity.VM{ID: 3, CPUs: 1, Memory: gib})
	require.ErrorContains(t, err, "not enough CPUs")
}
