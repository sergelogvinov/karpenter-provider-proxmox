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

package resourcemanager

import (
	"context"
	"fmt"

	proxmox "github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager"

	"k8s.io/utils/cpuset"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type ResourceManager interface {
	Allocate(*proxmox.VirtualMachine) error
	AllocateOrUpdate(*proxmox.VirtualMachine) error
	Release(*proxmox.VirtualMachine) error

	AvailableCPUs() int
	AvailableMemory() uint64

	Status() string
}

type resourceManager struct {
	cl   *goproxmox.APIClient
	zone string

	nodeCPUPolicy    cpumanager.Policy
	nodeMemoryPolicy memmanager.Policy
}

var _ ResourceManager = &resourceManager{}

func NewResourceManager(ctx context.Context, cl *goproxmox.APIClient, zone string) (ResourceManager, error) {
	manager := &resourceManager{
		cl:   cl,
		zone: zone,
	}

	n, err := cl.Client.Node(ctx, manager.zone)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", manager.zone, err)
	}

	manager.nodeCPUPolicy, err = cpumanager.NewSimplePolicy(n.CPUInfo.CPUs)
	if err != nil {
		return nil, fmt.Errorf("failed to create CPU policy for node %s: %w", manager.zone, err)
	}

	manager.nodeMemoryPolicy, err = memmanager.NewSimplePolicy(n.Memory.Total)
	if err != nil {
		return nil, fmt.Errorf("failed to create memory policy for node %s: %w", manager.zone, err)
	}

	log.FromContext(ctx).V(1).Info("Created resource manager", "node", manager.zone,
		"cpuStatus", manager.nodeCPUPolicy.Status(),
		"memoryStatus", manager.nodeMemoryPolicy.Status(),
	)

	return manager, nil
}

// Allocate implements ResourceManager.
func (r *resourceManager) Allocate(vm *proxmox.VirtualMachine) error {
	if vm.CPUs <= 0 || vm.MaxMem == 0 || vm.VMID == 0 {
		return fmt.Errorf("cannot allocate resources")
	}

	cpus, err := r.nodeCPUPolicy.Allocate(vm.CPUs)
	if err != nil {
		return err
	}

	err = r.nodeMemoryPolicy.Allocate(vm.MaxMem)
	if err != nil {
		r.nodeCPUPolicy.Release(vm.CPUs, cpus)

		return err
	}

	return nil
}

// AllocateOrUpdate implements ResourceManager.
func (r *resourceManager) AllocateOrUpdate(vm *proxmox.VirtualMachine) error {
	if vm.CPUs <= 0 || vm.MaxMem == 0 || vm.VMID == 0 {
		return fmt.Errorf("cannot allocate resources")
	}

	cpus, err := r.nodeCPUPolicy.AllocateOrUpdate(vm.CPUs, cpuset.New())
	if err != nil {
		return err
	}

	err = r.nodeMemoryPolicy.AllocateOrUpdate(vm.MaxMem)
	if err != nil {
		r.nodeCPUPolicy.Release(vm.CPUs, cpus)

		return err
	}

	return nil
}

// Release implements ResourceManager.
func (r *resourceManager) Release(vm *proxmox.VirtualMachine) error {
	if vm.CPUs == 0 || vm.MaxMem == 0 || vm.VMID == 0 {
		return nil
	}

	if err := r.nodeMemoryPolicy.Release(vm.MaxMem); err != nil {
		return err
	}

	if err := r.nodeCPUPolicy.Release(vm.CPUs, cpuset.New()); err != nil {
		return err
	}

	return nil
}

// AvailableCPUs implements ResourceManager.
func (r *resourceManager) AvailableCPUs() int {
	return r.nodeCPUPolicy.AvailableCPUs()
}

// AvailableMemory implements ResourceManager.
func (r *resourceManager) AvailableMemory() uint64 {
	return r.nodeMemoryPolicy.AvailableMemory()
}

// Status implements ResourceManager.
func (r *resourceManager) Status() string {
	return fmt.Sprintf("CPUs: %d, Mem: %dM", r.AvailableCPUs(), r.AvailableMemory()/1024/1024)
}
