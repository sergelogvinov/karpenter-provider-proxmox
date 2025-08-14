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

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/utils/cpuset"
)

const PolicyStatic policyName = "static"

type staticPolicy struct {
	mu sync.Mutex

	// Assigned CPUs of VM with dynamic assignments
	assignedCPUs int

	// allCPUs is the set of online CPUs as reported by the system
	allCPUs cpuset.CPUSet
	// availableCPUs is the set of CPUs that are available for exclusive assignment
	availableCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments
	usedCPUs cpuset.CPUSet
	// set of CPUs that is not available for exclusive assignment
	reservedCPUs cpuset.CPUSet

	// options allow to fine-tune the behavior of the policy
	options StaticPolicyOptions

	// cpu socket topology
	topology *topology.CPUTopology
	// we compute this value multiple time, and it's not supposed to change
	// at runtime - the cpumanager can't deal with runtime topology changes anyway.
	cpuGroupSize int

	log logr.Logger
}

// Ensure staticPolicy implements Policy interface
var _ Policy = &staticPolicy{}

func NewStaticPolicy(logger logr.Logger, topology *topology.CPUTopology, reservedCPUs cpuset.CPUSet) (Policy, error) {
	if topology.NumCPUs < reservedCPUs.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", topology.NumCPUs, reservedCPUs.Size())
	}

	allCPUs := topology.CPUDetails.CPUs()

	policy := &staticPolicy{
		allCPUs:       allCPUs,
		availableCPUs: allCPUs.Difference(reservedCPUs),
		usedCPUs:      cpuset.New(),
		reservedCPUs:  reservedCPUs,
		options: StaticPolicyOptions{
			FullPhysicalCPUsOnly:           false,
			DistributeCPUsAcrossNUMA:       false,
			DistributeCPUsAcrossCores:      false,
			PreferAlignByUncoreCacheOption: true,
		},
		topology:     topology,
		cpuGroupSize: topology.CPUsPerCore(),
		log:          logger,
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

func (p *staticPolicy) Allocate(numCPUs int) (cpuset.CPUSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cpus, err := p.takeByTopology(p.log, p.availableCPUs, numCPUs)
	if err != nil {
		return cpuset.New(), err
	}

	p.usedCPUs = p.usedCPUs.Union(cpus)
	p.availableCPUs = p.availableCPUs.Difference(cpus)

	return cpus, nil
}

func (p *staticPolicy) AllocateOrUpdate(numCPUs int, cpus cpuset.CPUSet) (cpuset.CPUSet, error) {
	if numCPUs <= 0 && cpus.IsEmpty() {
		return cpuset.New(), nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !cpus.IsEmpty() {
		if cpus.Size() > p.allCPUs.Size() {
			return cpuset.New(), fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", cpus.Size(), p.allCPUs.Size())
		}

		pinned := cpus.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Union(pinned)
		p.availableCPUs = p.availableCPUs.Difference(pinned)

		return cpuset.New(), nil
	}

	if p.assignedCPUs+numCPUs > p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size() {
		return cpuset.New(), fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", numCPUs, p.availableCPUs.Size())
	}

	p.assignedCPUs += numCPUs

	return cpuset.New(), nil
}

//nolint:dupl
func (p *staticPolicy) Release(numCPUs int, cpus cpuset.CPUSet) error {
	if numCPUs == 0 && cpus.IsEmpty() {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !cpus.IsEmpty() {
		freed := cpus.Difference(p.reservedCPUs)
		p.usedCPUs = p.usedCPUs.Difference(freed)
		p.availableCPUs = p.availableCPUs.Union(freed)

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

func (p *staticPolicy) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	available := max(0, p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size()-p.assignedCPUs)

	return fmt.Sprintf("Free: %d, Static: [%v], Common: [%v], Reserved: [%v]", available, p.usedCPUs, p.availableCPUs, p.reservedCPUs)
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

		return takeByTopologyNUMADistributed(logger, p.topology, availableCPUs, numCPUs, cpuGroupSize, cpuSortingStrategy)
	}

	return takeByTopologyNUMAPacked(logger, p.topology, availableCPUs, numCPUs, cpuSortingStrategy, p.options.PreferAlignByUncoreCacheOption)
}
