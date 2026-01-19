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

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	cputopology "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager"
	memtopology "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type ResourceManager interface {
	Allocate(*VMResourceOptions) error
	AllocateOrUpdate(*VMResourceOptions) error
	Release(*VMResourceOptions) error

	AvailableCPUs() int
	AvailableMemory() uint64

	Status() string
}

type resourceManager struct {
	cl   *goproxmox.APIClient
	zone string
	log  logr.Logger

	nodeSettings     settings.NodeSettings
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
		nodeSettings: settings.NodeSettings{
			// By default reserve 1 GiB memory for system use
			ReservedMemory: 1024 * 1024 * 1024, // 1GiB
		},
	}

	opts := options.FromContext(ctx)
	if opts == nil {
		return nil, fmt.Errorf("missing options in context")
	}

	var (
		nodeCPUTopology *cputopology.CPUTopology
		nodeMemTopology *memtopology.MemTopology
		err             error
	)

	if name := options.FromContext(ctx).NodeSettingFilePath; name != "" {
		setting, err := settings.LoadNodeSettingsFromFile(name, region, zone)
		if err != nil {
			return nil, err
		}

		if setting != nil {
			manager.nodeSettings = *setting

			log.V(4).Info("Loaded node settings from file", "file", name, "settings", manager.nodeSettings)
		}

		nodeCPUTopology, err = cputopology.DiscoverFromSettings(manager.nodeSettings)
		if err != nil {
			log.Info("failed to discover CPU topology from settings for node", "node", manager.zone, "error", err)
		}

		nodeMemTopology, err = memtopology.DiscoverFromSettings(manager.nodeSettings)
		if err != nil {
			log.Info("failed to discover memory topology from settings for node", "node", manager.zone, "error", err)
		}
	}

	if nodeCPUTopology == nil || nodeMemTopology == nil {
		n, err := cl.Client.Node(ctx, manager.zone)
		if err != nil {
			return nil, fmt.Errorf("failed to get node %s: %w", manager.zone, err)
		}

		if nodeCPUTopology == nil {
			nodeCPUTopology, err = cputopology.Discover(n)
			if err != nil {
				return nil, fmt.Errorf("failed to discover CPU topology for node %s: %w", manager.zone, err)
			}
		}

		if nodeMemTopology == nil {
			nodeMemTopology, err = memtopology.Discover(n)
			if err != nil {
				return nil, fmt.Errorf("failed to discover memory topology for node %s: %w", manager.zone, err)
			}
		}
	}

	switch opts.NodePolicy { //nolint:gocritic
	case string(cpumanager.PolicyStatic):
		manager.nodeCPUPolicy, err = cpumanager.NewStaticPolicy(log, nodeCPUTopology, manager.nodeSettings.ReservedCPUs)
		if err != nil {
			return nil, fmt.Errorf("failed to create static policy for node %s: %w", manager.zone, err)
		}
	default:
		manager.nodeCPUPolicy, err = cpumanager.NewSimplePolicy(nodeCPUTopology, manager.nodeSettings.ReservedCPUs)
		if err != nil {
			return nil, fmt.Errorf("failed to create simple policy for node %s: %w", manager.zone, err)
		}
	}

	manager.nodeMemoryPolicy, err = memmanager.NewSimplePolicy(nodeMemTopology, manager.nodeSettings.ReservedMemory)
	if err != nil {
		return nil, fmt.Errorf("failed to create memory policy for node %s: %w", manager.zone, err)
	}

	log.V(4).Info("Created resource manager",
		"capacityCPU", manager.nodeCPUPolicy.Status(),
		"capacityMem", manager.nodeMemoryPolicy.Status(),
		"settings", manager.nodeSettings,
		"policy", opts.NodePolicy,
	)

	return manager, nil
}

// Allocate implements ResourceManager.
func (r *resourceManager) Allocate(op *VMResourceOptions) (err error) {
	if op == nil || op.CPUs <= 0 || op.MemoryMBytes == 0 {
		return fmt.Errorf("cannot allocate resources, invalid options")
	}

	op.CPUSet, err = r.nodeCPUPolicy.Allocate(op.CPUs)
	if err != nil {
		return err
	}

	err = r.nodeMemoryPolicy.Allocate(op.MemoryMBytes * 1024 * 1024)
	if err != nil {
		r.nodeCPUPolicy.Release(op.CPUs, op.CPUSet)

		return err
	}

	r.log.V(4).Info("Allocated resources", "id", op.ID,
		"availableCapacity", r.Status(),
		"CPUs", op.CPUs,
		"CPUSet", op.CPUSet.String(),
		"memoryMB", op.MemoryMBytes)

	return nil
}

// AllocateOrUpdate implements ResourceManager.
func (r *resourceManager) AllocateOrUpdate(op *VMResourceOptions) error {
	if op == nil || op.CPUs <= 0 || op.MemoryMBytes == 0 || op.ID == 0 {
		return fmt.Errorf("cannot allocate resources, invalid options")
	}

	cpus, err := r.nodeCPUPolicy.AllocateOrUpdate(op.CPUs, op.CPUSet)
	if err != nil {
		return err
	}

	err = r.nodeMemoryPolicy.AllocateOrUpdate(op.MemoryMBytes * 1024 * 1024)
	if err != nil {
		r.nodeCPUPolicy.Release(op.CPUs, cpus)

		return err
	}

	r.log.V(4).Info("Allocated/Updated resources", "id", op.ID, "availableCapacity", r.Status(), "CPUs", op.CPUs, "CPUSet", cpus)

	return nil
}

// Release implements ResourceManager.
func (r *resourceManager) Release(op *VMResourceOptions) (err error) {
	if op == nil || op.CPUs == 0 || op.MemoryMBytes == 0 || op.ID == 0 {
		return nil
	}

	if err := r.nodeMemoryPolicy.Release(op.MemoryMBytes * 1024 * 1024); err != nil {
		return err
	}

	if err := r.nodeCPUPolicy.Release(op.CPUs, op.CPUSet); err != nil {
		return err
	}

	r.log.V(4).Info("Released resources", "id", op.ID, "availableCapacity", r.Status(), "CPUs", op.CPUs, "CPUSet", op.CPUSet.String())

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
	return fmt.Sprintf("CPU: %s, Mem: %s", r.nodeCPUPolicy.Status(), r.nodeMemoryPolicy.Status())
}
