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

package cloudinit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
)

const (
	IPv4DHCP  = "dhcp"
	IPv6DHCP  = "dhcp"
	IPv6SLAAC = "auto"
)

func GetNetworkConfigFromVirtualMachineConfig(vmc *proxmox.VirtualMachineConfig, nodeIfaces map[string]cloudcapacity.NetworkIfaceInfo) NetworkConfig {
	network := NetworkConfig{}

	if vmc.Nameserver != "" {
		network.NameServers = strings.Split(vmc.Nameserver, " ")
	}

	if vmc.Searchdomain != "" {
		network.SearchDomains = strings.Split(vmc.Searchdomain, " ")
	}

	nets := vmc.MergeNets()
	if len(nets) == 0 {
		return network
	}

	ipconfigs := vmc.MergeIPConfigs()

	for i, net := range nets {
		inx, _ := strconv.Atoi(strings.TrimPrefix(i, "net"))

		params := goproxmox.VMNetworkDevice{}
		if err := params.UnmarshalString(net); err != nil {
			continue
		}

		iface := InterfaceConfig{
			Name:    fmt.Sprintf("eth%d", inx),
			MacAddr: params.Virtio,
		}

		if params.MTU != nil {
			iface.MTU = uint32(*params.MTU)
		}

		if i, ok := nodeIfaces[params.Bridge]; ok {
			iface.NodeAddress4 = i.Address4
			iface.NodeAddress6 = i.Address6
			iface.NodeGateway4 = i.Gateway4
			iface.NodeGateway6 = i.Gateway6

			if iface.MTU == 0 || iface.MTU == 1 {
				iface.MTU = i.MTU
			}
		}

		if iface.MTU == 0 {
			iface.MTU = 1500
		}

		ipparams := goproxmox.VMCloudInitIPConfig{}
		if ipconfig, ok := ipconfigs[fmt.Sprintf("ipconfig%d", inx)]; ok {
			if err := ipparams.UnmarshalString(ipconfig); err != nil {
				continue
			}

			iface.Gateway4 = ipparams.GatewayIPv4
			if ipparams.IPv4 != "" {
				if ipparams.IPv4 == IPv4DHCP {
					iface.DHCPv4 = true
				} else {
					iface.Address4 = []string{ipparams.IPv4}
				}
			}

			iface.Gateway6 = ipparams.GatewayIPv6
			if ipparams.IPv6 != "" {
				switch ipparams.IPv6 {
				case IPv6DHCP:
					iface.DHCPv6 = true
				case IPv6SLAAC:
					iface.SLAAC = true
				default:
					iface.Address6 = []string{ipparams.IPv6}
				}
			}
		}

		network.Interfaces = append(network.Interfaces, iface)
	}

	return network
}

func SetNetworkConfig(ctx context.Context, vm *proxmox.VirtualMachine, networkConfig NetworkConfig) error {
	if len(networkConfig.Interfaces) == 0 {
		return fmt.Errorf("no network interfaces found")
	}

	vmOptions := []proxmox.VirtualMachineOption{}

	if len(networkConfig.NameServers) > 0 {
		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "nameserver", Value: strings.Join(networkConfig.NameServers, " ")})
	}

	if len(networkConfig.SearchDomains) > 0 {
		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "searchdomain", Value: strings.Join(networkConfig.SearchDomains, " ")})
	}

	for _, iface := range networkConfig.Interfaces {
		key := fmt.Sprintf("ipconfig%s", iface.Name[3:])

		ipconfig := goproxmox.VMCloudInitIPConfig{
			GatewayIPv4: iface.Gateway4,
			GatewayIPv6: iface.Gateway6,
		}

		if len(iface.Address4) > 0 {
			ipconfig.IPv4 = iface.Address4[0]
		}

		if len(iface.Address6) > 0 {
			ipconfig.IPv6 = iface.Address6[0]
		}

		if iface.DHCPv4 {
			ipconfig.IPv4 = "dhcp"
		}

		switch {
		case iface.DHCPv6:
			ipconfig.IPv6 = "dhcp"
		case iface.SLAAC:
			ipconfig.IPv6 = "auto"
		}

		val, err := ipconfig.ToString()
		if err != nil {
			return fmt.Errorf("failed to marshal ipconfig for interface %s: %v", iface.Name, err)
		}

		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: key, Value: val})
	}

	if len(vmOptions) > 0 {
		_, err := vm.Config(ctx, vmOptions...)
		if err != nil {
			return err
		}
	}

	return nil
}
