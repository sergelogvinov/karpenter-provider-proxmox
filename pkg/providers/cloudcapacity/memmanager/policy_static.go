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
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cloudresources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager/topology"
)

type staticPolicy struct {
	mu sync.Mutex

	NUMANodes map[int]uint64

	maxMemory      uint64
	assignedMemory uint64

	// log logr.Logger
}

var _ Policy = &staticPolicy{}

// PolicyStatic name of static policy
const PolicyStatic policyName = "static"

// NewStaticPolicy returns a static memory manager policy
func NewStaticPolicy(_ logr.Logger, topology *topology.MemTopology, reservedMemory uint64) (Policy, error) {
	if topology == nil {
		return nil, fmt.Errorf("topology must be provided for static memory policy")
	}

	if reservedMemory >= topology.TotalMemory {
		return nil, fmt.Errorf("reserved memory %d must be less than max memory %d", reservedMemory, topology.TotalMemory)
	}

	policy := staticPolicy{
		maxMemory: topology.TotalMemory - reservedMemory,
	}

	policy.NUMANodes = make(map[int]uint64, len(topology.NUMANodes))
	maps.Copy(policy.NUMANodes, topology.NUMANodes)

	return &policy, nil
}

func (p *staticPolicy) Name() string {
	return string(PolicyStatic)
}

func (p *staticPolicy) Allocate(op *cloudresources.VMResources) error {
	mem := op.Memory

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory+mem > p.maxMemory {
		return fmt.Errorf("not enough memory available")
	}

	var totalMem uint64

	for i, node := range op.NUMANodes {
		if p.NUMANodes[i] < node.Memory {
			return fmt.Errorf("not enough memory available on NUMA node")
		}

		if node.Memory == 0 {
			node.Memory = (mem / 1024 / 1024) / uint64(len(op.NUMANodes))
			op.NUMANodes[i] = node
		}

		totalMem += node.Memory * 1024 * 1024
	}

	if totalMem > 0 && totalMem != mem {
		return fmt.Errorf("requested memory does not match sum of NUMA node memory")
	}

	for i, node := range op.NUMANodes {
		p.NUMANodes[i] -= node.Memory * 1024 * 1024
	}

	p.assignedMemory += mem

	return nil
}

func (p *staticPolicy) AllocateOrUpdate(op *cloudresources.VMResources) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	mem := op.Memory
	if p.assignedMemory+mem > p.maxMemory {
		return fmt.Errorf("not enough memory available")
	}

	var totalMem uint64

	for i, node := range op.NUMANodes {
		if p.NUMANodes[i] < node.Memory {
			return fmt.Errorf("not enough memory available on NUMA node")
		}

		totalMem += node.Memory * 1024 * 1024
	}

	if totalMem > 0 && totalMem != mem {
		return fmt.Errorf("requested memory does not match sum of NUMA node memory")
	}

	for i, node := range op.NUMANodes {
		p.NUMANodes[i] -= node.Memory * 1024 * 1024
	}

	p.assignedMemory += mem

	return nil
}

func (p *staticPolicy) Release(op *cloudresources.VMResources) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	mem := op.Memory
	if mem > 0 {
		if p.assignedMemory < mem {
			return fmt.Errorf("cannot release memory")
		}

		for i, node := range op.NUMANodes {
			p.NUMANodes[i] += node.Memory * 1024 * 1024
		}

		p.assignedMemory -= mem
	}

	return nil
}

func (p *staticPolicy) AvailableMemory() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.assignedMemory >= p.maxMemory {
		return 0
	}

	return p.maxMemory - p.assignedMemory
}

func (p *staticPolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	var available uint64
	if p.assignedMemory < p.maxMemory {
		available = p.maxMemory - p.assignedMemory
	}

	r := []string{fmt.Sprintf("%dM", available/1024/1024)}

	nodeIDs := make([]int, 0, len(p.NUMANodes))
	for i := range p.NUMANodes {
		nodeIDs = append(nodeIDs, i)
	}

	sort.Ints(nodeIDs)

	for _, i := range nodeIDs {
		r = append(r, fmt.Sprintf("N%d:%dM", i, p.NUMANodes[i]/1024/1024))
	}

	return strings.Join(r, ", ")
}
