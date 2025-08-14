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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"k8s.io/utils/cpuset"
)

// Policy implements logic for CPU assignment.
type Policy interface {
	// Allocate call is idempotent
	Allocate(numCPUs int) error
	// RemoveContainer call is idempotent
	Remove(cpus string) error
}

type staticPolicy struct {
	// cpu socket topology
	topology *topology.CPUTopology
	// set of CPUs that is not available for exclusive assignment
	reservedCPUs cpuset.CPUSet
	// Superset of reservedCPUs. It includes not just the reservedCPUs themselves,
	// but also any siblings of those reservedCPUs on the same physical die.
	// NOTE: If the reserved set includes full physical CPUs from the beginning
	// (e.g. only reserved pairs of core siblings) this set is expected to be
	// identical to the reserved set.
	reservedPhysicalCPUs cpuset.CPUSet
	// set of CPUs to reuse across allocations in a pod
	cpusToReuse map[string]cpuset.CPUSet
	// options allow to fine-tune the behaviour of the policy
	options StaticPolicyOptions
	// we compute this value multiple time, and it's not supposed to change
	// at runtime - the cpumanager can't deal with runtime topology changes anyway.
	cpuGroupSize int
}

// Ensure staticPolicy implements Policy interface
var _ Policy = &staticPolicy{}

func NewStaticPolicy(topology *topology.CPUTopology, numReservedCPUs int, reservedCPUs cpuset.CPUSet) (Policy, error) {
	cpuGroupSize := topology.CPUsPerCore()

	policy := &staticPolicy{
		topology:     topology,
		cpusToReuse:  make(map[string]cpuset.CPUSet),
		cpuGroupSize: cpuGroupSize,
	}

	allCPUs := topology.CPUDetails.CPUs()
	var reserved cpuset.CPUSet
	if reservedCPUs.Size() > 0 {
		reserved = reservedCPUs
	} else {
		// takeByTopology allocates CPUs associated with low-numbered cores from
		// allCPUs.
		//
		// For example: Given a system with 8 CPUs available and HT enabled,
		// if numReservedCPUs=2, then reserved={0,4}
		reserved, _ = policy.takeByTopology(allCPUs, numReservedCPUs)
	}

	if reserved.Size() != numReservedCPUs {
		err := fmt.Errorf("[cpumanager] unable to reserve the required amount of CPUs (size of %s did not equal %d)", reserved, numReservedCPUs)
		return nil, err
	}

	var reservedPhysicalCPUs cpuset.CPUSet
	for _, cpu := range reserved.UnsortedList() {
		core, err := topology.CPUCoreID(cpu)
		if err != nil {
			return nil, fmt.Errorf("[cpumanager] unable to build the reserved physical CPUs from the reserved set: %w", err)
		}
		reservedPhysicalCPUs = reservedPhysicalCPUs.Union(topology.CPUDetails.CPUsInCores(core))
	}

	policy.reservedCPUs = reserved
	policy.reservedPhysicalCPUs = reservedPhysicalCPUs

	return policy, nil
}

func (p *staticPolicy) takeByTopology(availableCPUs cpuset.CPUSet, numCPUs int) (cpuset.CPUSet, error) {
	cpuSortingStrategy := CPUSortingStrategyPacked
	if p.options.DistributeCPUsAcrossCores {
		cpuSortingStrategy = CPUSortingStrategySpread
	}

	if p.options.DistributeCPUsAcrossNUMA {
		cpuGroupSize := 1
		if p.options.FullPhysicalCPUsOnly {
			cpuGroupSize = p.cpuGroupSize
		}
		return takeByTopologyNUMADistributed(p.topology, availableCPUs, numCPUs, cpuGroupSize, cpuSortingStrategy)
	}

	return takeByTopologyNUMAPacked(p.topology, availableCPUs, numCPUs, cpuSortingStrategy, p.options.PreferAlignByUncoreCacheOption)
}

func (p *staticPolicy) Allocate(numCPUs int) (rerr error) {
	return nil
}

func (p *staticPolicy) Remove(cpus string) error {
	return nil
}
