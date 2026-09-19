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

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	utilsip "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/ip"
)

// instanceNetworkSetup pushes IPAM-allocated addresses into the guest's
// Proxmox-native cloud-init config (ipconfigN/nameserver/searchdomain) and
// triggers a regeneration if the guest actually has a native cloud-init
// drive attached. This is independent of, and unrelated to, this
// project's own custom-ISO metadata path (attachCloudInitISO) — some
// instance templates ship with Proxmox's native cloud-init drive already
// configured (e.g. for network bootstrap before the custom ISO's userdata
// takes over), and this keeps that drive's IP config in sync either way.
func (p *DefaultProvider) instanceNetworkSetup(
	ctx context.Context,
	region string,
	zone string,
	vmID int,
) error {
	px, err := p.cluster.Get(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox client with region name %s: %v", region, err)
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmID, nil)
	if err != nil {
		return fmt.Errorf("failed to get vm config for vm %d in region %s: %v", vmID, region, err)
	}

	ifaces := map[string]cloudcapacity.NetworkIfaceInfo{}

	net := p.cloudCapacityProvider.GetNetwork(region, zone)
	if net != nil {
		ifaces = net.Ifaces
	}

	networkValues := cloudinit.GetNetworkConfigFromVirtualMachineConfig(cfg, ifaces)
	if err = p.generateNetworkIPs(&networkValues); err != nil {
		return fmt.Errorf("failed to generate network IPs: %v", err)
	}

	if err = cloudinit.SetNetworkConfig(ctx, px, zone, vmID, networkValues); err != nil {
		return fmt.Errorf("failed to update network config: %v", err)
	}

	if hasCloudInitDrive(cfg) {
		if err = px.Nodes(zone).Qemu().CloudInitUpdate(ctx, vmID); err != nil {
			return fmt.Errorf("failed to regenerate cloudinit iso for vm %d in region %s: %v", vmID, region, err)
		}
	}

	return nil
}

// hasCloudInitDrive reports whether the guest has a Proxmox-native
// cloud-init drive attached on its IDE bus (a volume whose path contains
// "cloudinit", e.g. "local-lvm:vm-100-cloudinit") — matching the old
// client's substring check over VirtualMachineConfig.MergeIDEs, which
// only ever checked the IDE bus.
func hasCloudInitDrive(cfg *qemu.Config) bool {
	for _, drive := range cfg.IDE {
		if strings.Contains(drive.File, "cloudinit") {
			return true
		}
	}

	return false
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
