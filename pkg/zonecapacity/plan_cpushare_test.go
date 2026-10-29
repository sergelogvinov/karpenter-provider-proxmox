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
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	"k8s.io/utils/cpuset"
)

// TestPlanCPUShareWorkedExampleMerge is the merge of docs/scheduler-cpushare.md §9: with the groups
// applied as the example lists them, the plan keeps them; once VM 302 stops, class (2, 2) needs one
// group, so group B (the tie with the higher CPU IDs) is dissolved and VM 303 moves to group A.
func TestPlanCPUShareWorkedExampleMerge(t *testing.T) {
	t.Parallel()

	z := trackWorkedExample(t)

	applied := map[int]cpuset.CPUSet{
		100: mustParse(t, "8-15"),
		301: cpuset.New(16, 48),
		302: cpuset.New(16, 48),
		303: cpuset.New(17, 49),
		401: mustParse(t, "18-21,50-53"),
	}

	plan := z.Plan(applied, zonecapacity.PlanOptions{})
	require.Equal(t, map[int]zonecapacity.Assignment{
		200: {CPUs: mustParse(t, "1-7,22-31,33-47,54-63"), Placement: zonecapacity.PlacementShared},
	}, plan.Changed, "the groups are kept, the VM under shared placement is kept off them")
	require.Equal(t, mustParse(t, "16-21,48-53"), plan.Groups)
	require.Equal(t, mustParse(t, "1-7,22-31,33-47,54-63"), plan.Ungrouped, "the sharedPool of §9, with the siblings 40-47 of VM 100")
	require.Empty(t, plan.Notes)

	applied[200] = plan.Changed[200].CPUs

	z.Release(302)
	delete(applied, 302)

	plan = z.Plan(applied, zonecapacity.PlanOptions{})
	require.Equal(t, map[int]zonecapacity.Assignment{
		303: {CPUs: cpuset.New(16, 48), Placement: zonecapacity.PlacementCPUShare},
		200: {CPUs: mustParse(t, "1-7,17,22-31,33-47,49,54-63"), Placement: zonecapacity.PlacementShared},
	}, plan.Changed, "VM 303 moves to 16,48 and 17,49 is ungrouped again")
	require.False(t, plan.Changed[303].OneToOne(), "a member is masked to its group")
	require.Equal(t, mustParse(t, "16,18-21,48,50-53"), plan.Groups)
	require.Empty(t, plan.Notes)
}

// TestPlanCPUShareWorkedExampleFresh plans the VMs of docs/scheduler-cpushare.md §9 with nothing
// applied: the groups take whole cores on one NUMA node. Node 1, the node with the most pinned
// CPUs, has no whole core left (VM 100 holds one thread of each), so they go to node 0, the lowest
// of the rest.
func TestPlanCPUShareWorkedExampleFresh(t *testing.T) {
	t.Parallel()

	z := trackWorkedExample(t)

	for _, policy := range []zonecapacity.PlanOptions{{}, partition} {
		plan := z.Plan(nil, policy)

		groupOf := func(id int) cpuset.CPUSet {
			t.Helper()

			a, ok := plan.Changed[id]
			require.True(t, ok, "VM %d is placed", id)
			require.Equal(t, zonecapacity.PlacementCPUShare, a.Placement)

			return a.CPUs
		}

		a, b, c := groupOf(301), groupOf(303), groupOf(401)
		require.Equal(t, a, groupOf(302), "VMs 301 and 302 share group A")

		for _, g := range []cpuset.CPUSet{a, b, c} {
			require.Equal(t, []int{0}, numaNodes(g), "group %s", g)
			require.True(t, wholeCores(g), "group %s is whole cores", g)
		}

		require.Equal(t, []int{2, 2, 8}, []int{a.Size(), b.Size(), c.Size()})
		require.Equal(t, 12, plan.Groups.Size(), "eCPUs = 12, as Karpenter counts")
		require.True(t, a.Union(b).Union(c).Equals(plan.Groups))
		require.True(t, plan.Groups.Intersection(plan.Pinned).IsEmpty())
		require.True(t, plan.Changed[200].CPUs.IsSubsetOf(plan.Ungrouped), "the VM under shared placement stays off the groups")
		require.Empty(t, plan.Notes)
	}
}

// TestPlanCPUSharePinnedVMMovesGroup is docs/scheduler-cpushare.md §10: Karpenter may pin a new VM
// on CPUs a group holds; the next plan moves the whole group off them, and leaves the other alone.
func TestPlanCPUSharePinnedVMMovesGroup(t *testing.T) {
	t.Parallel()

	z := trackWorkedExample(t)

	applied := map[int]cpuset.CPUSet{
		100: mustParse(t, "8-15"),
		301: cpuset.New(16, 48),
		302: cpuset.New(16, 48),
		303: cpuset.New(17, 49),
		401: mustParse(t, "18-21,50-53"),
	}

	track(t, z, zonecapacity.VM{ID: 500, CPUs: 2, Memory: gib, Affinity: "16,48"})

	plan := z.Plan(applied, zonecapacity.PlanOptions{})

	// Group A is gone; its members fill group B's open seat first, then a new group.
	require.Equal(t, zonecapacity.Assignment{CPUs: cpuset.New(17, 49), Placement: zonecapacity.PlacementCPUShare}, plan.Changed[301])

	moved := plan.Changed[302]
	require.Equal(t, zonecapacity.PlacementCPUShare, moved.Placement)
	require.Equal(t, 2, moved.CPUs.Size())
	require.True(t, wholeCores(moved.CPUs))
	require.True(t, moved.CPUs.Intersection(mustParse(t, "8-21,48-53")).IsEmpty(), "off the pinned VMs and the other groups: %s", moved.CPUs)
	require.NotContains(t, plan.Changed, 303)
	require.NotContains(t, plan.Changed, 401)
	require.False(t, plan.Groups.Contains(16), "nothing is left on the pinned VM's CPUs")
	require.Equal(t, 12, plan.Groups.Size())
	require.Empty(t, plan.Notes)
}

// TestPlanCPUShareSpills checks that a group spills across NUMA nodes, with a note, only when no
// single node has room for it.
func TestPlanCPUShareSpills(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		// Leave one whole core free on each NUMA node: 7,39 / 15,47 / 23,55 / 31,63.
		zonecapacity.VM{ID: 100, CPUs: 12, Memory: gib, Affinity: "1-6,33-38"},
		zonecapacity.VM{ID: 101, CPUs: 14, Memory: gib, Affinity: "8-14,40-46"},
		zonecapacity.VM{ID: 102, CPUs: 14, Memory: gib, Affinity: "16-22,48-54"},
		zonecapacity.VM{ID: 103, CPUs: 14, Memory: gib, Affinity: "24-30,56-62"},
		zonecapacity.VM{ID: 301, CPUs: 4, Memory: gib, Description: "cpushare=2"},
	)

	plan := z.Plan(nil, zonecapacity.PlanOptions{})

	require.Equal(t, 4, plan.Changed[301].CPUs.Size())
	require.True(t, wholeCores(plan.Changed[301].CPUs))
	require.Len(t, numaNodes(plan.Changed[301].CPUs), 2)
	require.Len(t, plan.Notes, 1)
	require.Contains(t, plan.Notes[0], "spills across NUMA nodes")
	require.Contains(t, plan.Notes[0], "[301]")
}

// TestPlanCPUShareGroupsThatDoNotFit checks that groups the unpinned CPUs cannot hold (VMs created
// by hand) are masked to what is left instead of failing, with a note and a drift note.
func TestPlanCPUShareGroupsThatDoNotFit(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 100, CPUs: 56, Memory: gib, Affinity: "1-28,33-60"},
		// 29-31,61-63 are left: room for the 4-CPU group, not for the 8-CPU one.
		zonecapacity.VM{ID: 301, CPUs: 8, Memory: gib, Description: "cpushare=2"},
		zonecapacity.VM{ID: 401, CPUs: 4, Memory: gib, Description: "cpushare=4"},
		zonecapacity.VM{ID: 200, CPUs: 2, Memory: gib},
	)

	plan := z.Plan(nil, partition)

	require.Equal(t, 4, plan.Changed[401].CPUs.Size(), "the group that fits keeps its full width")
	require.Equal(t, mustParse(t, "29-31,61-63").Difference(plan.Changed[401].CPUs), plan.Changed[301].CPUs, "the other gets the rest")
	require.NotContains(t, plan.Changed, 200, "no ungrouped CPUs are left for the VM under shared placement")
	require.Equal(t, mustParse(t, "29-31,61-63"), plan.Groups)
	require.Len(t, plan.Notes, 3)
	require.Contains(t, plan.Notes[0], "VMs [301]: no room for 8 CPUs")
	require.Contains(t, plan.Notes[1], "drift: the groups use 6 CPUs, accounting charges 12")
	require.Contains(t, plan.Notes[2], "VM 200")
}

// TestPlanCPUShareGroupsWithNothingLeft checks that a group the other groups left no CPUs for is
// not seated: its member is left as it is instead of being masked onto another group.
func TestPlanCPUShareGroupsWithNothingLeft(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 100, CPUs: 60, Memory: gib, Affinity: "1-30,33-62"},
		// 31,63 are left: room for the 2-CPU group only.
		zonecapacity.VM{ID: 301, CPUs: 2, Memory: gib, Description: "cpushare=2"},
		zonecapacity.VM{ID: 401, CPUs: 1, Memory: gib, Description: "cpushare=2"},
	)

	plan := z.Plan(map[int]cpuset.CPUSet{100: mustParse(t, "1-30,33-62")}, zonecapacity.PlanOptions{})

	require.Equal(t, map[int]zonecapacity.Assignment{
		301: {CPUs: cpuset.New(31, 63), Placement: zonecapacity.PlacementCPUShare},
	}, plan.Changed, "VM 401 is not masked onto VM 301's group")
	require.Equal(t, cpuset.New(31, 63), plan.Groups)
	require.Equal(t, []string{
		"CPU share group of VMs [401]: no CPUs left in the group pool 31,63, not seated and left as is",
		"CPU share groups drift: the groups use 2 CPUs, accounting charges 3",
	}, plan.Notes)
}

// TestPlanCPUShareConsistency checks, for random VM sets the zone admits, that the CPU share groups
// take exactly the CPUs the zone's accounting charges, and that groups, pinned VMs and VMs under
// shared placement never overlap, under either --shared-policy. It plans again with the plan applied
// after VMs stop, so merges are checked too.
func TestPlanCPUShareConsistency(t *testing.T) {
	t.Parallel()

	for _, o := range []zonecapacity.PlanOptions{partition, {}} {
		t.Run(fmt.Sprintf("partition=%t", o.Partition), func(t *testing.T) {
			t.Parallel()

			for seed := range uint64(200) {
				checkCPUShareConsistencySeed(t, seed, o)
			}
		})
	}
}

// checkCPUShareConsistencySeed plans a random VM set from seed with o three times, with some VMs
// stopping between the rounds.
func checkCPUShareConsistencySeed(t *testing.T, seed uint64, o zonecapacity.PlanOptions) {
	t.Helper()

	{
		rng := rand.New(rand.NewPCG(272, seed))
		z := newZone(t, schedulerHost, 0, 32)

		vms := map[int]zonecapacity.VM{}

		for id := 1; id <= 5+rng.IntN(40); id++ {
			vm := zonecapacity.VM{ID: id, CPUs: 1 + rng.IntN(8), Memory: gib}

			switch rng.IntN(3) {
			case 0:
				grant, err := z.Reserve(static, vm)
				if err != nil {
					continue
				}

				vm.Affinity = grant.CPUSet.String()
			case 1:
				vm.CPUs = []int{2, 4, 8}[rng.IntN(3)]
				vm.CPUShare = 2 + rng.IntN(3)

				if _, err := z.Reserve(static, vm); err != nil {
					continue
				}
			default:
				if z.Available(shared).CPUs < vm.CPUs {
					continue
				}

				track(t, z, vm)
			}

			vms[id] = vm
		}

		applied := map[int]cpuset.CPUSet{}

		for round := range 3 {
			plan := z.Plan(maps.Clone(applied), o)
			maps.Copy(applied, cpusOf(plan.Changed))

			name := fmt.Sprintf("seed %d, round %d", seed, round)
			checkCPUShareConsistency(t, name, plan, vms, applied, o.Partition)
			require.Equal(t, z.CPUShareCPUs(static), plan.Groups.Size(), "%s: groups take what accounting charges", name)

			// Some VMs stop.
			for _, id := range slices.Sorted(maps.Keys(vms)) {
				if rng.IntN(3) == 0 {
					z.Release(id)
					delete(vms, id)
					delete(applied, id)
				}
			}
		}
	}
}

// checkCPUShareConsistency checks plan for vms, with applied holding every VM's applied cpuset.
// With partitioned, the VMs under shared placement must not overlap one another either.
func checkCPUShareConsistency(t *testing.T, name string, plan zonecapacity.Plan, vms map[int]zonecapacity.VM, applied map[int]cpuset.CPUSet, partitioned bool) {
	t.Helper()

	type class struct{ seats, vcpus int }

	groups := map[string][]zonecapacity.VM{}
	sharedVMs := cpuset.New()

	for _, vm := range vms {
		cpus := applied[vm.ID]

		switch {
		case vm.CPUShare > 0:
			groups[cpus.String()] = append(groups[cpus.String()], vm)
			require.Equal(t, vm.CPUs, cpus.Size(), "%s: VM %d sees its group of %d CPUs", name, vm.ID, vm.CPUs)
		case vm.Affinity != "":
			require.Equal(t, vm.Affinity, cpus.String(), "%s: pinned VM %d keeps its affinity", name, vm.ID)
		default:
			if partitioned {
				require.True(t, sharedVMs.Intersection(cpus).IsEmpty(), "%s: VM %d under shared placement overlaps another", name, vm.ID)
			}

			sharedVMs = sharedVMs.Union(cpus)
		}
	}

	groupCPUs := cpuset.New()

	for key, members := range groups {
		first := members[0]
		require.LessOrEqual(t, len(members), first.CPUShare, "%s: group %s has a seat per member", name, key)

		for _, m := range members {
			require.Equal(t, class{first.CPUShare, first.CPUs}, class{m.CPUShare, m.CPUs}, "%s: group %s mixes classes", name, key)
		}

		require.True(t, groupCPUs.Intersection(mustParse(t, key)).IsEmpty(), "%s: group %s overlaps another", name, key)
		groupCPUs = groupCPUs.Union(mustParse(t, key))
	}

	require.Equal(t, groupCPUs, plan.Groups, name)
	require.True(t, groupCPUs.Intersection(plan.Pinned).IsEmpty(), "%s: groups off pinned VMs", name)
	require.True(t, sharedVMs.Intersection(plan.Pinned.Union(groupCPUs)).IsEmpty(), "%s: VMs under shared placement off pinned VMs and groups", name)

	for _, note := range plan.Notes {
		require.Contains(t, note, "spills across NUMA nodes", "%s: only spills while the zone fits", name)
	}
}

// wholeCores reports whether cpus are whole schedulerHost cores, siblings 32 apart.
func wholeCores(cpus cpuset.CPUSet) bool {
	for _, cpu := range cpus.List() {
		if !cpus.Contains((cpu + 32) % 64) {
			return false
		}
	}

	return true
}

// TestPlanCPUShareSoftPacksAroundSlots is docs/scheduler-numa.md §8: a new CPU share group goes to the
// NUMA node with the most pinned CPUs, so it stays where it is when a NUMA VM takes the empty node.
func TestPlanCPUShareSoftPacksAroundSlots(t *testing.T) {
	t.Parallel()

	z := newZone(t, numaExampleHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 100, CPUs: 2, Memory: gib, Affinity: "31,63"},
		zonecapacity.VM{ID: 301, CPUs: 4, Memory: gib, Description: "cpushare=2"},
	)

	plan := z.Plan(map[int]cpuset.CPUSet{100: cpuset.New(31, 63)}, zonecapacity.PlanOptions{})
	group := plan.Changed[301].CPUs
	require.True(t, group.IsSubsetOf(mustParse(t, "16-31,48-63")), "on the node with the pinned VM: %s", group)

	// Karpenter launches a NUMA VM into the whole slot of node 0.
	reserve(t, z, static, slotVM(200, numaExampleSlots[0].Shape))

	plan = z.Plan(map[int]cpuset.CPUSet{100: cpuset.New(31, 63), 301: group, 200: mustParse(t, "1-15,33-47")}, zonecapacity.PlanOptions{})
	require.Empty(t, plan.Changed, "the group does not move")
	require.Empty(t, plan.Notes)
}
