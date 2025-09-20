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

package instance

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	utilsip "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/ip"
)

func (p *DefaultProvider) instanceNetworkSetup(
	ctx context.Context,
	region string,
	zone string,
	vmID int,
) error {
	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	vm, err := px.GetVMConfig(ctx, vmID)
	if err != nil {
		return fmt.Errorf("failed to get vm config for vm %d in region %s: %v", vmID, region, err)
	}

	ifaces := map[string]cloudcapacity.NetworkIfaceInfo{}

	net := p.cloudCapacityProvider.GetNetwork(region, zone)
	if net != nil {
		ifaces = net.Ifaces
	}

	networkValues := cloudinit.GetNetworkConfigFromVirtualMachineConfig(vm.VirtualMachineConfig, ifaces)
	if err = p.generateNetworkIPs(&networkValues); err != nil {
		return fmt.Errorf("failed to generate network IPs: %v", err)
	}

	if err = cloudinit.SetNetworkConfig(ctx, vm, networkValues); err != nil {
		return fmt.Errorf("failed to update network config: %v", err)
	}

	vm.VirtualMachineConfig.MergeIDEs()

	for _, iso := range vm.VirtualMachineConfig.IDEs {
		if strings.Contains(iso, "cloudinit") {
			if err = px.RegenerateVMCloudInit(ctx, vm.Node, vmID); err != nil {
				return fmt.Errorf("failed to regenerate cloudinit iso for vm %d in region %s: %v", vmID, region, err)
			}

			break
		}
	}

	return nil
}

func (p *DefaultProvider) generateNetworkIPs(networkConfig *cloudinit.NetworkConfig) error {
	for i := range networkConfig.Interfaces {
		iface := &networkConfig.Interfaces[i]

		if len(iface.Address4) > 0 {
			addresses := []string{}

			for _, addr := range iface.Address4 {
				ipv4, ipnet, err := net.ParseCIDR(addr)
				if err != nil {
					return err
				}

				if ipv4.Equal(ipnet.IP) {
					err = p.nodeIpamProvider.AllocateOrOccupyCIDR(addr)
					if err != nil {
						return err
					}

					subnet := ipnet.String()

					if iface.NodeAddress4 != "" {
						nodeip, nodenet, err := net.ParseCIDR(iface.NodeAddress4)
						if err != nil {
							return err
						}

						if ipnet.Contains(nodenet.IP) {
							nodenet.IP = nodeip
							subnet = nodenet.String()

							if iface.Gateway4 == "" {
								iface.Gateway4 = nodeip.String()
							}
						}
					}

					ip, err := p.nodeIpamProvider.OccupyIP(subnet)
					if err != nil {
						return err
					}

					ipnet.IP = ip
					addresses = append(addresses, ipnet.String())
				}
			}

			iface.Address4 = addresses
		}

		if len(iface.Address6) > 0 {
			addresses := []string{}

			for _, addr := range iface.Address6 {
				ipv6, ipnet, err := net.ParseCIDR(addr)
				if err != nil {
					return err
				}

				if ipv6.Equal(ipnet.IP) {
					ipv6, err := utilsip.Slaac(iface.MacAddr, addr)
					if err == nil {
						addresses = append(addresses, ipv6)
					}
				}
			}

			iface.Address6 = addresses
		}
	}

	return nil
}
