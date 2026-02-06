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
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/samber/lo"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

const PolicyStatic policyName = "static"

type staticPolicy struct {
	// allCPUs is the set of online CPUs as reported by the system
	allCPUs cpuset.CPUSet
	// availableCPUs is the set of CPUs that are available for exclusive assignment
	availableCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments
	usedCPUs cpuset.CPUSet
	// set of CPUs that is not available for exclusive assignment
	reservedCPUs cpuset.CPUSet
	// Assigned CPUs of VM with dynamic assignments
	assignedCPUs int

	// cpu socket topology
	cpuTopology *topology.CPUTopology
	// we compute this value multiple time, and it's not supposed to change
	// at runtime - the cpumanager can't deal with runtime topology changes anyway.
	cpuGroupSize int

	// Memory-related fields
	memTopology *topology.MemTopology
	numaNodes   map[int]uint64

	// Available memory for allocation
	availableMemory uint64
	// Assigned memory of all VMs
	assignedMemory uint64

	// options allow to fine-tune the behavior of the policy
	options StaticPolicyOptions

	log logr.Logger
	mu  sync.Mutex
}

// Ensure staticPolicy implements Policy interface
var _ Policy = &staticPolicy{}

// NewStaticPolicy returns a resource manager policy that handles both CPU and memory allocation based on static topology-aware strategy
func NewStaticPolicy(logger logr.Logger, sysTopology *topology.Topology, reservedCPUs []int, reservedMemory uint64) (Policy, error) {
	if sysTopology == nil {
		return nil, fmt.Errorf("system topology must be provided for %s policy", string(PolicyStatic))
	}

	reservedCPUSet := cpuset.New(reservedCPUs...)
	if sysTopology.NumCPUs < reservedCPUSet.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", sysTopology.NumCPUs, reservedCPUSet.Size())
	}

	if reservedMemory >= sysTopology.TotalMemory {
		return nil, fmt.Errorf("reserved memory %d must be less than max memory %d", reservedMemory, sysTopology.TotalMemory)
	}

	allCPUs := sysTopology.CPUDetails.CPUs()

	numaNodes := make(map[int]uint64, len(sysTopology.NUMANodes))
	maps.Copy(numaNodes, sysTopology.NUMANodes)

	policy := &staticPolicy{
		allCPUs:       allCPUs,
		availableCPUs: allCPUs.Difference(reservedCPUSet),
		usedCPUs:      cpuset.New(),
		reservedCPUs:  reservedCPUSet,
		options: StaticPolicyOptions{
			FullPhysicalCPUsOnly:           false,
			DistributeCPUsAcrossNUMA:       false,
			DistributeCPUsAcrossCores:      false,
			PreferAlignByUncoreCacheOption: true,
		},
		cpuTopology:  &sysTopology.CPUTopology,
		cpuGroupSize: sysTopology.CPUTopology.CPUsPerCore(),

		memTopology:     &sysTopology.MemTopology,
		numaNodes:       numaNodes,
		availableMemory: sysTopology.TotalMemory - reservedMemory,
		assignedMemory:  0,

		log: logger,
	}

	return policy, nil
}

func (p *staticPolicy) Name() string {
	return string(PolicyStatic)
}

func (p *staticPolicy) AvailableCPUs() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)
}

func (p *staticPolicy) AvailableMemory() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory >= p.availableMemory {
		return 0
	}

	return p.availableMemory - p.assignedMemory
}

func (p *staticPolicy) Allocate(op *resources.VMResources) error {
	if op.CPUs <= 0 && op.Memory == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+op.Memory > p.availableMemory {
		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, p.availableMemory-p.assignedMemory)
	}

	if p.assignedCPUs+op.CPUs > p.availableCPUs.Size() {
		return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, p.availableCPUs.Size()-p.assignedCPUs)
	}

	NUMANodes := make(map[int]goproxmox.NUMANodeState, p.cpuTopology.CPUDetails.NUMANodes().Size())
	availableCPUs := cpuset.New()

	for i, node := range op.NUMANodes {
		if p.numaNodes[i] < node.Memory*1024*1024 {
			return fmt.Errorf("not enough memory available on NUMA node %d", i)
		}

		availableCPUs = availableCPUs.Union(p.cpuTopology.CPUDetails.CPUsInNUMANodes(i))
	}

	if len(op.NUMANodes) == 0 {
		for i := range p.numaNodes {
			if p.numaNodes[i] >= op.Memory {
				availableCPUs = availableCPUs.Union(p.cpuTopology.CPUDetails.CPUsInNUMANodes(i))
			}
		}
	}

	cpus, err := p.takeByTopology(p.log, p.availableCPUs.Intersection(availableCPUs), op.CPUs)
	if err != nil {
		return err
	}

	p.usedCPUs = p.usedCPUs.Union(cpus)
	p.availableCPUs = p.availableCPUs.Difference(cpus)
	op.CPUSet = cpus.Clone()

	CPUinx := 0

	for _, i := range p.cpuTopology.CPUDetails.NUMANodes().List() {
		numaCPUs := op.CPUSet.Intersection(p.cpuTopology.CPUDetails.CPUsInNUMANodes(i))
		if numaCPUs.Size() > 0 {
			NUMANodes[i] = goproxmox.NUMANodeState{
				CPUs:   lo.Must(cpuset.Parse(fmt.Sprintf("%d-%d", CPUinx, CPUinx+numaCPUs.Size()-1))),
				Memory: op.Memory / 1024 / 1024,
				Policy: "bind",
			}

			CPUinx += numaCPUs.Size()
		}
	}

	if len(NUMANodes) > 0 {
		if op.NUMANodes == nil {
			op.NUMANodes = make(map[int]goproxmox.NUMANodeState, len(NUMANodes))
		}

		maps.Copy(op.NUMANodes, NUMANodes)
	}

	for i, node := range op.NUMANodes {
		p.numaNodes[i] -= node.Memory * 1024 * 1024
	}

	p.assignedMemory += op.Memory

	return nil
}

//nolint:dupl
func (p *staticPolicy) AllocateOrUpdate(op *resources.VMResources) error {
	if op.CPUs <= 0 && op.CPUSet.IsEmpty() && op.Memory == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+op.Memory > p.availableMemory {
		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, p.availableMemory-p.assignedMemory)
	}

	var totalMem uint64

	for i, node := range op.NUMANodes {
		if p.numaNodes[i] < node.Memory*1024*1024 {
			return fmt.Errorf("not enough memory available on NUMA node %d: requested=%dM, available=%dM", i, node.Memory, p.numaNodes[i]/1024/1024)
		}

		totalMem += node.Memory * 1024 * 1024
	}

	if totalMem > 0 && totalMem != op.Memory {
		return fmt.Errorf("requested memory does not match sum of NUMA node memory")
	}

	if !op.CPUSet.IsEmpty() {
		if op.CPUSet.Size() > p.allCPUs.Size() {
			return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUSet.Size(), p.allCPUs.Size())
		}

		pinned := op.CPUSet.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Union(pinned)
		p.availableCPUs = p.availableCPUs.Difference(pinned)
	} else if op.CPUs > 0 {
		available := p.availableCPUs.Size() - p.assignedCPUs
		if op.CPUs > available {
			return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, available)
		}

		p.assignedCPUs += op.CPUs
	}

	for i, node := range op.NUMANodes {
		p.numaNodes[i] -= node.Memory * 1024 * 1024
	}

	p.assignedMemory += op.Memory

	return nil
}

//nolint:dupl
func (p *staticPolicy) Release(op *resources.VMResources) error {
	if op.CPUs == 0 && op.CPUSet.IsEmpty() && op.Memory == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory < op.Memory {
		return fmt.Errorf("cannot release memory: requested=%d, assigned=%d", op.Memory, p.assignedMemory)
	}

	if !op.CPUSet.IsEmpty() {
		freed := op.CPUSet.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Difference(freed)
		p.availableCPUs = p.availableCPUs.Union(freed)
	} else if op.CPUs > 0 {
		if p.assignedCPUs < op.CPUs {
			return fmt.Errorf("cannot release CPUs: requested=%d, assigned=%d", op.CPUs, p.assignedCPUs)
		}

		p.assignedCPUs -= op.CPUs
	}

	for i, node := range op.NUMANodes {
		if _, ok := p.numaNodes[i]; !ok {
			continue
		}

		p.numaNodes[i] += node.Memory * 1024 * 1024
	}

	p.assignedMemory -= op.Memory

	return nil
}

func (p *staticPolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	availableCPUs := max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)
	availableMemory := uint64(0)
	if p.assignedMemory < p.availableMemory {
		availableMemory = p.availableMemory - p.assignedMemory
	}

	r := []string{
		fmt.Sprintf("CPU: Free: %d, Static: [%v], Common: [%v], Reserved: [%v], Mem: %dM",
			availableCPUs, p.usedCPUs, p.availableCPUs, p.reservedCPUs, availableMemory/1024/1024),
	}

	nodeIDs := make([]int, 0, len(p.numaNodes))
	for i := range p.numaNodes {
		nodeIDs = append(nodeIDs, i)
	}

	sort.Ints(nodeIDs)

	for _, i := range nodeIDs {
		r = append(r, fmt.Sprintf("N%d:%dM", i, p.numaNodes[i]/1024/1024))
	}

	return strings.Join(r, ", ")
}

func (p *staticPolicy) takeByTopology(logger logr.Logger, availableCPUs cpuset.CPUSet, numCPUs int) (cpuset.CPUSet, error) {
	cpuSortingStrategy := CPUSortingStrategyPacked
	if p.options.DistributeCPUsAcrossCores {
		cpuSortingStrategy = CPUSortingStrategySpread
	}

	if p.options.DistributeCPUsAcrossNUMA {
		cpuGroupSize := 1
		if p.options.FullPhysicalCPUsOnly {
			cpuGroupSize = p.cpuGroupSize
		}

		return takeByTopologyNUMADistributed(logger, p.cpuTopology, availableCPUs, numCPUs, cpuGroupSize, cpuSortingStrategy)
	}

	return takeByTopologyNUMAPacked(logger, p.cpuTopology, availableCPUs, numCPUs, cpuSortingStrategy, p.options.PreferAlignByUncoreCacheOption)
}
