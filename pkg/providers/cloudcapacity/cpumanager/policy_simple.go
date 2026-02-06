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
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

type simplePolicy struct {
	// allCPUs is the set of online CPUs as reported by the system
	allCPUs cpuset.CPUSet
	// availableCPUs is the set of CPUs that are available for exclusive assignment
	availableCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments
	usedCPUs cpuset.CPUSet
	// Reserved CPUs that cannot be assigned
	reservedCPUs cpuset.CPUSet
	// Assigned CPUs of VM with dynamic assignments
	assignedCPUs int

	// Available memory for allocation
	availableMemory uint64
	// Assigned memory of all VMs
	assignedMemory uint64

	mu sync.Mutex
}

// PolicySimple name of simple policy
const PolicySimple policyName = "simple"

// Ensure simplePolicy implements Policy interface
var _ Policy = &simplePolicy{}

// NewSimplePolicy returns a resource manager policy that handles both CPU and memory allocation
func NewSimplePolicy(sysTopology *topology.Topology, reservedCPUs []int, reservedMemory uint64) (Policy, error) {
	if sysTopology == nil {
		return nil, fmt.Errorf("system topology must be provided for %s policy", string(PolicySimple))
	}

	reservedCPUSet := cpuset.New(reservedCPUs...)
	if sysTopology.NumCPUs < reservedCPUSet.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs: %d, reservedCPUs: %d", sysTopology.NumCPUs, reservedCPUSet.Size())
	}

	if reservedMemory >= sysTopology.TotalMemory {
		return nil, fmt.Errorf("reserved memory %d must be less than max memory %d", reservedMemory, sysTopology.TotalMemory)
	}

	allCPUs := sysTopology.CPUDetails.CPUs()

	return &simplePolicy{
		allCPUs:         allCPUs,
		availableCPUs:   allCPUs.Difference(reservedCPUSet),
		usedCPUs:        cpuset.New(),
		reservedCPUs:    reservedCPUSet,
		availableMemory: sysTopology.TotalMemory - reservedMemory,
	}, nil
}

func (p *simplePolicy) Name() string {
	return string(PolicySimple)
}

func (p *simplePolicy) AvailableCPUs() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)
}

func (p *simplePolicy) AvailableMemory() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory >= p.availableMemory {
		return 0
	}

	return p.availableMemory - p.assignedMemory
}

//nolint:dupl
func (p *simplePolicy) Allocate(op *resources.VMResources) error {
	if op.CPUs <= 0 && op.Memory == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+op.Memory > p.availableMemory {
		available := uint64(0)
		if p.assignedMemory < p.availableMemory {
			available = p.availableMemory - p.assignedMemory
		}

		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, available)
	}

	if p.assignedCPUs+op.CPUs > p.availableCPUs.Size() {
		return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, p.availableCPUs.Size()-p.assignedCPUs)
	}

	p.assignedCPUs += op.CPUs
	p.assignedMemory += op.Memory

	return nil
}

// nolint:dupl
func (p *simplePolicy) AllocateOrUpdate(op *resources.VMResources) error {
	if op.CPUs <= 0 && op.CPUSet.IsEmpty() && op.Memory == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+op.Memory > p.availableMemory {
		available := uint64(0)
		if p.assignedMemory < p.availableMemory {
			available = p.availableMemory - p.assignedMemory
		}

		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, available)
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

	p.assignedMemory += op.Memory

	return nil
}

//nolint:dupl
func (p *simplePolicy) Release(op *resources.VMResources) error {
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

	p.assignedMemory -= op.Memory

	return nil
}

func (p *simplePolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	availableCPUs := max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)
	availableMemory := uint64(0)
	if p.assignedMemory < p.availableMemory {
		availableMemory = p.availableMemory - p.assignedMemory
	}

	return fmt.Sprintf("CPU: Free: %d, Static: [%v], Common: [%v], Reserved: [%v], Mem: %dM",
		availableCPUs, p.usedCPUs, p.availableCPUs, p.reservedCPUs, availableMemory/1024/1024)
}
