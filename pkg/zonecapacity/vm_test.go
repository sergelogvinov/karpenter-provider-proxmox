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
)

func TestZoneTrackClassifiesVMs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		vm   zonecapacity.VM
		want zonecapacity.Capacity
	}{
		{
			name: "shared VM uses shared vCPUs",
			vm:   zonecapacity.VM{ID: 1, CPUs: 4, Memory: 2 * gib},
			want: zonecapacity.Capacity{CPUs: 12, Memory: 29 * gib},
		},
		{
			name: "pinned VM by the affinity option",
			vm:   zonecapacity.VM{ID: 1, CPUs: 4, Memory: 2 * gib, Affinity: "0-3"},
			want: zonecapacity.Capacity{CPUs: 12, Memory: 29 * gib},
		},
		{
			name: "pinned VM by the affinity token in its description",
			vm:   zonecapacity.VM{ID: 1, CPUs: 2, Memory: 2 * gib, Description: "Karpenter managed instance, class=default, affinity=0-5"},
			want: zonecapacity.Capacity{CPUs: 10, Memory: 29 * gib},
		},
		{
			name: "pinned VM by a token of several CPU ranges",
			vm:   zonecapacity.VM{ID: 1, CPUs: 4, Memory: 2 * gib, Description: "Karpenter managed instance, affinity=1-2,9-10, class=default"},
			want: zonecapacity.Capacity{CPUs: 12, Memory: 29 * gib},
		},
		{
			name: "the affinity option wins over the token",
			vm:   zonecapacity.VM{ID: 1, CPUs: 2, Memory: 2 * gib, Affinity: "0,1", Description: "affinity=0-7"},
			want: zonecapacity.Capacity{CPUs: 14, Memory: 29 * gib},
		},
		{
			name: "constrained VM holds its affinity, not its vCPUs",
			vm:   zonecapacity.VM{ID: 1, CPUs: 8, Memory: 2 * gib, Affinity: "0-1"},
			want: zonecapacity.Capacity{CPUs: 14, Memory: 29 * gib},
		},
		{
			name: "node-capacity guest is never counted",
			vm:   zonecapacity.VM{ID: 1, CPUs: 16, Memory: 32 * gib, Affinity: "0-15", Tags: []string{"karpenter"}},
			want: zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			z := newZone(t, sharedHost)
			require.NoError(t, z.Track(tc.vm))
			require.Equal(t, tc.want, z.Available(shared))
		})
	}
}

func TestZoneTrackNodeCapacityGuestReplacesRecord(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: 2 * gib}))
	require.NoError(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 4, Memory: 2 * gib, Tags: []string{"pool", "karpenter"}}))
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared))
}

func TestZoneTrackRejectsUnparseableAffinity(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	require.ErrorIs(t, z.Track(zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib, Affinity: "0-x"}), zonecapacity.ErrInvalid)
	require.ErrorIs(t, z.Track(zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, Description: "affinity=junk"}), zonecapacity.ErrInvalid)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared))
}

func TestZoneHugePagePoolTakesNoHostReserve(t *testing.T) {
	t.Parallel()

	host := sharedHost
	host.HugePageSize = gib

	z := newZone(t, host)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 32 * gib}, z.Available(shared))
}
