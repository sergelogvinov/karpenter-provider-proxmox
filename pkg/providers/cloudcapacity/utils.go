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

package cloudcapacity

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	proxmox "github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func getNodeCapacity(ctx context.Context, cl *goproxmox.APIClient, region string, r *proxmox.ClusterResource) (NodeCapacityInfo, error) {
	resourceManager, err := resourcemanager.NewResourceManager(ctx, cl, region, r.Node)
	if err != nil {
		return NodeCapacityInfo{}, fmt.Errorf("Failed to create resource manager for node %s in region %s: %w", r.Node, region, err)
	}

	info := NodeCapacityInfo{
		Name:            r.Node,
		Region:          region,
		CPULoad:         int(r.CPU * 100),
		ResourceManager: resourceManager,
	}

	err = info.updateNodeCapacity(ctx, cl)
	if err != nil {
		return NodeCapacityInfo{}, fmt.Errorf("Failed to get allocatable resources for node %s in region %s: %w", r.Node, region, err)
	}

	return info, nil
}

func (i *NodeCapacityInfo) updateNodeCapacity(ctx context.Context, cl *goproxmox.APIClient) error {
	vms, err := cl.GetVMsByFilter(ctx, func(vm *proxmox.ClusterResource) (bool, error) {
		return vm.Node == i.Name && vm.Status == "running", nil
	})
	if err != nil && !errors.Is(err, goproxmox.ErrVirtualMachineNotFound) {
		return fmt.Errorf("cannot list vms for node %s: %w", i.Name, err)
	}

	for _, vmr := range vms {
		vm, err := cl.GetVMConfig(ctx, int(vmr.VMID))
		if err != nil {
			return fmt.Errorf("Failed to get VM %d config for node %s in region %s: %w", vmr.VMID, i.Name, i.Region, err)
		}

		err = i.ResourceManager.AllocateOrUpdate(vm)
		if err != nil {
			return fmt.Errorf("Failed to allocate resources for VM %d on node %s in region %s: %w", vmr.VMID, i.Name, i.Region, err)
		}
	}

	cpu := i.ResourceManager.AvailableCPUs()
	mem := i.ResourceManager.AvailableMemory()

	i.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", cpu)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", mem)),
	}

	return nil
}

func getNodeNetwork(ctx context.Context, cl *goproxmox.APIClient, region string, r *proxmox.ClusterResource) (NodeNetworkIfaceInfo, error) {
	node := (&proxmox.Node{}).New(cl.Client, r.Node)
	networks, err := node.Networks(ctx, "any_bridge")
	if err != nil {
		return NodeNetworkIfaceInfo{}, fmt.Errorf("Failed to get network interfaces for node %s in region %s: %w", r.Node, region, err)
	}

	ifaces := map[string]NetworkIfaceInfo{}

	for _, net := range networks {
		if net.Active == 0 {
			continue
		}

		mtu := 1500
		if net.MTU != "" {
			if mtu, err = strconv.Atoi(net.MTU); err != nil {
				return NodeNetworkIfaceInfo{}, fmt.Errorf("Failed to parse MTU for node %s in region %s: %w", r.Node, region, err)
			}
		}

		ifaces[net.Iface] = NetworkIfaceInfo{
			Address4: net.CIDR,
			Address6: net.CIDR6,
			Gateway4: net.Gateway,
			Gateway6: net.Gateway6,
			MTU:      uint32(mtu),
		}
	}

	return NodeNetworkIfaceInfo{
		Name:   r.Node,
		Region: region,
		Ifaces: ifaces,
	}, nil
}
