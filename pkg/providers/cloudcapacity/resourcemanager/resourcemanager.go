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

	"github.com/go-logr/logr"
	proxmox "github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
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
	log  logr.Logger

	nodeSettings     NodeSettings
	nodeCPUPolicy    cpumanager.Policy
	nodeMemoryPolicy memmanager.Policy
}

var _ ResourceManager = &resourceManager{}

func NewResourceManager(ctx context.Context, cl *goproxmox.APIClient, region, zone string) (ResourceManager, error) {
	log := log.FromContext(ctx).WithName("ResourceManager").WithValues("node", zone)

	manager := &resourceManager{
		cl:   cl,
		zone: zone,
		log:  log,
		nodeSettings: NodeSettings{
			// By default reserve 1 GiB memory for system use
			ReservedMemory: 1024 * 1024 * 1024, // 1GiB
		},
	}

	opts := options.FromContext(ctx)
	if opts == nil {
		return nil, fmt.Errorf("missing options in context")
	}

	if name := options.FromContext(ctx).NodeSettingFilePath; name != "" {
		setting, err := loadNodeSettingsFromFile(name, region, zone)
		if err != nil {
			return nil, err
		}

		if setting != nil {
			manager.nodeSettings = *setting

			log.V(1).Info("Loaded node settings from file", "file", name, "settings", manager.nodeSettings)
		}
	}

	n, err := cl.Client.Node(ctx, manager.zone)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", manager.zone, err)
	}

	nodeCPUTopology, err := topology.Discover(&n.CPUInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to discover CPU topology for node %s: %w", manager.zone, err)
	}

	cpus := cpuset.New(manager.nodeSettings.ReservedCPUs...)

	switch opts.NodePolicy { //nolint:gocritic
	default:
		manager.nodeCPUPolicy, err = cpumanager.NewSimplePolicy(nodeCPUTopology, cpus)
		if err != nil {
			return nil, fmt.Errorf("failed to create simple policy for node %s: %w", manager.zone, err)
		}
	}

	manager.nodeMemoryPolicy, err = memmanager.NewSimplePolicy(n.Memory.Total, manager.nodeSettings.ReservedMemory)
	if err != nil {
		return nil, fmt.Errorf("failed to create memory policy for node %s: %w", manager.zone, err)
	}

	log.V(1).Info("Created resource manager", "node", manager.zone,
		"cpuStatus", manager.nodeCPUPolicy.Status(),
		"memoryStatus", manager.nodeMemoryPolicy.Status(),
		"settings", manager.nodeSettings,
		"policy", opts.NodePolicy,
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

	r.log.V(1).Info("Allocated resources", "vmid", vm.VMID, "status", r.Status(), "cpus", r.nodeCPUPolicy.Status())

	return nil
}

// AllocateOrUpdate implements ResourceManager.
func (r *resourceManager) AllocateOrUpdate(vm *proxmox.VirtualMachine) (err error) {
	if vm.CPUs <= 0 || vm.MaxMem == 0 || vm.VMID == 0 {
		return fmt.Errorf("cannot allocate resources")
	}

	cpus := cpuset.New()

	if vm.VirtualMachineConfig != nil && vm.VirtualMachineConfig.Affinity != "" {
		cpus, err = cpuset.Parse(vm.VirtualMachineConfig.Affinity)
		if err != nil {
			return fmt.Errorf("failed to parse existing CPU affinity for VM %d: %w", vm.VMID, err)
		}
	}

	cpus, err = r.nodeCPUPolicy.AllocateOrUpdate(vm.CPUs, cpus)
	if err != nil {
		return err
	}

	err = r.nodeMemoryPolicy.AllocateOrUpdate(vm.MaxMem)
	if err != nil {
		r.nodeCPUPolicy.Release(vm.CPUs, cpus)

		return err
	}

	r.log.V(1).Info("Allocated/Updated resources", "vmid", vm.VMID, "status", r.Status(), "cpus", r.nodeCPUPolicy.Status())

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

	r.log.V(1).Info("Released resources", "vmid", vm.VMID, "status", r.Status(), "cpus", r.nodeCPUPolicy.Status())

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
