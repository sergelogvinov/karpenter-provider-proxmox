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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cloudresources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/utils/cpuset"
)

type simplePolicy struct {
	mu sync.Mutex

	// Maximum CPUs that can be assigned
	// maxCPUs int
	// Assigned CPUs of VM with dynamic assignments
	assignedCPUs int

	// allCPUs is the set of online CPUs as reported by the system
	allCPUs cpuset.CPUSet
	// availableCPUs is the set of CPUs that are available for exclusive assignment
	availableCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments
	usedCPUs cpuset.CPUSet
	// Reserved CPUs that cannot be assigned
	reservedCPUs cpuset.CPUSet
}

// PolicySimple name of simple policy
const PolicySimple policyName = "simple"

// Ensure simplePolicy implements Policy interface
var _ Policy = &simplePolicy{}

// NewSimplePolicy returns a cpuset manager policy that does nothing
func NewSimplePolicy(topology *topology.CPUTopology, reserved []int) (Policy, error) {
	if topology == nil {
		return nil, fmt.Errorf("topology must be provided for simple cpu policy")
	}

	reservedCPUs := cpuset.New(reserved...)
	if topology.NumCPUs < reservedCPUs.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", topology.NumCPUs, reservedCPUs.Size())
	}

	allCPUs := topology.CPUDetails.CPUs()

	return &simplePolicy{
		// maxCPUs:       topology.NumCPUs,
		allCPUs:       allCPUs,
		availableCPUs: allCPUs.Difference(reservedCPUs),
		usedCPUs:      cpuset.New(),
		reservedCPUs:  reservedCPUs,
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

func (p *simplePolicy) Allocate(op *cloudresources.VMResources) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedCPUs+op.CPUs > p.availableCPUs.Size() {
		return fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", op.CPUs, p.availableCPUs.Size())
	}

	p.assignedCPUs += op.CPUs

	return nil
}

func (p *simplePolicy) AllocateOrUpdate(op *cloudresources.VMResources) error {
	if op.CPUs <= 0 && op.CPUSet.IsEmpty() {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !op.CPUSet.IsEmpty() {
		if op.CPUSet.Size() > p.allCPUs.Size() {
			return fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", op.CPUSet.Size(), p.allCPUs.Size())
		}

		pinned := op.CPUSet.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Union(pinned)
		p.availableCPUs = p.availableCPUs.Difference(pinned)

		return nil
	}

	available := p.availableCPUs.Size() - p.assignedCPUs
	if op.CPUs > available {
		return fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", op.CPUs, available)
	}

	p.assignedCPUs += op.CPUs

	return nil
}

//nolint:dupl
func (p *simplePolicy) Release(op *cloudresources.VMResources) error {
	if op.CPUs == 0 && op.CPUSet.IsEmpty() {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !op.CPUSet.IsEmpty() {
		freed := op.CPUSet.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Difference(freed)
		p.availableCPUs = p.availableCPUs.Union(freed)

		return nil
	}

	if op.CPUs > 0 {
		if p.assignedCPUs < op.CPUs {
			return fmt.Errorf("cannot release CPUs")
		}

		p.assignedCPUs -= op.CPUs
	}

	return nil
}

func (p *simplePolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	available := max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)

	return fmt.Sprintf("Free: %d, Static: [%v], Common: [%v], Reserved: [%v]", available, p.usedCPUs, p.availableCPUs, p.reservedCPUs)
}
