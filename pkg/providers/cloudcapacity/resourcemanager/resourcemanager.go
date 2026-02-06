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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type ResourceManager interface {
	Allocate(*resources.VMResources) error
	AllocateOrUpdate(*resources.VMResources) error
	Release(*resources.VMResources) error

	AvailableCPUs() int
	AvailableMemory() uint64

	Status() string
}

type resourceManager struct {
	cl   *goproxmox.APIClient
	zone string
	log  logr.Logger

	nodeSettings settings.NodeSettings
	nodePolicy   cpumanager.Policy
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
		sysTopology *topology.Topology
		err         error
	)

	if name := opts.NodeSettingFilePath; name != "" {
		setting, err := settings.LoadNodeSettingsFromFile(name, region, zone)
		if err != nil {
			return nil, err
		}

		if setting != nil {
			manager.nodeSettings = *setting

			log.V(4).Info("Loaded node settings from file", "file", name, "settings", manager.nodeSettings)
		}

		sysTopology, err = topology.DiscoverFromSettings(&manager.nodeSettings)
		if err != nil {
			log.Info("failed to discover topology from settings for node", "node", manager.zone, "error", err)
		}
	}

	if sysTopology == nil {
		n, err := cl.Client.Node(ctx, manager.zone)
		if err != nil {
			return nil, fmt.Errorf("failed to get node %s: %w", manager.zone, err)
		}

		sysTopology, err = topology.Discover(n)
		if err != nil {
			return nil, fmt.Errorf("failed to discover topology for node %s: %w", manager.zone, err)
		}
	}

	switch opts.NodePolicy { //nolint:gocritic
	case string(cpumanager.PolicyStatic):
		manager.nodePolicy, err = cpumanager.NewStaticPolicy(log, sysTopology, manager.nodeSettings.ReservedCPUs, manager.nodeSettings.ReservedMemory)
		if err != nil {
			return nil, fmt.Errorf("failed to create static policy for node %s: %w", manager.zone, err)
		}
	default:
		manager.nodePolicy, err = cpumanager.NewSimplePolicy(sysTopology, manager.nodeSettings.ReservedCPUs, manager.nodeSettings.ReservedMemory)
		if err != nil {
			return nil, fmt.Errorf("failed to create simple policy for node %s: %w", manager.zone, err)
		}
	}

	log.V(1).Info("Created resource manager",
		"capacity", manager.nodePolicy.Status(),
		"settings", manager.nodeSettings,
		"policy", opts.NodePolicy,
	)

	return manager, nil
}

// Allocate implements ResourceManager.
func (r *resourceManager) Allocate(op *resources.VMResources) (err error) {
	if op == nil || op.CPUs <= 0 || op.Memory == 0 {
		return fmt.Errorf("cannot allocate resources, invalid resources request")
	}

	err = r.nodePolicy.Allocate(op)
	if err != nil {
		return err
	}

	r.log.V(1).Info("Allocated resources", "id", op.ID,
		"availableCapacity", r.Status(),
		"CPUs", op.CPUs,
		"CPUSet", op.CPUSet.String(),
		"memory", op.Memory/1024/1024,
		"numaNodes", op.NUMANodes,
	)

	return nil
}

// AllocateOrUpdate implements ResourceManager.
func (r *resourceManager) AllocateOrUpdate(op *resources.VMResources) error {
	if op == nil || op.CPUs <= 0 || op.Memory == 0 || op.ID == 0 {
		return fmt.Errorf("cannot allocate resources, invalid resources request")
	}

	err := r.nodePolicy.AllocateOrUpdate(op)
	if err != nil {
		return err
	}

	r.log.V(4).Info("Allocated or updated resources", "id", op.ID,
		"availableCapacity", r.Status(),
		"CPUs", op.CPUs,
		"CPUSet", op.CPUSet.String(),
		"memory", op.Memory/1024/1024)

	return nil
}

// Release implements ResourceManager.
func (r *resourceManager) Release(op *resources.VMResources) (err error) {
	if op == nil || op.CPUs <= 0 || op.Memory == 0 || op.ID == 0 {
		return nil
	}

	if err := r.nodePolicy.Release(op); err != nil {
		return err
	}

	r.log.V(4).Info("Released resources", "id", op.ID, "availableCapacity", r.Status(), "CPUs", op.CPUs, "CPUSet", op.CPUSet.String())

	return nil
}

// AvailableCPUs implements ResourceManager.
func (r *resourceManager) AvailableCPUs() int {
	return r.nodePolicy.AvailableCPUs()
}

// AvailableMemory implements ResourceManager.
func (r *resourceManager) AvailableMemory() uint64 {
	return r.nodePolicy.AvailableMemory()
}

// Status implements ResourceManager.
func (r *resourceManager) Status() string {
	return r.nodePolicy.Status()
}
