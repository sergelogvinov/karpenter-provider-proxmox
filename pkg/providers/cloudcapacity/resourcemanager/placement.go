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
	"errors"
	"fmt"
	"sort"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// errNUMAMemory means a CPU distribution puts more memory on a NUMA node than it has free.
var errNUMAMemory = errors.New("not enough memory available on NUMA node")

// Placement decides where a zone puts the CPUs of a newly reserved VM.
type Placement interface {
	// ratio is how many shared vCPUs the zone offers per unpinned host CPU.
	ratio() float64
	// place returns the host CPUs and NUMA layout for op, or an empty set for a shared VM.
	place(free freeResources, op *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error)
}

// freeResources is what the zone has left for a new VM.
type freeResources struct {
	topo *topology.Topology
	// usable are the CPUs not reserved for the host.
	usable cpuset.CPUSet
	// cpus are the unreserved CPUs no VM is pinned to.
	cpus cpuset.CPUSet
	// numaMemory is each NUMA node's free memory, capped by the zone's free memory.
	numaMemory map[int]uint64
}

type shared struct {
	cpuOvercommitRatio float64
}

// Shared places VMs on the unpinned CPUs, offering ratio shared vCPUs per CPU.
func Shared(ratio float64) Placement {
	return shared{cpuOvercommitRatio: ratio}
}

func (p shared) ratio() float64 {
	return p.cpuOvercommitRatio
}

func (p shared) place(freeResources, *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error) {
	return cpuset.New(), nil, nil
}

type pinned struct {
	log logr.Logger
}

// Pinned gives each VM its own host CPUs, on the one NUMA node it leaves fullest
// when one can hold it, otherwise packed across NUMA nodes with its memory
// split across them in proportion to its CPUs on each.
func Pinned(log logr.Logger) Placement {
	return pinned{log: log}
}

func (p pinned) ratio() float64 {
	return 1
}

func (p pinned) place(free freeResources, op *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error) {
	if len(op.NUMANodes) > 0 {
		nodes := cpuset.New()
		for i := range op.NUMANodes {
			nodes = nodes.Union(cpuset.New(i))
		}

		return p.take(free, nodes, op)
	}

	for _, node := range tightestNodes(free, op) {
		if cpus, numaNodes, err := p.take(free, cpuset.New(node), op); err == nil {
			return cpus, numaNodes, nil
		}
	}

	// Keep the VM on nodes that could each hold all of its memory when possible;
	// otherwise span every node with free memory and let the split decide.
	if cpus, numaNodes, err := p.take(free, nodesWithMemory(free, op.Memory), op); err == nil {
		return cpus, numaNodes, nil
	}

	nodes := nodesWithMemory(free, 1)

	cpus, numaNodes, err := p.take(free, nodes, op)
	if errors.Is(err, errNUMAMemory) {
		// Packing put too much memory on some node; spread the CPUs to where the memory is.
		return p.takeByMemory(free, nodes, op)
	}

	return cpus, numaNodes, err
}

// takeByMemory gives each node no more of op's CPUs than its free memory can back,
// filling the nodes that can back the most CPUs first.
func (p pinned) takeByMemory(free freeResources, nodes cpuset.CPUSet, op *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error) {
	totalMiB := max(1, op.Memory/1024/1024)

	type room struct {
		node int
		cpus int
	}

	rooms := make([]room, 0, nodes.Size())
	for _, i := range nodes.List() {
		freeCPUs := free.topo.CPUDetails.CPUsInNUMANodes(i).Intersection(free.cpus).Size()
		backed := int(free.numaMemory[i] / 1024 / 1024 * uint64(op.CPUs) / totalMiB)

		if n := min(freeCPUs, backed); n > 0 {
			rooms = append(rooms, room{node: i, cpus: n})
		}
	}

	sort.SliceStable(rooms, func(a, b int) bool { return rooms[a].cpus > rooms[b].cpus })

	cpus := cpuset.New()
	left := op.CPUs

	for _, r := range rooms {
		if left == 0 {
			break
		}

		n := min(left, r.cpus)

		taken, err := cpumanager.TakeByTopology(p.log, &free.topo.CPUTopology, free.topo.CPUDetails.CPUsInNUMANodes(r.node).Intersection(free.cpus), n)
		if err != nil {
			return cpuset.New(), nil, err
		}

		cpus = cpus.Union(taken)
		left -= n
	}

	if left > 0 {
		return cpuset.New(), nil, fmt.Errorf("not enough memory available across NUMA nodes for %d CPUs and %dM", op.CPUs, op.Memory/1024/1024)
	}

	numaNodes, err := splitNUMAMemory(free, cpus, op.Memory)
	if err != nil {
		return cpuset.New(), nil, err
	}

	return cpus, numaNodes, nil
}

// tightestNodes returns the NUMA nodes that can each hold all of op's CPUs and memory,
// the one op would leave fullest first, ties by lowest node.
func tightestNodes(free freeResources, op *resources.VMResources) []int {
	type nodeFit struct {
		node     int
		leftover float64
	}

	fits := []nodeFit{}

	for _, i := range free.topo.CPUDetails.NUMANodes().List() {
		nodeCPUs := free.topo.CPUDetails.CPUsInNUMANodes(i)
		freeCPUs := nodeCPUs.Intersection(free.cpus).Size()

		if freeCPUs < op.CPUs || free.numaMemory[i] < op.Memory {
			continue
		}

		fits = append(fits, nodeFit{
			node:     i,
			leftover: scarcerShare(freeCPUs-op.CPUs, nodeCPUs.Intersection(free.usable).Size(), free.numaMemory[i]-op.Memory, free.topo.NUMANodes[i]),
		})
	}

	sort.SliceStable(fits, func(a, b int) bool { return fits[a].leftover < fits[b].leftover })

	nodes := make([]int, len(fits))
	for i, f := range fits {
		nodes[i] = f.node
	}

	return nodes
}

// scarcerShare is the smaller of the free CPU share and the free memory share.
func scarcerShare(freeCPUs, cpus int, freeMemory, memory uint64) float64 {
	if cpus <= 0 || memory == 0 {
		return 0
	}

	return min(float64(freeCPUs)/float64(cpus), float64(freeMemory)/float64(memory))
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

// take picks op's CPUs from the free CPUs of the given NUMA nodes and splits its memory across them.
func (p pinned) take(free freeResources, nodes cpuset.CPUSet, op *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error) {
	candidates := free.topo.CPUDetails.CPUsInNUMANodes(nodes.List()...).Intersection(free.cpus)
	if candidates.Size() < op.CPUs {
		return cpuset.New(), nil, fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, candidates.Size())
	}

	cpus, err := cpumanager.TakeByTopology(p.log, &free.topo.CPUTopology, candidates, op.CPUs)
	if err != nil {
		return cpuset.New(), nil, err
	}

	numaNodes, err := splitNUMAMemory(free, cpus, op.Memory)
	if err != nil {
		return cpuset.New(), nil, err
	}

	return cpus, numaNodes, nil
}

// splitNUMAMemory gives each NUMA node a share of memory in proportion to the VM's CPUs on it,
// in MiB, with the rounding remainder on the lowest node. Each node's CPUs are numbered
// from the VM's point of view, in NUMA node order. It fails if a node's share exceeds its free memory.
func splitNUMAMemory(free freeResources, cpus cpuset.CPUSet, memory uint64) (map[int]resources.NUMANodeState, error) {
	topo := free.topo
	totalMiB := memory / 1024 / 1024
	nodes := make(map[int]resources.NUMANodeState)

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

		mem := totalMiB * uint64(n) / uint64(cpus.Size())
		assigned += mem

		nodes[i] = resources.NUMANodeState{
			CPUs:   fmt.Sprintf("%d-%d", vcpu, vcpu+n-1),
			Memory: mem,
			Policy: "bind",
		}

		vcpu += n
	}

	if lowest >= 0 {
		node := nodes[lowest]
		node.Memory += totalMiB - assigned
		nodes[lowest] = node
	}

	for i, node := range nodes {
		if node.Memory*1024*1024 > free.numaMemory[i] {
			return nil, fmt.Errorf("%w %d: requested=%dM, available=%dM", errNUMAMemory, i, node.Memory, free.numaMemory[i]/1024/1024)
		}
	}

	return nodes, nil
}
