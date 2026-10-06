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
	"math"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// PolicySimpleWithCPUOvercommit identifies the opt-in shared CPU allocation policy.
const PolicySimpleWithCPUOvercommit policyName = "simple-with-cpu-overcommit"

type simplePolicyWithCPUOvercommit struct {
	*simplePolicy

	cpuOvercommitRatio float64
	// allocations retains a snapshot per VM so observation and release are idempotent.
	allocations map[int]resources.VMResources
}

var _ Policy = &simplePolicyWithCPUOvercommit{}

// NewSimplePolicyWithCPUOvercommit creates a separate policy for shared CPU overcommit.
// Reserved and affinity-assigned CPUs are excluded from the multiplied budget.
// Memory admission remains strict, while discovery retains all existing VM usage.
func NewSimplePolicyWithCPUOvercommit(sysTopology *topology.Topology, reservedCPUs []int, reservedMemory uint64, ratio float64) (Policy, error) {
	base, err := NewSimplePolicy(sysTopology, reservedCPUs, reservedMemory)
	if err != nil {
		return nil, err
	}

	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 1 {
		return nil, fmt.Errorf("CPU overcommit ratio must be a finite number greater than or equal to 1")
	}

	if float64(sysTopology.NumCPUs)*ratio >= float64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("CPU overcommit ratio exceeds the supported CPU budget")
	}

	return &simplePolicyWithCPUOvercommit{
		simplePolicy:       base.(*simplePolicy),
		cpuOvercommitRatio: ratio,
		allocations:        make(map[int]resources.VMResources),
	}, nil
}

// Name distinguishes the overcommit policy from the unchanged simple policy.
func (p *simplePolicyWithCPUOvercommit) Name() string {
	return string(PolicySimpleWithCPUOvercommit)
}

// AvailableCPUs reports the remaining shared vCPU budget, clamped at zero.
func (p *simplePolicyWithCPUOvercommit) AvailableCPUs() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return max(0, p.sharedCPUCapacity()-p.assignedCPUs)
}

// Allocate admits a new shared VM only when both CPU and memory budgets permit it.
func (p *simplePolicyWithCPUOvercommit) Allocate(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 || op.CPUs <= 0 || op.Memory == 0 || !op.CPUSet.IsEmpty() {
		return fmt.Errorf("cannot allocate resources: a VM ID, shared CPUs, and memory are required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.allocations[op.ID]; exists {
		return fmt.Errorf("resources for VM %d are already allocated", op.ID)
	}

	availableMemory := uint64(0)
	if p.assignedMemory < p.availableMemory {
		availableMemory = p.availableMemory - p.assignedMemory
	}

	if op.Memory > availableMemory {
		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, availableMemory)
	}

	availableCPUs := max(0, p.sharedCPUCapacity()-p.assignedCPUs)
	if op.CPUs > availableCPUs {
		return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, availableCPUs)
	}

	p.recordAllocation(op)

	return nil
}

// AllocateOrUpdate observes existing usage even when it exceeds admission budgets.
// Re-observing a VM replaces its snapshot, retaining memory after a ratio reduction.
func (p *simplePolicyWithCPUOvercommit) AllocateOrUpdate(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 || op.CPUs <= 0 || op.Memory == 0 {
		return fmt.Errorf("cannot observe resources: a VM ID, CPUs, and memory are required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.recordAllocation(op)

	return nil
}

// Release removes only the recorded VM; overlapping affinity stays assigned until
// its last owner releases it. A repeated or unknown release leaves capacity intact.
func (p *simplePolicyWithCPUOvercommit) Release(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 {
		return fmt.Errorf("cannot release resources: a VM ID is required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.allocations, op.ID)
	p.rebuildUsage()

	return nil
}

// Status describes real CPU sets and the remaining shared CPU and memory budgets.
func (p *simplePolicyWithCPUOvercommit) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	availableMemory := uint64(0)
	if p.assignedMemory < p.availableMemory {
		availableMemory = p.availableMemory - p.assignedMemory
	}

	return fmt.Sprintf("CPU: Free: %d, Static: [%v], Common: [%v], Reserved: [%v], Mem: %dM",
		max(0, p.sharedCPUCapacity()-p.assignedCPUs), p.usedCPUs, p.availableCPUs, p.reservedCPUs, availableMemory/1024/1024)
}

// recordAllocation snapshots the relevant resources while p.mu is held.
func (p *simplePolicyWithCPUOvercommit) recordAllocation(op *resources.VMResources) {
	p.allocations[op.ID] = resources.VMResources{
		CPUs:   op.CPUs,
		CPUSet: op.CPUSet.Clone(),
		Memory: op.Memory,
	}
	p.rebuildUsage()
}

// rebuildUsage recomputes affinity ownership and usage while p.mu is held.
// Saturation prevents over-budget discovery from wrapping counters into free capacity.
func (p *simplePolicyWithCPUOvercommit) rebuildUsage() {
	p.usedCPUs = cpuset.New()
	p.assignedCPUs = 0
	p.assignedMemory = 0

	for _, op := range p.allocations {
		if op.CPUSet.IsEmpty() {
			p.assignedCPUs += min(op.CPUs, int(^uint(0)>>1)-p.assignedCPUs)
		} else {
			p.usedCPUs = p.usedCPUs.Union(op.CPUSet.Intersection(p.allCPUs).Difference(p.reservedCPUs))
		}

		p.assignedMemory += min(op.Memory, math.MaxUint64-p.assignedMemory)
	}

	p.availableCPUs = p.allCPUs.Difference(p.reservedCPUs).Difference(p.usedCPUs)
}

// sharedCPUCapacity multiplies real unreserved, unaffined CPU capacity under p.mu.
func (p *simplePolicyWithCPUOvercommit) sharedCPUCapacity() int {
	return int(math.Floor(float64(p.availableCPUs.Size()) * p.cpuOvercommitRatio))
}
