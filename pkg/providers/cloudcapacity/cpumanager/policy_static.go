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
	"sync"

	"github.com/go-logr/logr"
	"github.com/samber/lo"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cloudresources"
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

func NewStaticPolicy(logger logr.Logger, topology *topology.CPUTopology, reserved []int) (Policy, error) {
	if topology == nil {
		return nil, fmt.Errorf("topology must be provided for static cpu policy")
	}

	reservedCPUs := cpuset.New(reserved...)
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

func (p *staticPolicy) Allocate(op *cloudresources.VMResources) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cpus, err := p.takeByTopology(p.log, p.availableCPUs, op.CPUs)
	if err != nil {
		return err
	}

	p.usedCPUs = p.usedCPUs.Union(cpus)
	p.availableCPUs = p.availableCPUs.Difference(cpus)
	op.CPUSet = cpus.Clone()

	CPUinx := 0
	NUMANodes := make(map[int]goproxmox.NUMANodeState, p.topology.CPUDetails.NUMANodes().Size())

	for _, i := range p.topology.CPUDetails.NUMANodes().List() {
		numaCPUs := op.CPUSet.Intersection(p.topology.CPUDetails.CPUsInNUMANodes(i))
		if numaCPUs.Size() > 0 {
			NUMANodes[i] = goproxmox.NUMANodeState{
				CPUs:   lo.Must(cpuset.Parse(fmt.Sprintf("%d-%d", CPUinx, CPUinx+numaCPUs.Size()-1))),
				Policy: "bind",
			}

			CPUinx += numaCPUs.Size()
		}
	}

	if len(NUMANodes) > 0 {
		op.NUMANodes = make(map[int]goproxmox.NUMANodeState, len(NUMANodes))
		maps.Copy(op.NUMANodes, NUMANodes)
	}

	return nil
}

func (p *staticPolicy) AllocateOrUpdate(op *cloudresources.VMResources) error {
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

	if p.assignedCPUs+op.CPUs > p.allCPUs.Size()-p.reservedCPUs.Size()-p.usedCPUs.Size() {
		return fmt.Errorf("not enough CPUs available to satisfy request: requested=%d, available=%d", op.CPUs, p.availableCPUs.Size())
	}

	p.assignedCPUs += op.CPUs

	return nil
}

//nolint:dupl
func (p *staticPolicy) Release(op *cloudresources.VMResources) error {
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
