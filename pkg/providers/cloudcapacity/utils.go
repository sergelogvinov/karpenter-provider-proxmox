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
	"fmt"
	"net"
	"strconv"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/network"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager"
	vmresources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources/vm"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

func getNodeCapacity(ctx context.Context, cl *proxmoxrest.Client, region string, r *cluster.Resource) (NodeCapacityInfo, error) {
	resourceManager, err := resourcemanager.NewResourceManager(ctx, cl, region, r.Node)
	if err != nil {
		return NodeCapacityInfo{}, fmt.Errorf("failed to create resource manager for node %s in region %s: %w", r.Node, region, err)
	}

	info := NodeCapacityInfo{
		Name:            r.Node,
		Region:          region,
		CPULoad:         int(r.CPU * 100),
		ResourceManager: resourceManager,
	}

	err = info.updateNodeCapacity(ctx, cl)
	if err != nil {
		return NodeCapacityInfo{}, fmt.Errorf("failed to get allocatable resources for node %s in region %s: %w", r.Node, region, err)
	}

	return info, nil
}

func (i *NodeCapacityInfo) updateNodeCapacity(ctx context.Context, cl *proxmoxrest.Client) error {
	log := log.FromContext(ctx).WithName("updateNodeCapacity()")

	vms, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
		Type:      cluster.ResourceTypeVM,
		GuestType: "qemu",
		Node:      i.Name,
		Match: func(r *cluster.Resource) (bool, error) {
			return r.Status == "running", nil
		},
	})
	if err != nil {
		return fmt.Errorf("cannot list vms for node %s: %w", i.Name, err)
	}

	for idx := range vms {
		vmr := vms[idx]

		cfg, err := cl.Nodes(vmr.Node).Qemu().Config(ctx, vmr.VMID, nil)
		if err != nil {
			return fmt.Errorf("failed to get VM %d config for node %s in region %s: %w", vmr.VMID, i.Name, i.Region, err)
		}

		opt, err := vmresources.GetResourceFromVMConfig(&vmr, cfg)
		if err != nil {
			return fmt.Errorf("failed to generate resource request for VM %d: %w", vmr.VMID, err)
		}

		err = i.ResourceManager.AllocateOrUpdate(opt)
		if err != nil {
			log.Error(err, "Failed to allocate resources for VM", "vmID", vmr.VMID)
		}
	}

	return nil
}

func getNodeNetwork(ctx context.Context, cl *proxmoxrest.Client, region string, r *cluster.Resource) (NodeNetworkIfaceInfo, error) {
	networks, err := cl.Nodes(r.Node).Network().List(ctx, network.TypeAnyBridge)
	if err != nil {
		return NodeNetworkIfaceInfo{}, fmt.Errorf("failed to get network interfaces for node %s in region %s: %w", r.Node, region, err)
	}

	ifaces := map[string]NetworkIfaceInfo{}

	for _, iface := range networks {
		if !iface.Active {
			continue
		}

		mtu := 1500
		if iface.MTU != 0 {
			mtu = iface.MTU
		}

		ifaces[iface.Iface] = NetworkIfaceInfo{
			Address4: cidrString(iface.Address, iface.Netmask),
			Address6: cidrString6(iface.Address6, iface.Netmask6),
			Gateway4: iface.Gateway,
			Gateway6: iface.Gateway6,
			MTU:      uint32(mtu),
		}
	}

	return NodeNetworkIfaceInfo{
		Name:   r.Node,
		Region: region,
		Ifaces: ifaces,
	}, nil
}

// cidrString combines an IPv4 address with its network mask (either a
// dotted-decimal mask or an already-numeric prefix length, as Proxmox
// reports either depending on the interface's configuration) into CIDR
// notation. Proxmox's network_config response has no combined field for
// this, unlike the deprecated luthermonson client's derived "cidr" field.
func cidrString(address, netmask string) string {
	if address == "" {
		return ""
	}

	if netmask == "" {
		return address
	}

	if prefix, err := strconv.Atoi(netmask); err == nil {
		return fmt.Sprintf("%s/%d", address, prefix)
	}

	mask := net.ParseIP(netmask).To4()
	if mask == nil {
		return address
	}

	prefix, _ := net.IPMask(mask).Size()

	return fmt.Sprintf("%s/%d", address, prefix)
}

// cidrString6 combines an IPv6 address with its prefix length into CIDR
// notation; see cidrString for why this isn't returned as a single field.
func cidrString6(address string, prefixLen int) string {
	if address == "" {
		return ""
	}

	return fmt.Sprintf("%s/%d", address, prefixLen)
}
