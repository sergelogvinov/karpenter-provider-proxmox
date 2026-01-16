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

	"k8s.io/utils/cpuset"
)

type simplePolicy struct {
	mu sync.Mutex

	// Maximum CPUs that can be assigned
	maxCPUs int
	// Assigned CPUs of VM with dynamic assignments
	assignedCPUs int

	// allCPUs is the set of online CPUs as reported by the system
	allCPUs cpuset.CPUSet
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
func NewSimplePolicy(topology *topology.CPUTopology, reservedCPUs cpuset.CPUSet) (Policy, error) {
	if topology.NumCPUs < reservedCPUs.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", topology.NumCPUs, reservedCPUs.Size())
	}

	allCPUs := topology.CPUDetails.CPUs().Difference(reservedCPUs)

	return &simplePolicy{
		maxCPUs:      topology.NumCPUs,
		allCPUs:      allCPUs,
		usedCPUs:     cpuset.New(),
		reservedCPUs: reservedCPUs,
	}, nil
}

func (p *simplePolicy) Name() string {
	return string(PolicySimple)
}

func (p *simplePolicy) AvailableCPUs() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return max(0, p.maxCPUs-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)
}

func (p *simplePolicy) Allocate(numCPUs int) (cpuset.CPUSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedCPUs+numCPUs > p.maxCPUs {
		return cpuset.CPUSet{}, fmt.Errorf("not enough CPUs available")
	}

	p.assignedCPUs += numCPUs

	return cpuset.New(), nil
}

func (p *simplePolicy) AllocateOrUpdate(numCPUs int, cpus cpuset.CPUSet) (cpuset.CPUSet, error) {
	if numCPUs == 0 && cpus.Size() == 0 {
		return cpuset.New(), nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	n := max(numCPUs, cpus.Size())
	if p.assignedCPUs+n > p.maxCPUs {
		return cpuset.CPUSet{}, fmt.Errorf("not enough CPUs available")
	}

	if cpus.Size() > 0 {
		p.usedCPUs = p.usedCPUs.Union(cpus).Difference(p.reservedCPUs)

		return cpuset.New(), nil
	}

	if n > 0 {
		p.assignedCPUs += n
	}

	return cpuset.New(), nil
}

func (p *simplePolicy) Release(numCPUs int, cpus cpuset.CPUSet) error {
	if numCPUs == 0 && cpus.Size() == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if cpus.Size() > 0 {
		p.usedCPUs = p.usedCPUs.Difference(cpus)

		return nil
	}

	if numCPUs > 0 {
		if p.assignedCPUs < numCPUs {
			return fmt.Errorf("cannot release CPUs")
		}

		p.assignedCPUs -= numCPUs
	}

	return nil
}

func (p *simplePolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	available := max(0, p.maxCPUs-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)

	return fmt.Sprintf("Free: %d, Static: [%v], All: [%v], Reserved: [%v]", available, p.usedCPUs, p.allCPUs, p.reservedCPUs)
}
