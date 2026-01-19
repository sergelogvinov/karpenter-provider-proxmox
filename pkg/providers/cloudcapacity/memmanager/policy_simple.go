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

package memmanager

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager/topology"
)

type simplePolicy struct {
	mu sync.Mutex

	topology *topology.MemTopology

	maxMemory      uint64
	assignedMemory uint64
}

var _ Policy = &simplePolicy{}

// PolicySimple name of simple policy
const PolicySimple policyName = "simple"

// NewSimplePolicy returns a simple memory manager policy
func NewSimplePolicy(topology *topology.MemTopology, reservedMemory uint64) (Policy, error) {
	if topology == nil {
		return nil, fmt.Errorf("topology must be provided for simple memory policy")
	}

	if reservedMemory >= topology.TotalMemory {
		return nil, fmt.Errorf("reserved memory %d must be less than max memory %d", reservedMemory, topology.TotalMemory)
	}

	return &simplePolicy{
		topology:  topology,
		maxMemory: topology.TotalMemory - reservedMemory,
	}, nil
}

func (p *simplePolicy) Name() string {
	return string(PolicySimple)
}

func (p *simplePolicy) AvailableMemory() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory > p.maxMemory {
		return 0
	}

	return p.maxMemory - p.assignedMemory
}

func (p *simplePolicy) Allocate(mem uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+mem > p.maxMemory {
		return fmt.Errorf("not enough memory available")
	}

	p.assignedMemory += mem

	return nil
}

func (p *simplePolicy) AllocateOrUpdate(mem uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+mem > p.maxMemory {
		return fmt.Errorf("not enough memory available")
	}

	p.assignedMemory += mem

	return nil
}

func (p *simplePolicy) Release(mem uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if mem > 0 {
		if p.assignedMemory < mem {
			return fmt.Errorf("cannot release memory")
		}

		p.assignedMemory -= mem
	}

	return nil
}

func (p *simplePolicy) Status() string {
	r := []string{fmt.Sprintf("%dM", p.AvailableMemory()/1024/1024)}

	nodeIDs := make([]int, 0, len(p.topology.NUMANodes))
	for i := range p.topology.NUMANodes {
		nodeIDs = append(nodeIDs, i)
	}

	sort.Ints(nodeIDs)

	for _, i := range nodeIDs {
		r = append(r, fmt.Sprintf("N%d:%dM", i, p.topology.NUMANodes[i]/1024/1024))
	}

	return strings.Join(r, ", ")
}
