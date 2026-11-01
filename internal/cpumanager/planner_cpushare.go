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

package cpumanager

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// CPUShareRequest is one CPU share VM's request for a seat in a CPU share group.
type CPUShareRequest struct {
	VMID int
	// CPUs is the VM's vCPU count, and the width of its group.
	CPUs int
	// Share is the number of seats of the VM's group: 2, 3 or 4.
	Share int
	// Current is the cpuset applied to the VM. Members with equal Current sets form an
	// existing group, which the planner keeps when it can.
	Current cpuset.CPUSet
}

// CPUShareGroup is one planned CPU share group.
type CPUShareGroup struct {
	// CPUs are the host CPUs every member is masked to. They are empty for a group
	// that did not fit when the other groups left no CPUs: its members are not seated.
	CPUs cpuset.CPUSet
	// Members are the VMIDs seated in the group, in VMID order.
	Members []int
}

// CPUShareClass is the shape every member of a CPU share group has: the group's
// seats and each member's vCPUs.
type CPUShareClass struct {
	Seats int
	VCPUs int
}

// Groups is how many groups count CPU share VMs of the class need: ceil(count/Seats).
func (c CPUShareClass) Groups(count int) int {
	return (count + c.Seats - 1) / c.Seats
}

// CPUShareCPUs is the one count of host CPUs the CPU share groups take: each class
// of count seated VMs needs c.Groups(count) groups of c.VCPUs host CPUs.
func CPUShareCPUs(seated map[CPUShareClass]int) int {
	cpus := 0

	for c, count := range seated {
		cpus += c.Groups(count) * c.VCPUs
	}

	return cpus
}

// PlanCPUShare seats every request in a CPU share group within pool
// (docs/scheduler-cpushare.md §8.3). Each (Share, CPUs) class gets exactly
// ceil(count/Share) groups. Existing groups are kept while they are inside pool and
// disjoint from the other kept groups; a class with more groups than it needs
// dissolves its emptiest ones (ties: highest CPU IDs), and their members fill the
// open seats of the kept ones, fullest first. New groups take whole physical cores
// on one NUMA node, the node with the most pinned CPUs first, and spill across nodes
// for whole cores only when no single node has room; a group with an odd width takes
// any CPUs. A group that does not fit takes what the other groups left, up to its
// width, never overlapping them; when nothing is left its members are not seated.
//
// It is a pure function of its arguments: classes are processed by CPUs descending
// then Share ascending, members by VMID. The notes say which groups spilled or did
// not fit.
func PlanCPUShare(logger logr.Logger, topo *topology.CPUTopology, pool, pinned cpuset.CPUSet, reqs []CPUShareRequest) ([]CPUShareGroup, []string, error) {
	if topo == nil {
		return nil, nil, fmt.Errorf("cpu topology must be provided")
	}

	pool = pool.Intersection(topo.CPUDetails.CPUs())

	members := map[CPUShareClass][]CPUShareRequest{}

	for _, req := range reqs {
		if req.CPUs <= 0 || req.Share <= 0 {
			continue
		}

		c := CPUShareClass{Seats: req.Share, VCPUs: req.CPUs}
		members[c] = append(members[c], req)
	}

	classes := slices.SortedFunc(maps.Keys(members), func(a, b CPUShareClass) int {
		return cmp.Or(cmp.Compare(b.VCPUs, a.VCPUs), cmp.Compare(a.Seats, b.Seats))
	})

	remaining := pool
	kept := map[CPUShareClass][]*CPUShareGroup{}

	// Keep first, for every class, so a new group never takes the CPUs of a group kept later.
	for _, c := range classes {
		slices.SortFunc(members[c], func(a, b CPUShareRequest) int { return cmp.Compare(a.VMID, b.VMID) })

		need := c.Groups(len(members[c]))

		for _, g := range existingGroups(members[c]) {
			if len(kept[c]) == need {
				break
			}

			if g.CPUs.Size() != c.VCPUs || !g.CPUs.IsSubsetOf(remaining) {
				continue
			}

			g.Members = g.Members[:min(len(g.Members), c.Seats)]
			kept[c] = append(kept[c], g)
			remaining = remaining.Difference(g.CPUs)
		}
	}

	type unfitGroup struct {
		index int // into groups
		width int
	}

	var (
		groups []CPUShareGroup
		unfit  []unfitGroup
		notes  []string
	)

	for _, c := range classes {
		seated := map[int]bool{}

		for _, g := range kept[c] {
			for _, id := range g.Members {
				seated[id] = true
			}
		}

		var unseated []int

		for _, req := range members[c] {
			if !seated[req.VMID] {
				unseated = append(unseated, req.VMID)
			}
		}

		// kept[c] is already fullest first.
		for _, g := range kept[c] {
			n := min(c.Seats-len(g.Members), len(unseated))
			g.Members = append(g.Members, unseated[:n]...)
			unseated = unseated[n:]

			slices.Sort(g.Members)
			groups = append(groups, *g)
		}

		for len(unseated) > 0 {
			n := min(c.Seats, len(unseated))
			g := CPUShareGroup{CPUs: cpuset.New(), Members: unseated[:n:n]}
			unseated = unseated[n:]

			placed, ok := placeCPUShareGroup(logger, topo, remaining, pinned, c.VCPUs)
			if !ok {
				unfit = append(unfit, unfitGroup{index: len(groups), width: c.VCPUs})
				groups = append(groups, g)

				continue
			}

			if placed.spilled {
				notes = append(notes, fmt.Sprintf("CPU share group of VMs %v spills across NUMA nodes %v: %s", g.Members, numaNodesOf(topo, placed.cpus), placed.cpus))
			}

			g.CPUs = placed.cpus
			remaining = remaining.Difference(placed.cpus)

			groups = append(groups, g)
		}
	}

	// The groups that do not fit take what is left, up to their width, one after the other.
	for _, u := range unfit {
		g := &groups[u.index]

		if remaining.IsEmpty() {
			notes = append(notes, fmt.Sprintf("CPU share group of VMs %v: no CPUs left in the group pool %s, not seated and left as is", g.Members, pool))

			continue
		}

		taken, err := takeByTopologyNUMAPacked(logger, topo, remaining, min(u.width, remaining.Size()), CPUSortingStrategyPacked, true)
		if err != nil {
			taken = remaining
		}

		g.CPUs = taken
		remaining = remaining.Difference(taken)

		notes = append(notes, fmt.Sprintf("CPU share group of VMs %v: no room for %d CPUs in the group pool %s, masked to %s", g.Members, u.width, pool, taken))
	}

	return groups, notes, nil
}

// existingGroups are the groups members already form, from their equal Current sets,
// fullest first, then lowest CPU IDs first.
func existingGroups(members []CPUShareRequest) []*CPUShareGroup {
	byCPUs := map[string]*CPUShareGroup{}

	for _, req := range members {
		if req.Current.IsEmpty() {
			continue
		}

		key := req.Current.String()
		if byCPUs[key] == nil {
			byCPUs[key] = &CPUShareGroup{CPUs: req.Current}
		}

		byCPUs[key].Members = append(byCPUs[key].Members, req.VMID)
	}

	groups := slices.Collect(maps.Values(byCPUs))
	slices.SortFunc(groups, func(a, b *CPUShareGroup) int {
		return cmp.Or(cmp.Compare(len(b.Members), len(a.Members)), slices.Compare(a.CPUs.List(), b.CPUs.List()))
	})

	return groups
}

// placedGroup is where a new CPU share group went.
type placedGroup struct {
	cpus cpuset.CPUSet
	// spilled is true when the group spans more than one NUMA node.
	spilled bool
}

// placeCPUShareGroup takes cpus CPUs from available for a new group: whole physical
// cores on one NUMA node, the node with the most pinned CPUs first (ties: lowest node
// ID), otherwise whole cores across nodes. Only a group that is not a whole number of
// cores takes any CPUs. It reports false when the group does not fit.
func placeCPUShareGroup(logger logr.Logger, topo *topology.CPUTopology, available, pinned cpuset.CPUSet, cpus int) (placedGroup, bool) {
	// candidates keeps whole cores of set when the group is a whole number of cores.
	candidates := func(set cpuset.CPUSet) cpuset.CPUSet {
		if cpus%max(1, topo.CPUsPerCore()) == 0 {
			return wholeCores(topo, set)
		}

		return set
	}

	take := func(set cpuset.CPUSet) (cpuset.CPUSet, bool) {
		if set.Size() < cpus {
			return cpuset.New(), false
		}

		taken, err := takeByTopologyNUMAPacked(logger, topo, set, cpus, CPUSortingStrategyPacked, true)

		return taken, err == nil
	}

	nodes := topo.CPUDetails.NUMANodes().List()
	slices.SortStableFunc(nodes, func(a, b int) int {
		return cmp.Compare(pinned.Intersection(topo.CPUDetails.CPUsInNUMANodes(b)).Size(), pinned.Intersection(topo.CPUDetails.CPUsInNUMANodes(a)).Size())
	})

	for _, node := range nodes {
		if taken, ok := take(candidates(available.Intersection(topo.CPUDetails.CPUsInNUMANodes(node)))); ok {
			return placedGroup{cpus: taken}, true
		}
	}

	if taken, ok := take(candidates(available)); ok {
		return placedGroup{cpus: taken, spilled: len(numaNodesOf(topo, taken)) > 1}, true
	}

	return placedGroup{}, false
}

// wholeCores are the CPUs of set whose physical core lies entirely inside set.
func wholeCores(topo *topology.CPUTopology, set cpuset.CPUSet) cpuset.CPUSet {
	whole := []int{}

	for _, cpu := range set.List() {
		if topo.CPUDetails.CPUsInCores(topo.CPUDetails[cpu].CoreID).IsSubsetOf(set) {
			whole = append(whole, cpu)
		}
	}

	return cpuset.New(whole...)
}

// numaNodesOf are the NUMA nodes cpus touches.
func numaNodesOf(topo *topology.CPUTopology, cpus cpuset.CPUSet) []int {
	nodes := []int{}

	for _, cpu := range cpus.List() {
		nodes = append(nodes, topo.CPUDetails[cpu].NUMANodeID)
	}

	return cpuset.New(nodes...).List()
}
