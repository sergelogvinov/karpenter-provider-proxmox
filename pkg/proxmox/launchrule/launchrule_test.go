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

package launchrule_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/launchrule"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	giB = uint64(1) << 30
	miB = uint64(1) << 20
)

var (
	// The zones of the launch matrix: without a hugepage pool, with a 1GiB pool on one NUMA node (host node 1),
	// and with a 1GiB pool on two NUMA nodes.
	noPool  = zonecapacity.Pool{NUMANodes: 1}
	onePool = zonecapacity.Pool{PageSize: giB, NUMANodes: 1, HostNode: 1}
	twoPool = zonecapacity.Pool{PageSize: giB, NUMANodes: 2}

	// The templates of the launch matrix, by their hugepages.
	templates = map[string]launchrule.Template{
		"none":    launchrule.KnownTemplate(""),
		"1024":    launchrule.KnownTemplate("1024"),
		"2":       launchrule.KnownTemplate("2"),
		"any":     launchrule.KnownTemplate("any"),
		"unknown": launchrule.UnknownTemplate("VM templates disagree on hugepages"),
	}

	policies = []zonecapacity.PolicyKind{zonecapacity.PolicyStatic, zonecapacity.PolicySimple, zonecapacity.PolicyOvercommit}

	// numaTypes and nonNUMATypes are the launch profiles of the launch matrix, with and without a hugepage request.
	numaTypes = []launchrule.Profile{
		launchrule.NUMAType(1), launchrule.NUMAType(2).WithHugepageRequest(2 * miB),
	}
	nonNUMATypes = []launchrule.Profile{
		launchrule.CPUShareType(2), launchrule.CPUShareType(4).WithHugepageRequest(giB),
		launchrule.DedicatedType(), launchrule.DedicatedType().WithHugepageRequest(2 * miB),
	}
)

// want is the launch decision a row of the matrix expects. A dropped row expects no VM options of its own:
// what a launch writes is checked only where the type is offered, and in the root zone that drops one.
type want struct {
	offered   bool
	reason    string
	split     bool
	memory    launchrule.MemorySource
	hugepages launchrule.HugepagesAction
	value     string
	guestNUMA launchrule.GuestNUMA
	hostNode  int
}

var (
	// offeredOnSystem launches with nothing changed: system memory, the template's hugepages, guest NUMA
	// from the grant.
	offeredOnSystem = want{offered: true, memory: launchrule.SystemMemory}
	// offeredInPool launches from the template's hugepages, drawing from the pool, guest NUMA from the grant.
	offeredInPool = want{offered: true, memory: launchrule.HugepagePool}
)

func dropped(reason string, split bool) want {
	return want{reason: reason, split: split}
}

func TestDecideWithoutRoot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		zone     string
		pool     zonecapacity.Pool
		template string
		numa     want
		nonNUMA  want
	}{
		// A zone without a hugepage pool launches from a template without hugepages, or one not known, as it is.
		{zone: "no pool", pool: noPool, template: "none", numa: offeredOnSystem, nonNUMA: offeredOnSystem},
		{zone: "no pool", pool: noPool, template: "unknown", numa: offeredOnSystem, nonNUMA: offeredOnSystem},
		// #321: a hugepage template has no pool to draw from, and its clones fail to start.
		{
			zone: "no pool", pool: noPool, template: "1024",
			numa: dropped(launchrule.ReasonHugepagesNoPool, false), nonNUMA: dropped(launchrule.ReasonHugepagesNoPool, false),
		},
		{
			zone: "no pool", pool: noPool, template: "2",
			numa: dropped(launchrule.ReasonHugepagesNoPool, false), nonNUMA: dropped(launchrule.ReasonHugepagesNoPool, false),
		},
		{
			zone: "no pool", pool: noPool, template: "any",
			numa: dropped(launchrule.ReasonHugepagesNoPool, false), nonNUMA: dropped(launchrule.ReasonHugepagesNoPool, false),
		},

		// A pool on one NUMA node is the whole zone budget: a template of its page size launches every type
		// from it, non-NUMA types with one guest NUMA node bound to the zone's host node.
		{
			zone: "pool on one NUMA node", pool: onePool, template: "none",
			numa: dropped(launchrule.ReasonHugepagesUnset, true), nonNUMA: offeredOnSystem,
		},
		{
			zone: "pool on one NUMA node", pool: onePool, template: "1024",
			numa:    offeredInPool,
			nonNUMA: want{offered: true, memory: launchrule.HugepagePool, guestNUMA: launchrule.GuestNUMAOneNode, hostNode: 1},
		},
		{
			zone: "pool on one NUMA node", pool: onePool, template: "2",
			numa: dropped(launchrule.ReasonHugepagesMismatch, false), nonNUMA: dropped(launchrule.ReasonHugepagesMismatch, false),
		},
		{
			zone: "pool on one NUMA node", pool: onePool, template: "any",
			numa: dropped(launchrule.ReasonHugepagesMismatch, false), nonNUMA: dropped(launchrule.ReasonHugepagesMismatch, false),
		},
		{
			zone: "pool on one NUMA node", pool: onePool, template: "unknown",
			numa: dropped(launchrule.ReasonStatusUnknown, false), nonNUMA: dropped(launchrule.ReasonStatusUnknown, false),
		},

		// A pool on two NUMA nodes splits node classes between NUMA and non-NUMA types.
		{
			zone: "pool on two NUMA nodes", pool: twoPool, template: "none",
			numa: dropped(launchrule.ReasonHugepagesUnset, true), nonNUMA: offeredOnSystem,
		},
		{
			zone: "pool on two NUMA nodes", pool: twoPool, template: "1024",
			numa: offeredInPool, nonNUMA: dropped(launchrule.ReasonHugepagesSet, true),
		},
		{
			zone: "pool on two NUMA nodes", pool: twoPool, template: "2",
			numa: dropped(launchrule.ReasonHugepagesMismatch, false), nonNUMA: dropped(launchrule.ReasonHugepagesSet, true),
		},
		{
			zone: "pool on two NUMA nodes", pool: twoPool, template: "any",
			numa: dropped(launchrule.ReasonHugepagesMismatch, false), nonNUMA: dropped(launchrule.ReasonHugepagesSet, true),
		},
		{
			zone: "pool on two NUMA nodes", pool: twoPool, template: "unknown",
			numa: dropped(launchrule.ReasonStatusUnknown, false), nonNUMA: dropped(launchrule.ReasonStatusUnknown, false),
		},
	} {
		t.Run(tc.zone+", template hugepages "+tc.template, func(t *testing.T) {
			t.Parallel()

			requireMatrixRow(t, launchrule.Zone{Pool: tc.pool}, templates[tc.template], tc.numa, tc.nonNUMA)
		})
	}
}

func TestDecideAsRoot(t *testing.T) {
	t.Parallel()

	setPool := func(guestNUMA launchrule.GuestNUMA, hostNode int) want {
		return want{
			offered: true, memory: launchrule.HugepagePool, hugepages: launchrule.SetHugepages, value: "1024",
			guestNUMA: guestNUMA, hostNode: hostNode,
		}
	}

	for _, tc := range []struct {
		zone    string
		pool    zonecapacity.Pool
		numa    want
		nonNUMA want
	}{
		// As root the controller writes hugepages itself, whatever the template has: it clears them where
		// there is no pool to draw from (#321).
		{
			zone: "no pool", pool: noPool,
			numa:    want{offered: true, memory: launchrule.SystemMemory, hugepages: launchrule.DeleteHugepages},
			nonNUMA: want{offered: true, memory: launchrule.SystemMemory, hugepages: launchrule.DeleteHugepages},
		},
		{
			zone: "pool on one NUMA node", pool: onePool,
			numa: setPool(launchrule.GuestNUMAFromGrant, 0), nonNUMA: setPool(launchrule.GuestNUMAOneNode, 1),
		},
		// The root rule: no non-NUMA type in a pool of two NUMA nodes. A clone that still lands there has its
		// inherited hugepages cleared, as Proxmox refuses hugepages without numa.
		{
			zone: "pool on two NUMA nodes", pool: twoPool,
			numa:    setPool(launchrule.GuestNUMAFromGrant, 0),
			nonNUMA: want{reason: launchrule.ReasonPoolSpansNUMANodes, memory: launchrule.SystemMemory, hugepages: launchrule.DeleteHugepages},
		},
	} {
		for name, template := range templates {
			t.Run(tc.zone+", template hugepages "+name, func(t *testing.T) {
				t.Parallel()

				requireMatrixRow(t, launchrule.Zone{Pool: tc.pool, Root: true}, template, tc.numa, tc.nonNUMA)
			})
		}
	}
}

// requireMatrixRow checks that every NUMA type gets numa and every non-NUMA type nonNUMA in zone from template,
// under each node policy.
func requireMatrixRow(t *testing.T, zone launchrule.Zone, template launchrule.Template, numa, nonNUMA want) {
	t.Helper()

	check := func(profile launchrule.Profile, w want) {
		for _, policy := range policies {
			zone.Policy = policy
			d := launchrule.Decide(profile, template, zone)
			msg := fmt.Sprintf("%+v under policy %d", profile, policy)

			require.Equal(t, w.offered, d.Offered, msg)
			require.Equal(t, d.Drop, launchrule.Decide(profile.Types().Profile(), template, zone).Drop,
				"%s: offered as the profile of its types", msg)

			if !w.offered {
				require.Equal(t, w.reason, d.Drop.Reason, msg)
				require.Equal(t, w.split, d.Drop.Split, msg)
				require.NotEmpty(t, d.Drop.Message, msg)

				if !zone.Root {
					continue
				}
			} else {
				require.Zero(t, d.Drop, msg)
			}

			require.Equal(t, w.memory, d.Memory, msg)
			require.Equal(t, w.hugepages, d.Hugepages, msg)
			require.Equal(t, w.value, d.HugepagesValue, msg)
			require.Equal(t, w.guestNUMA, d.GuestNUMA, msg)
			require.Equal(t, w.hostNode, d.HostNode, msg)
		}
	}

	for _, p := range numaTypes {
		check(p, numa)
	}

	for _, p := range nonNUMATypes {
		check(p, nonNUMA)
	}
}

// TestDecideTwoMiBPool checks that a template matches a pool of 2MiB pages by "2", and not by "1024".
func TestDecideTwoMiBPool(t *testing.T) {
	t.Parallel()

	pool := zonecapacity.Pool{PageSize: 2 * miB, NUMANodes: 1}

	require.True(t, launchrule.Decide(launchrule.NUMAType(1), launchrule.KnownTemplate("2"), launchrule.Zone{Pool: pool}).Offered)
	require.True(t, launchrule.Decide(launchrule.DedicatedType(), launchrule.KnownTemplate("2"), launchrule.Zone{Pool: pool}).Offered)

	d := launchrule.Decide(launchrule.NUMAType(1), launchrule.KnownTemplate("1024"), launchrule.Zone{Pool: pool})
	require.Equal(t, launchrule.ReasonHugepagesMismatch, d.Drop.Reason)
	require.Equal(t, "the template has hugepages=1024 and the zone's hugepage pool has 2 MiB pages", d.Drop.Message)

	d = launchrule.Decide(launchrule.NUMAType(1), launchrule.Template{}, launchrule.Zone{Pool: pool, Root: true})
	require.Equal(t, "2", d.HugepagesValue)
}

// TestDecideRootNUMANodesNotKnown checks that as root a non-NUMA type in a hugepage zone whose NUMA node count
// is not known is offered, and clears the hugepages it inherits.
func TestDecideRootNUMANodesNotKnown(t *testing.T) {
	t.Parallel()

	d := launchrule.Decide(launchrule.DedicatedType(), launchrule.KnownTemplate("1024"),
		launchrule.Zone{Pool: zonecapacity.Pool{PageSize: giB}, Root: true})

	require.True(t, d.Offered)
	require.Equal(t, launchrule.DeleteHugepages, d.Hugepages)
	require.Equal(t, launchrule.GuestNUMAFromGrant, d.GuestNUMA)
}

// TestDropNamesTypes checks that a drop names the types it drops, for the NodeClass condition.
func TestDropNamesTypes(t *testing.T) {
	t.Parallel()

	zone := launchrule.Zone{Pool: twoPool}

	require.Equal(t, launchrule.NUMATypes, launchrule.Decide(launchrule.NUMAType(1), templates["2"], zone).Drop.Types)
	require.Equal(t, launchrule.NonNUMATypes, launchrule.Decide(launchrule.CPUShareType(2), templates["2"], zone).Drop.Types)
	require.Equal(t, launchrule.NonNUMATypes, launchrule.Decide(launchrule.DedicatedType(), templates["2"], zone).Drop.Types)
	require.Equal(t, "NUMA", launchrule.NUMATypes.String())
	require.Equal(t, "non-NUMA", launchrule.NonNUMATypes.String())
	require.Contains(t, launchrule.Decide(launchrule.DedicatedType(), templates["unknown"], zone).Drop.Message,
		"VM templates disagree on hugepages")
}

// TestBound checks which VMs are bound to a NUMA node: NUMA types always, dedicated types under the static node
// policy, CPU share types never.
func TestBound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		profile launchrule.Profile
		bound   []zonecapacity.PolicyKind
	}{
		{profile: launchrule.NUMAType(4), bound: policies},
		{profile: launchrule.DedicatedType(), bound: []zonecapacity.PolicyKind{zonecapacity.PolicyStatic}},
		{profile: launchrule.CPUShareType(3)},
	} {
		for _, policy := range policies {
			for _, pool := range []zonecapacity.Pool{noPool, onePool, twoPool} {
				d := launchrule.Decide(tc.profile, templates["none"], launchrule.Zone{Pool: pool, Policy: policy})
				require.Equal(t, slices.Contains(tc.bound, policy), d.Bound, "%+v under policy %d", tc.profile, policy)
			}
		}
	}
}

// TestProfileOf checks the launch profile an instance types file entry gives.
func TestProfileOf(t *testing.T) {
	t.Parallel()

	twoMiB := corev1.ResourceList{"hugepages-2Mi": resource.MustParse("4Gi"), corev1.ResourceCPU: resource.MustParse("2")}
	oneGiB := corev1.ResourceList{"hugepages-1Gi": resource.MustParse("4Gi")}

	for _, tc := range []struct {
		name      string
		numa      int
		cpuShare  int
		capacity  corev1.ResourceList
		want      launchrule.Profile
		hugepages uint64
	}{
		{name: "dedicated type", want: launchrule.DedicatedType()},
		{name: "CPU share type", cpuShare: 3, want: launchrule.CPUShareType(3)},
		{name: "NUMA type", numa: 2, want: launchrule.NUMAType(2)},
		{name: "2MiB request", capacity: twoMiB, want: launchrule.DedicatedType().WithHugepageRequest(2 * miB), hugepages: 2 * miB},
		{name: "1GiB request", numa: 1, capacity: oneGiB, want: launchrule.NUMAType(1).WithHugepageRequest(giB), hugepages: giB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := launchrule.ProfileOf(tc.numa, tc.cpuShare, tc.capacity)
			require.Equal(t, tc.want, p)
			require.Equal(t, tc.numa, p.SlotFraction())
			require.Equal(t, tc.cpuShare, p.CPUShare())
			require.Equal(t, tc.hugepages, p.HugepageRequest())
		})
	}
}
