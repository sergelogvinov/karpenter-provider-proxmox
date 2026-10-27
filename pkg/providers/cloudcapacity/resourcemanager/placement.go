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
	"fmt"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

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

// Pinned gives each VM its own host CPUs, packed by NUMA node, with its memory
// split across those NUMA nodes in proportion to its CPUs on each.
func Pinned(log logr.Logger) Placement {
	return pinned{log: log}
}

func (p pinned) ratio() float64 {
	return 1
}

func (p pinned) place(free freeResources, op *resources.VMResources) (cpuset.CPUSet, map[int]resources.NUMANodeState, error) {
	candidates := cpuset.New()

	if len(op.NUMANodes) > 0 {
		for i := range op.NUMANodes {
			candidates = candidates.Union(free.topo.CPUDetails.CPUsInNUMANodes(i))
		}
	} else {
		for i, mem := range free.numaMemory {
			if mem >= op.Memory {
				candidates = candidates.Union(free.topo.CPUDetails.CPUsInNUMANodes(i))
			}
		}
	}

	candidates = candidates.Intersection(free.cpus)
	if candidates.Size() < op.CPUs {
		return cpuset.New(), nil, fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, candidates.Size())
	}

	cpus, err := cpumanager.TakeByTopology(p.log, &free.topo.CPUTopology, candidates, op.CPUs)
	if err != nil {
		return cpuset.New(), nil, err
	}

	return cpus, splitNUMAMemory(free.topo, cpus, op.Memory), nil
}

// splitNUMAMemory gives each NUMA node a share of memory in proportion to the VM's CPUs on it,
// in MiB, with the rounding remainder on the lowest node. Each node's CPUs are numbered
// from the VM's point of view, in NUMA node order.
func splitNUMAMemory(topo *topology.Topology, cpus cpuset.CPUSet, memory uint64) map[int]resources.NUMANodeState {
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

	return nodes
}
