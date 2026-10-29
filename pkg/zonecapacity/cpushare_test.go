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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
)

// workedExampleHost is the 64-CPU, 4-NUMA host of docs/scheduler.md: node 0 is CPUs 0-7 with
// their siblings 32-39, and so on, 124GiB per node.
var workedExampleHost = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 4,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-7,32-39", MemSize: 124 * gib},
		1: {CPUs: "8-15,40-47", MemSize: 124 * gib},
		2: {CPUs: "16-23,48-55", MemSize: 124 * gib},
		3: {CPUs: "24-31,56-63", MemSize: 124 * gib},
	},
}

// trackWorkedExample tracks the VMs of docs/scheduler-cpushare.md §9 on a zone with CPUs 0 and 32 reserved.
func trackWorkedExample(t *testing.T) *zonecapacity.Zone {
	t.Helper()

	z := newZone(t, workedExampleHost, 0, 32)

	for _, vm := range []zonecapacity.VM{
		{ID: 100, CPUs: 8, Memory: gib, Affinity: "8-15"},
		{ID: 301, CPUs: 2, Memory: gib, Description: "Karpenter managed instance, class=default, cpushare=2"},
		{ID: 302, CPUs: 2, Memory: gib, Description: "Karpenter managed instance, cpushare=2, class=default"},
		{ID: 303, CPUs: 2, Memory: gib, Description: "cpushare=2"},
		{ID: 401, CPUs: 8, Memory: gib, Description: "created by hand, cpushare=4"},
		{ID: 200, CPUs: 4, Memory: gib},
	} {
		require.NoError(t, z.Track(vm), "VM %d", vm.ID)
	}

	return z
}

// TestZoneCPUShareWorkedExample is docs/scheduler-cpushare.md §9: class (2, 2) has 3 VMs in 2 groups
// and class (4, 8) one VM in 1 group, so the groups take 4 + 8 = 12 host CPUs.
func TestZoneCPUShareWorkedExample(t *testing.T) {
	t.Parallel()

	free := 496*gib - 6*gib

	// 64 CPUs - 2 reserved - 8 pinned - 12 for the groups = 42 CPUs, 4 of them used by VM 200.
	z := trackWorkedExample(t)
	require.Equal(t, zonecapacity.Capacity{CPUs: 38, Memory: free}, z.Available(shared))
	require.Equal(t, zonecapacity.Capacity{CPUs: 38, Memory: free}, z.Available(static))

	// VM 302 stops: class (2, 2) needs one group less.
	z.Release(302)
	require.Equal(t, 40, z.Available(shared).CPUs)
}

// TestZoneCPUShareStaticRoom checks that the groups shrink the room left for pinned VMs, as a count.
func TestZoneCPUShareStaticRoom(t *testing.T) {
	t.Parallel()

	z := trackWorkedExample(t)
	z.Release(200)

	// 64 - 2 reserved - 8 pinned - 12 for the groups.
	_, err := z.Fit(static, zonecapacity.VM{CPUs: 43, Memory: gib})
	require.ErrorIs(t, err, zonecapacity.ErrNoRoom)

	_, err = z.Reserve(static, zonecapacity.VM{ID: 500, CPUs: 42, Memory: gib})
	require.NoError(t, err)
}

// TestZoneCPUShareReserveFillsOpenSeat checks that a CPU share VM is launched with its token and no CPUs,
// and that a second VM of the same class takes the open seat at no extra CPU cost.
func TestZoneCPUShareReserveFillsOpenSeat(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]zonecapacity.Policy{"static": static, "simple": shared} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			z := newZone(t, sharedHost)

			grant, err := z.Reserve(p, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, CPUShare: 2})
			require.NoError(t, err)
			require.Equal(t, zonecapacity.Grant{DescriptionTokens: []string{"cpushare=2"}}, grant, "no CPUs and no NUMA binding")
			require.Equal(t, zonecapacity.Capacity{CPUs: 14, Memory: 30 * gib}, z.Available(p))

			fit, err := z.Fit(p, zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: 2})
			require.NoError(t, err)
			require.Equal(t, 0, fit.NUMANodes)

			_, err = z.Reserve(p, zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, CPUShare: 2})
			require.NoError(t, err)
			require.Equal(t, zonecapacity.Capacity{CPUs: 14, Memory: 29 * gib}, z.Available(p), "the open seat costs no CPU")

			// Other classes never share a group: 2 CPUs for (3, 2) and 4 for (2, 4).
			_, err = z.Reserve(p, zonecapacity.VM{ID: 3, CPUs: 2, Memory: gib, CPUShare: 3})
			require.NoError(t, err)
			_, err = z.Reserve(p, zonecapacity.VM{ID: 4, CPUs: 4, Memory: gib, CPUShare: 2})
			require.NoError(t, err)
			require.Equal(t, 8, z.Available(p).CPUs)
		})
	}
}

// TestZoneCPUShareGroupsNeedRoom checks that a new group needs its whole width, and an open seat none.
func TestZoneCPUShareGroupsNeedRoom(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]zonecapacity.Policy{"static": static, "simple": shared} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			z := newZone(t, sharedHost)
			require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 12, Memory: gib, Affinity: "0-11"}))
			require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 4, Memory: gib, Description: "cpushare=4"}))
			require.Equal(t, 0, z.Available(p).CPUs)

			_, err := z.Reserve(p, zonecapacity.VM{ID: 3, CPUs: 4, Memory: gib, CPUShare: 4})
			require.NoError(t, err, "an open seat always fits on CPU")

			_, err = z.Fit(p, zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: 4})
			require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
			_, err = z.Reserve(p, zonecapacity.VM{ID: 4, CPUs: 1, Memory: gib})
			require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
		})
	}
}

// TestZoneCPUShareNotOfferedUnderOvercommit checks that CPU overcommit offers no CPU share VM, and counts
// one created by hand as a shared VM.
func TestZoneCPUShareNotOfferedUnderOvercommit(t *testing.T) {
	t.Parallel()

	overcommit := zonecapacity.Policy{Overcommit: true, CPUOvercommitRatio: 1}

	z := newZone(t, sharedHost)

	_, err := z.Fit(overcommit, zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: 2})
	require.ErrorIs(t, err, zonecapacity.ErrNotOffered)
	_, err = z.Reserve(overcommit, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, CPUShare: 2})
	require.ErrorIs(t, err, zonecapacity.ErrNotOffered)

	require.NoError(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, Description: "cpushare=2"}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 3, CPUs: 2, Memory: gib, Description: "cpushare=2"}))
	require.Equal(t, 12, z.Available(overcommit).CPUs, "each VM holds its vCPUs")
	require.Equal(t, 14, z.Available(shared).CPUs, "the same VMs share one group")
}

func TestZoneCPUShareTrack(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		vm      zonecapacity.VM
		wantErr error
		want    int
	}{
		{name: "token after other text", vm: zonecapacity.VM{ID: 1, CPUs: 4, Memory: gib, Description: "Karpenter managed instance, class=default, cpushare=3"}, want: 12},
		{name: "token next to a multi-range affinity token", vm: zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, Description: "affinity=1,9, cpushare=2"}, want: 14},
		{name: "token with no affinity", vm: zonecapacity.VM{ID: 1, CPUs: 8, Memory: gib, Description: "class=default, cpushare=4"}, want: 8},
		{name: "an affinity option makes it pinned", vm: zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, Affinity: "0-1", Description: "cpushare=4"}, want: 14},
		{name: "a constrained affinity wins too", vm: zonecapacity.VM{ID: 1, CPUs: 4, Memory: gib, Affinity: "0", Description: "cpushare=2"}, want: 15},
		{name: "malformed token counts as shared", vm: zonecapacity.VM{ID: 1, CPUs: 4, Memory: gib, Description: "cpushare=two"}, wantErr: zonecapacity.ErrInvalid, want: 12},
		{name: "unsupported seats count as shared", vm: zonecapacity.VM{ID: 1, CPUs: 4, Memory: gib, Description: "cpushare=1"}, wantErr: zonecapacity.ErrInvalid, want: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			z := newZone(t, sharedHost)

			err := z.Track(tc.vm)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.want, z.Available(shared).CPUs)
		})
	}
}

func TestZoneCPUShareRejectsBadSeats(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)

	for _, seats := range []int{-1, 1, 5} {
		_, err := z.Fit(shared, zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: seats})
		require.ErrorIs(t, err, zonecapacity.ErrInvalid, "seats %d", seats)
		_, err = z.Reserve(shared, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, CPUShare: seats})
		require.ErrorIs(t, err, zonecapacity.ErrInvalid, "seats %d", seats)
	}
}
