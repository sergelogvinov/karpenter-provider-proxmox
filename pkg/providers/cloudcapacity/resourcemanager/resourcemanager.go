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
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	vmresources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources/vm"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/nodesettings"

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

	opts := options.FromContext(ctx)
	if opts == nil {
		return nil, fmt.Errorf("missing options in context")
	}

	var (
		sysTopology *topology.Topology
		err         error
	)

	manager := &resourceManager{
		cl:   cl,
		zone: zone,
		log:  log,
	}

	manager.nodeSettings, err = nodeSettingsFromCluster(ctx, cl, zone)
	if err != nil {
		return nil, fmt.Errorf("failed to get node settings from VM: %w", err)
	}

	if name := opts.NodeSettingFilePath; name != "" {
		setting, err := settings.LoadNodeSettingsFromFile(name, region, zone)
		if err != nil {
			return nil, err
		}

		if setting != nil {
			if setting.NumSockets != 0 {
				manager.nodeSettings.NumSockets = setting.NumSockets
			}

			if setting.NumThreads != 0 {
				manager.nodeSettings.NumThreads = setting.NumThreads
			}

			if setting.NumUncoreCaches != 0 {
				manager.nodeSettings.NumUncoreCaches = setting.NumUncoreCaches
			}

			if len(setting.ReservedCPUs) != 0 {
				manager.nodeSettings.ReservedCPUs = setting.ReservedCPUs
			}

			if setting.ReservedMemory != 0 {
				manager.nodeSettings.ReservedMemory = setting.ReservedMemory
			}

			if len(setting.NUMANodes) != 0 {
				manager.nodeSettings.NUMANodes = setting.NUMANodes
			}

			log.V(1).Info("Loaded node settings from file", "file", name, "settings", manager.nodeSettings)
		}
	}

	sysTopology, err = topology.DiscoverFromSettings(&manager.nodeSettings)
	if err != nil {
		return nil, fmt.Errorf("failed to discover topology from settings for node %s: %w", manager.zone, err)
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

func nodeSettingsFromCluster(ctx context.Context, cl *goproxmox.APIClient, zone string) (settings.NodeSettings, error) {
	nodeSettings := settings.NodeSettings{
		ReservedMemory: 1024 * 1024 * 1024, // 1GiB
	}

	n, err := cl.Client.Node(ctx, zone)
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get node %s: %w", zone, err)
	}

	st, err := nodesettings.GetNodeSettingByNode(n)
	if err != nil {
		return nodeSettings, fmt.Errorf("getting node settings: %w", err)
	}

	if st != nil {
		nodeSettings = *st
		nodeSettings.ReservedMemory = 1024 * 1024 * 1024 // 1GiB
	}

	vmr, err := cl.GetVMByFilter(ctx, func(v *proxmox.ClusterResource) (bool, error) {
		return v.Node == zone && v.Name == "node-capacity" && slices.Contains(strings.Split(v.Tags, ";"), "karpenter"), nil
	})
	if err != nil && !errors.Is(err, goproxmox.ErrVirtualMachineNotFound) {
		return nodeSettings, fmt.Errorf("failed to get VM by filter for node settings: %w", err)
	}

	if vmr == nil {
		return nodeSettings, nil
	}

	vm, err := cl.GetVMConfig(ctx, int(vmr.VMID))
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get VM config for node settings: %w", err)
	}

	opt, err := vmresources.GetResourceFromVM(vm)
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get resources from VM config for node settings: %w", err)
	}

	nodeSettings.NUMANodes = make(settings.NUMANodes, len(opt.NUMANodes))

	for nodeID, numaNode := range opt.NUMANodes {
		numaInfo := settings.NUMAInfo{
			CPUs:    numaNode.CPUs.String(),
			MemSize: numaNode.Memory * 1024 * 1024, // Convert from MiB to bytes
		}
		nodeSettings.NUMANodes[nodeID] = numaInfo
	}

	// If no NUMA nodes were found but we have CPU affinity set, create a single NUMA node
	if len(nodeSettings.NUMANodes) == 0 && !opt.CPUSet.IsEmpty() {
		nodeSettings.NUMANodes[0] = settings.NUMAInfo{
			CPUs:    opt.CPUSet.String(),
			MemSize: opt.Memory,
		}
	}

	return nodeSettings, nil
}
