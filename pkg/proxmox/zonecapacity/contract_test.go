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
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/settings"
)

func TestZoneFitShared(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)

	fit, err := z.Fit(shared, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)
	// 12 of 16 CPUs and 23 of 31GiB stay free; memory is scarcer.
	require.Equal(t, zonecapacity.Fit{NUMANodes: 0, Leftover: 23.0 / 31}, fit)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(shared), "fit reserves nothing")

	_, err = z.Fit(shared, zonecapacity.VM{CPUs: 17, Memory: gib})
	require.ErrorContains(t, err, "not enough CPUs")
}

func TestZoneFitPinned(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)

	fit, err := z.Fit(static, zonecapacity.VM{CPUs: 4, Memory: 8 * gib})
	require.NoError(t, err)
	require.Equal(t, zonecapacity.Fit{NUMANodes: 1, Leftover: 23.0 / 31}, fit)

	// 10 CPUs cannot stay on one 8-CPU node.
	fit, err = z.Fit(static, zonecapacity.VM{ID: 1, CPUs: 10, Memory: 10 * gib})
	require.NoError(t, err)
	require.Equal(t, zonecapacity.Fit{NUMANodes: 2, Leftover: 6.0 / 16}, fit)
	require.Equal(t, zonecapacity.Capacity{CPUs: 16, Memory: 31 * gib}, z.Available(static), "fit reserves nothing")

	reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
	_, err = z.Fit(static, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib})
	require.ErrorIs(t, err, zonecapacity.ErrExists)
}

// TestZoneCount checks that Count is how many VMs like vm Reserve then admits one after another:
// n Reserves succeed, the next fails for lack of room.
func TestZoneCount(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		zone   func(t *testing.T) *zonecapacity.Zone
		policy zonecapacity.Policy
		vm     zonecapacity.VM
		want   int
	}{
		// 16 CPUs over 4 CPUs a VM.
		"shared": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, sharedHost) }, policy: shared, vm: zonecapacity.VM{CPUs: 4, Memory: 2 * gib}, want: 4},
		// 31GiB over 7GiB a VM.
		"shared memory": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, sharedHost) }, policy: shared, vm: zonecapacity.VM{CPUs: 1, Memory: 7 * gib}, want: 4},
		// Two VMs of 3 CPUs fit on each 8-CPU NUMA node, and a fifth spans the 2 CPUs left on each.
		"pinned": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, pinnedHost) }, policy: static, vm: zonecapacity.VM{CPUs: 3, Memory: gib}, want: 5},
		// Each group of 2 seats holds two 2-vCPU VMs on 2 host CPUs: 16 CPUs hold 16 VMs.
		"CPU share": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, pinnedHost) }, policy: static, vm: zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: 2}, want: 16},
		// Node 0 has 4 quarter slots of 6 CPUs.
		"NUMA slot": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, numaExampleHost, 0, 32) }, policy: static, vm: slotVM(0, numaExampleSlots[4].Shape), want: 4},
		// Each 16GiB hugepage pool takes three 5GiB VMs, though the zone has 32GiB.
		"hugepage": {zone: func(t *testing.T) *zonecapacity.Zone { return newZone(t, hugePageHost) }, policy: static, vm: zonecapacity.VM{CPUs: 2, Memory: 5 * gib}, want: 6},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			z := tc.zone(t)
			require.Equal(t, tc.want, z.Count(tc.policy, tc.vm, 32))

			for id := 1; id <= tc.want; id++ {
				vm := tc.vm
				vm.ID = 1000 + id
				reserve(t, z, tc.policy, vm)
			}

			vm := tc.vm
			vm.ID = 2000

			_, err := z.Reserve(tc.policy, vm)
			require.ErrorIs(t, err, zonecapacity.ErrNoRoom)
			require.Zero(t, z.Count(tc.policy, tc.vm, 32))
		})
	}
}

// hugePageHost is pinnedHost with each NUMA node's 16GiB as its hugepage pool.
var hugePageHost = func() settings.NodeSettings {
	host := pinnedHost
	host.HugePageSize = gib

	return host
}()

func TestZoneCountLeavesZoneUnchanged(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})

	vm := zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib}
	before := z.Available(static)
	wantFit, err := z.Fit(static, vm)
	require.NoError(t, err)

	require.Equal(t, 6, z.Count(static, vm, 32))
	require.Equal(t, before, z.Available(static), "count reserves nothing")

	gotFit, err := z.Fit(static, vm)
	require.NoError(t, err)
	require.Equal(t, wantFit, gotFit)
	require.Equal(t, 6, z.Count(static, vm, 32), "count gives the same answer twice")
}

func TestZoneCountLimit(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	vm := zonecapacity.VM{CPUs: 1, Memory: gib}

	require.Equal(t, 16, z.Count(shared, vm, 32))
	require.Equal(t, 3, z.Count(shared, vm, 3))
	require.Zero(t, z.Count(shared, vm, 0))
}

// TestZoneCountNotAdmitted checks that a VM Reserve would never admit counts zero, and that the VM's
// own ID does not matter, even one that already has a record.
func TestZoneCountNotAdmitted(t *testing.T) {
	t.Parallel()

	z := newZone(t, pinnedHost)
	reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})

	require.Zero(t, z.Count(static, zonecapacity.VM{CPUs: 1}, 32), "no memory")
	require.Zero(t, z.Count(overcommitAt(0), zonecapacity.VM{CPUs: 2, Memory: gib, CPUShare: 2}, 32), "CPU share under CPU overcommit")
	require.Equal(t, 6, z.Count(static, zonecapacity.VM{ID: 1, CPUs: 2, Memory: gib}, 32))
}

// TestZoneErrorContract checks that Fit fails exactly when Reserve would, with the same error kind.
func TestZoneErrorContract(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		policy zonecapacity.Policy
		vm     zonecapacity.VM
		want   error
	}{
		"too many CPUs":        {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 13, Memory: gib}, want: zonecapacity.ErrNoRoom},
		"too much memory":      {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 1, Memory: 24 * gib}, want: zonecapacity.ErrNoRoom},
		"too much NUMA memory": {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 1, Memory: 17 * gib}, want: zonecapacity.ErrNoRoom},
		"too many shared CPUs": {policy: shared, vm: zonecapacity.VM{ID: 2, CPUs: 13, Memory: gib}, want: zonecapacity.ErrNoRoom},
		"already reserved":     {policy: static, vm: zonecapacity.VM{ID: 1, CPUs: 1, Memory: gib}, want: zonecapacity.ErrExists},
		"no CPUs":              {policy: static, vm: zonecapacity.VM{ID: 2, Memory: gib}, want: zonecapacity.ErrInvalid},
		"no memory":            {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 1}, want: zonecapacity.ErrInvalid},
		"ratio below one":      {policy: overcommitAt(0.5), vm: zonecapacity.VM{ID: 2, CPUs: 1, Memory: gib}, want: zonecapacity.ErrInvalid},
		"ratio not a number":   {policy: overcommitAt(math.NaN()), vm: zonecapacity.VM{ID: 2, CPUs: 1, Memory: gib}, want: zonecapacity.ErrInvalid},
		"ratio infinite":       {policy: overcommitAt(math.Inf(1)), vm: zonecapacity.VM{ID: 2, CPUs: 1, Memory: gib}, want: zonecapacity.ErrInvalid},
		"CPU share overcommit": {policy: overcommitAt(0), vm: zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, CPUShare: 2}, want: zonecapacity.ErrNotOffered},
		"CPU share bad seats":  {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib, CPUShare: 5}, want: zonecapacity.ErrInvalid},
		"too many share CPUs":  {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 14, Memory: gib, CPUShare: 2}, want: zonecapacity.ErrNoRoom},
		"fits":                 {policy: static, vm: zonecapacity.VM{ID: 2, CPUs: 2, Memory: gib}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			z := newZone(t, pinnedHost)
			reserve(t, z, static, zonecapacity.VM{ID: 1, CPUs: 4, Memory: 8 * gib})
			before := z.Available(static)

			_, fitErr := z.Fit(tc.policy, tc.vm)
			require.Equal(t, before, z.Available(static), "fit reserves nothing")

			_, reserveErr := z.Reserve(tc.policy, tc.vm)

			if tc.want == nil {
				require.NoError(t, fitErr)
				require.NoError(t, reserveErr)

				return
			}

			require.ErrorIs(t, fitErr, tc.want)
			require.ErrorIs(t, reserveErr, tc.want)
			require.Equal(t, before, z.Available(static), "a failed reserve holds nothing")
		})
	}
}

func TestZoneAvailableRejectsHugeRatio(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)
	require.Equal(t, zonecapacity.Capacity{}, z.Available(overcommitAt(math.MaxFloat64)))
}

func TestZoneReserveRequiresID(t *testing.T) {
	t.Parallel()

	z := newZone(t, sharedHost)

	_, err := z.Fit(shared, zonecapacity.VM{CPUs: 1, Memory: gib})
	require.NoError(t, err, "fit takes a VM that has no ID yet")

	_, err = z.Reserve(shared, zonecapacity.VM{CPUs: 1, Memory: gib})
	require.ErrorIs(t, err, zonecapacity.ErrInvalid)
}

// vmFromGrant is the VM a launch writes from a Grant, as the zone later reads it back:
// the description tokens and the NUMA memory bound to each host node.
func vmFromGrant(vm zonecapacity.VM, grant zonecapacity.Grant) zonecapacity.VM {
	vm.CPUShare = 0
	vm.Description = strings.Join(append([]string{"Karpenter managed instance"}, grant.DescriptionTokens...), ", ")

	if len(grant.NUMA) > 0 {
		vm.NUMAMemory = map[int]uint64{}
		for node, b := range grant.NUMA {
			vm.NUMAMemory[node] = b.Memory
		}
	}

	return vm
}

// TestZoneRoundTrip checks that a VM rebuilt from its Reserve grant and tracked on a fresh zone
// gives the same available capacity, and leaves the same room for the next VM.
func TestZoneRoundTrip(t *testing.T) {
	t.Parallel()

	for _, p := range []zonecapacity.Policy{static, shared, overcommitAt(1.5)} {
		rng := rand.New(rand.NewPCG(269, 1))

		reserved := newZone(t, pinnedHost, 0)
		tracked := newZone(t, pinnedHost, 0)

		for id := 1; id <= 40; id++ {
			vm := zonecapacity.VM{ID: id, CPUs: 1 + rng.IntN(6), Memory: uint64(1+rng.IntN(8)) * gib / 2}
			if seats := rng.IntN(5); p.Kind != zonecapacity.PolicyOvercommit && seats >= 2 {
				vm.CPUShare = seats
				vm.CPUs = 2 * (1 + rng.IntN(2))
			}

			grant, err := reserved.Reserve(p, vm)
			if err != nil {
				require.ErrorIs(t, err, zonecapacity.ErrNoRoom)

				continue
			}

			require.NoError(t, tracked.Track(vmFromGrant(vm, grant)))
			require.Equal(t, reserved.Available(p), tracked.Available(p), "policy %+v, VM %d", p, id)

			next := zonecapacity.VM{ID: 1000 + id, CPUs: 1 + rng.IntN(4), Memory: gib}
			wantFit, wantErr := reserved.Fit(p, next)
			gotFit, gotErr := tracked.Fit(p, next)
			require.Equal(t, wantFit, gotFit, "policy %+v, VM %d", p, id)
			require.Equal(t, errors.Is(wantErr, zonecapacity.ErrNoRoom), errors.Is(gotErr, zonecapacity.ErrNoRoom))
		}
	}
}
