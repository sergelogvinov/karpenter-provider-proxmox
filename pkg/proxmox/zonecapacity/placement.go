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

package zonecapacity

import (
	"errors"
	"fmt"
	"sort"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/internal/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// MiB is one mebibyte in bytes: Proxmox sizes VM and NUMA memory in MiB.
const MiB = uint64(1 << 20)

// errNUMAMemory means a CPU distribution puts more memory on a NUMA node than it has free.
var errNUMAMemory = errors.New("not enough memory available on NUMA node")

// freeResources is what the zone has left for a new VM.
type freeResources struct {
	topo *topology.Topology
	// usable are the CPUs not reserved for the host.
	usable cpuset.CPUSet
	// cpus are the unreserved CPUs no VM is pinned to.
	cpus cpuset.CPUSet
	// numaMemory is each NUMA node's free memory, capped by the zone's free memory.
	numaMemory map[int]uint64
	// slots are the NUMA slots of the zone, clear or not. Placement reads the slots from here only.
	slots []slot
}

// isClear reports whether none of slot s's CPUs is pinned and its NUMA node still has its memory.
// It is weaker than a free slot (docs/scheduler-numa.md §5), which also needs the zone to keep
// room for the CPU share groups and the VMs under shared placement.
func (f freeResources) isClear(s slot) bool {
	return s.cpus.IsSubsetOf(f.cpus) && f.numaMemory[s.node] >= s.shape.Memory
}

// placePinned gives a VM its own host CPUs, on the one NUMA node it leaves fullest
// when one can hold it, otherwise packed across NUMA nodes with its memory
// split across them in proportion to its CPUs on each. It soft packs around NUMA
// slots (docs/scheduler-numa.md §8): a node whose whole slot is clear is tried last,
// and CPUs that break the fewest clear slots are taken first, without spanning more NUMA nodes.
func placePinned(free freeResources, cpus int, memory uint64) (cpuset.CPUSet, map[int]NUMABinding, error) {
	for _, node := range preferredNodes(free, cpus, memory) {
		if set, numa, err := take(free, cpuset.New(node), cpus, memory); err == nil {
			return set, numa, nil
		}
	}

	// Keep the VM on nodes that could each hold all of its memory when possible;
	// otherwise span every node with free memory and let the split decide.
	if set, numa, err := take(free, nodesWithMemory(free, memory), cpus, memory); err == nil {
		return set, numa, nil
	}

	nodes := nodesWithMemory(free, 1)

	set, numa, err := take(free, nodes, cpus, memory)
	if errors.Is(err, errNUMAMemory) {
		// Packing put too much memory on some node; spread the CPUs to where the memory is.
		return takeByMemory(free, nodes, cpus, memory)
	}

	return set, numa, err
}

// takeByMemory gives each node no more of the VM's CPUs than its free memory can back,
// filling the nodes that can back the most CPUs first.
func takeByMemory(free freeResources, nodes cpuset.CPUSet, numCPUs int, memory uint64) (cpuset.CPUSet, map[int]NUMABinding, error) {
	totalMiB := max(1, memory/MiB)

	type room struct {
		node int
		cpus int
	}

	rooms := make([]room, 0, nodes.Size())
	for _, i := range nodes.List() {
		freeCPUs := free.topo.CPUDetails.CPUsInNUMANodes(i).Intersection(free.cpus).Size()
		backed := int(free.numaMemory[i] / MiB * uint64(numCPUs) / totalMiB)

		if n := min(freeCPUs, backed); n > 0 {
			rooms = append(rooms, room{node: i, cpus: n})
		}
	}

	sort.SliceStable(rooms, func(a, b int) bool { return rooms[a].cpus > rooms[b].cpus })

	cpus := cpuset.New()
	left := numCPUs

	for _, r := range rooms {
		if left == 0 {
			break
		}

		n := min(left, r.cpus)

		taken, err := cpumanager.TakeByTopology(logr.Discard(), &free.topo.CPUTopology, free.topo.CPUDetails.CPUsInNUMANodes(r.node).Intersection(free.cpus), n)
		if err != nil {
			return cpuset.New(), nil, err
		}

		cpus = cpus.Union(taken)
		left -= n
	}

	if left > 0 {
		return cpuset.New(), nil, fmt.Errorf("not enough memory available across NUMA nodes for %d CPUs and %dM", numCPUs, memory/MiB)
	}

	numa, err := splitNUMAMemory(free, cpus, memory)
	if err != nil {
		return cpuset.New(), nil, err
	}

	return cpus, numa, nil
}

// preferredNodes returns the NUMA nodes that can each hold all of the VM's CPUs and memory,
// the nodes already in use before those whose whole slot is clear, then the one the VM would
// leave fullest first, ties by lowest node.
func preferredNodes(free freeResources, cpus int, memory uint64) []int {
	type nodeFit struct {
		node       int
		wholeClear bool
		leftover   float64
	}

	fits := []nodeFit{}

	for _, i := range free.topo.CPUDetails.NUMANodes().List() {
		nodeCPUs := free.topo.CPUDetails.CPUsInNUMANodes(i)
		freeCPUs := nodeCPUs.Intersection(free.cpus).Size()

		if freeCPUs < cpus || free.numaMemory[i] < memory {
			continue
		}

		fits = append(fits, nodeFit{
			node:       i,
			wholeClear: wholeSlotClear(free, i),
			leftover:   scarcerShare(freeCPUs-cpus, nodeCPUs.Intersection(free.usable).Size(), free.numaMemory[i]-memory, free.topo.NUMANodes[i]),
		})
	}

	sort.SliceStable(fits, func(a, b int) bool {
		if fits[a].wholeClear != fits[b].wholeClear {
			return fits[b].wholeClear
		}

		return fits[a].leftover < fits[b].leftover
	})

	nodes := make([]int, len(fits))
	for i, f := range fits {
		nodes[i] = f.node
	}

	return nodes
}

// nodesWithMemory returns the NUMA nodes with at least memory bytes free.
func nodesWithMemory(free freeResources, memory uint64) cpuset.CPUSet {
	nodes := cpuset.New()

	for i, mem := range free.numaMemory {
		if mem >= memory {
			nodes = nodes.Union(cpuset.New(i))
		}
	}

	return nodes
}

// wholeSlotClear reports whether the NUMA node's whole-node slot is still clear.
func wholeSlotClear(free freeResources, node int) bool {
	for _, s := range free.slots {
		if s.node == node && s.shape.Fraction == 1 && free.isClear(s) {
			return true
		}
	}

	return false
}

// take picks the VM's CPUs from the free CPUs of the given NUMA nodes and splits its memory across them.
// It tries the CPUs that break the fewest clear NUMA slots first, and falls back to any free CPU.
// Soft packing never spans more NUMA nodes than the fallback does: across several nodes, a preferred
// placement is taken only when it spans no more of them.
func take(free freeResources, nodes cpuset.CPUSet, numCPUs int, memory uint64) (cpuset.CPUSet, map[int]NUMABinding, error) {
	candidates := free.topo.CPUDetails.CPUsInNUMANodes(nodes.List()...).Intersection(free.cpus)
	if candidates.Size() < numCPUs {
		return cpuset.New(), nil, fmt.Errorf("not enough CPUs available: requested=%d, available=%d", numCPUs, candidates.Size())
	}

	var (
		anyCPUs cpuset.CPUSet
		anyNUMA map[int]NUMABinding
		span    = 1
	)

	if nodes.Size() > 1 {
		var err error
		if anyCPUs, anyNUMA, err = takeFrom(free, candidates, numCPUs, memory); err != nil {
			return cpuset.New(), nil, err
		}

		span = len(anyNUMA)
	}

	for _, preferred := range slotPreference(free, candidates) {
		if preferred.Size() < numCPUs {
			continue
		}

		if cpus, numa, err := takeFrom(free, preferred, numCPUs, memory); err == nil && len(numa) <= span {
			return cpus, numa, nil
		}
	}

	if anyNUMA != nil {
		return anyCPUs, anyNUMA, nil
	}

	return takeFrom(free, candidates, numCPUs, memory)
}

// slotPreference are strictly growing subsets of candidates, short of all of them: the CPUs inside
// at most 0, 1, 2… clear NUMA slots, so a VM keeps off clear slots, and breaks the smallest ones when it must.
func slotPreference(free freeResources, candidates cpuset.CPUSet) []cpuset.CPUSet {
	clearSlotsOn := map[int]int{}

	for _, s := range free.slots {
		if !free.isClear(s) {
			continue
		}

		for _, cpu := range s.cpus.Intersection(candidates).UnsortedList() {
			clearSlotsOn[cpu]++
		}
	}

	maxClearSlots := 0
	for _, n := range clearSlotsOn {
		maxClearSlots = max(maxClearSlots, n)
	}

	levels := make([]cpuset.CPUSet, 0, maxClearSlots)
	previous := cpuset.New()

	for breaks := range maxClearSlots {
		cpus := []int{}

		for _, cpu := range candidates.UnsortedList() {
			if clearSlotsOn[cpu] <= breaks {
				cpus = append(cpus, cpu)
			}
		}

		if level := cpuset.New(cpus...); level.Size() > previous.Size() {
			levels = append(levels, level)
			previous = level
		}
	}

	return levels
}

// takeFrom picks the VM's CPUs from candidates and splits its memory across their NUMA nodes.
func takeFrom(free freeResources, candidates cpuset.CPUSet, numCPUs int, memory uint64) (cpuset.CPUSet, map[int]NUMABinding, error) {
	cpus, err := cpumanager.TakeByTopology(logr.Discard(), &free.topo.CPUTopology, candidates, numCPUs)
	if err != nil {
		return cpuset.New(), nil, err
	}

	numa, err := splitNUMAMemory(free, cpus, memory)
	if err != nil {
		return cpuset.New(), nil, err
	}

	return cpus, numa, nil
}

// splitNUMAMemory gives each NUMA node a share of memory in proportion to the VM's CPUs on it,
// in whole MiB, with the rounding remainder on the lowest node. Each node's vCPUs are numbered
// from the VM's point of view, in NUMA node order. It fails if a node's share exceeds its free memory.
func splitNUMAMemory(free freeResources, cpus cpuset.CPUSet, memory uint64) (map[int]NUMABinding, error) {
	topo := free.topo
	totalMiB := memory / MiB
	nodes := make(map[int]NUMABinding)

	var (
		vcpu     int
		assigned uint64
		lowest   = -1
	)

	for _, i := range topo.CPUDetails.NUMANodes().List() {
		n := cpus.Intersection(topo.CPUDetails.CPUsInNUMANodes(i)).Size()
		if n == 0 {
			continue
		}

		if lowest < 0 {
			lowest = i
		}

		memMiB := totalMiB * uint64(n) / uint64(cpus.Size())
		assigned += memMiB

		nodes[i] = NUMABinding{VCPUs: vcpuRange(vcpu, n), Memory: memMiB * MiB}

		vcpu += n
	}

	if lowest >= 0 {
		node := nodes[lowest]
		node.Memory += (totalMiB - assigned) * MiB
		nodes[lowest] = node
	}

	for i, node := range nodes {
		if node.Memory > free.numaMemory[i] {
			return nil, fmt.Errorf("%w %d: requested=%dM, available=%dM", errNUMAMemory, i, node.Memory/MiB, free.numaMemory[i]/MiB)
		}
	}

	return nodes, nil
}

// vcpuRange is the n guest vCPUs numbered from first.
func vcpuRange(first, n int) cpuset.CPUSet {
	vcpus := make([]int, n)
	for k := range vcpus {
		vcpus[k] = first + k
	}

	return cpuset.New(vcpus...)
}
