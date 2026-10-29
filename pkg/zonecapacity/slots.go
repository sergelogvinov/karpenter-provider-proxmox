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
	"cmp"
	"fmt"
	"slices"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// GiB is one gibibyte in bytes: NUMA slot memory is sized in whole GiB.
const GiB = uint64(1 << 30)

// SlotFractions names the slot fractions a NUMA node can be divided by, for messages.
const SlotFractions = "1, 2 or 4"

// slotFractions are the slot fractions, whole node first, so each fraction nests inside the one before.
var slotFractions = []int{1, 2, 4}

// ValidSlotFraction reports whether n is a slot fraction: 1, 2 or 4.
func ValidSlotFraction(n int) bool {
	return slices.Contains(slotFractions, n)
}

// SlotShape is the CPU and memory size of a NUMA slot of a slot fraction.
type SlotShape struct {
	Fraction int
	CPUs     int
	Memory   uint64
}

// SlotOffer is one slot shape of a zone, and how many slots of it are free.
type SlotOffer struct {
	Shape SlotShape
	Free  int
}

// slot is one NUMA slot: an aligned whole, half or quarter of a NUMA node's whole cores and memory.
type slot struct {
	node  int
	shape SlotShape
	cpus  cpuset.CPUSet
}

// coreRange is the cores [lo, hi) of a NUMA node, in slot order.
type coreRange struct {
	lo, hi int
}

// Slots is every slot shape of the zone under p with the number of its free slots; nil under
// shared placement, which has no pinning. A slot is free when none of its CPUs is pinned, its
// NUMA node has its memory free, and the zone keeps room for the CPU share groups and the VMs
// under shared placement next to it (docs/scheduler-numa.md §5). Shapes are ordered by fraction, CPUs and memory.
func (z *Zone) Slots(p Policy) []SlotOffer {
	if p.Kind != PolicyStatic {
		return nil
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	u := z.usage(p)
	free := z.free(u)
	room := z.unpinned(u) - u.cpuShareCPUs - u.sharedCPUs

	type nodeShape struct {
		node  int
		shape SlotShape
	}

	slotsClear := map[nodeShape]int{}
	shapes := map[SlotShape]bool{}

	for _, s := range z.slots {
		shapes[s.shape] = true

		if s.cpus.Intersection(u.pinnedCPUs).IsEmpty() {
			slotsClear[nodeShape{node: s.node, shape: s.shape}]++
		}
	}

	freeByShape := map[SlotShape]int{}

	for ns, n := range slotsClear {
		freeByShape[ns.shape] += min(n, int(free.numaMemory[ns.node]/ns.shape.Memory))
	}

	offers := make([]SlotOffer, 0, len(shapes))

	for shape := range shapes {
		offers = append(offers, SlotOffer{Shape: shape, Free: min(freeByShape[shape], max(0, room/shape.CPUs))})
	}

	slices.SortFunc(offers, func(a, b SlotOffer) int {
		return cmp.Or(
			cmp.Compare(a.Shape.Fraction, b.Shape.Fraction),
			cmp.Compare(a.Shape.CPUs, b.Shape.CPUs),
			cmp.Compare(a.Shape.Memory, b.Shape.Memory),
		)
	})

	return offers
}

// carveSlots cuts every NUMA node into its slots. A node's usable whole cores are ordered by uncore
// cache, then core; a slot of fraction k takes len/k of them and memory/k in whole GiB, and the
// slots of each fraction are carved inside those of the fraction before, snapped to an uncore cache
// boundary when the size allows. A fraction too small for one core or one GiB has no slots.
func carveSlots(topo *topology.Topology, usable cpuset.CPUSet, numaMemory map[int]uint64) []slot {
	slots := []slot{}

	for _, node := range topo.CPUDetails.NUMANodes().List() {
		cores, caches := wholeCores(topo, node, usable)
		parents := []coreRange{{lo: 0, hi: len(cores)}}

		for _, fraction := range slotFractions {
			size := len(cores) / fraction
			memory := numaMemory[node] / uint64(fraction) / GiB * GiB

			if size == 0 || memory == 0 {
				break
			}

			ranges := []coreRange{}

			for _, parent := range parents {
				if fraction == 1 {
					ranges = append(ranges, coreRange{lo: parent.lo, hi: parent.lo + size})

					continue
				}

				ranges = append(ranges, splitRange(caches, parent, size)...)
			}

			for _, r := range ranges {
				cpus := topo.CPUDetails.CPUsInCores(cores[r.lo:r.hi]...)

				slots = append(slots, slot{
					node:  node,
					shape: SlotShape{Fraction: fraction, CPUs: cpus.Size(), Memory: memory},
					cpus:  cpus,
				})
			}

			parents = ranges
		}
	}

	return slots
}

// splitRange carves two ranges of size cores out of parent: the first at its start, the second
// right after it, or at the first uncore cache boundary that still leaves it room.
// caches are the uncore caches of the node's cores, in slot order.
func splitRange(caches []int, parent coreRange, size int) []coreRange {
	second := parent.lo + size

	for i := second; i <= parent.hi-size; i++ {
		if caches[i] != caches[i-1] {
			second = i

			break
		}
	}

	return []coreRange{{lo: parent.lo, hi: parent.lo + size}, {lo: second, hi: second + size}}
}

// wholeCores are the cores of node whose CPUs are all usable, ordered by uncore cache, then core,
// with the uncore cache of each.
func wholeCores(topo *topology.Topology, node int, usable cpuset.CPUSet) ([]int, []int) {
	type core struct {
		id, cache int
	}

	found := []core{}

	for _, id := range topo.CPUDetails.CoresInNUMANodes(node).List() {
		cpus := topo.CPUDetails.CPUsInCores(id)
		if cpus.IsSubsetOf(usable) {
			found = append(found, core{id: id, cache: topo.CPUDetails[cpus.List()[0]].UncoreCacheID})
		}
	}

	slices.SortStableFunc(found, func(a, b core) int { return cmp.Compare(a.cache, b.cache) })

	cores := make([]int, len(found))
	caches := make([]int, len(found))

	for i, c := range found {
		cores[i], caches[i] = c.id, c.cache
	}

	return cores, caches
}

// placeSlot is strict slot allocation: it gives a VM the lowest free slot of shape, by NUMA node, then
// slot index. The VM is pinned 1:1 to the slot's CPUs, with one guest NUMA node bound to the slot's NUMA
// node holding all its memory. There is no fallback: without a free slot, the placement fails.
func (z *Zone) placeSlot(free freeResources, shape SlotShape) (cpuset.CPUSet, map[int]NUMABinding, error) {
	for _, s := range z.slots {
		if s.shape != shape || !s.cpus.IsSubsetOf(free.cpus) || free.numaMemory[s.node] < shape.Memory {
			continue
		}

		return s.cpus.Clone(), map[int]NUMABinding{s.node: {VCPUs: vcpuRange(0, s.cpus.Size()), Memory: shape.Memory}}, nil
	}

	return cpuset.New(), nil, fmt.Errorf("no free NUMA slot of fraction %d with %d CPUs and %dM", shape.Fraction, shape.CPUs, shape.Memory/MiB)
}
