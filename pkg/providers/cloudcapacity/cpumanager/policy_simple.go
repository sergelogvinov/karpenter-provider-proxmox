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

	"k8s.io/utils/cpuset"
)

type simplePolicy struct {
	mu sync.Mutex

	maxCPUs      int
	assignedCPUs int
}

var _ Policy = &simplePolicy{}

// PolicySimple name of simple policy
const PolicySimple policyName = "simple"

// NewSimplePolicy returns a cpuset manager policy that does nothing
func NewSimplePolicy(maxCPUs int) (Policy, error) {
	return &simplePolicy{
		maxCPUs: maxCPUs,
	}, nil
}

func (p *simplePolicy) Name() string {
	return string(PolicySimple)
}

func (p *simplePolicy) AvailableCPUs() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedCPUs > p.maxCPUs {
		return 0
	}

	return p.maxCPUs - p.assignedCPUs
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
	p.mu.Lock()
	defer p.mu.Unlock()

	n := max(numCPUs, cpus.Size())
	if p.assignedCPUs+n > p.maxCPUs {
		return cpuset.CPUSet{}, fmt.Errorf("not enough CPUs available")
	}

	if n > 0 {
		p.assignedCPUs += n
	}

	return cpuset.New(), nil
}

func (p *simplePolicy) Release(numCPUs int, cpus cpuset.CPUSet) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := numCPUs
	if n == 0 {
		n = cpus.Size()
	}

	if n > 0 {
		if p.assignedCPUs < n {
			return fmt.Errorf("cannot release CPUs")
		}

		p.assignedCPUs -= n
	}

	return nil
}

func (p *simplePolicy) Status() string {
	return fmt.Sprintf("%d", p.AvailableCPUs())
}
