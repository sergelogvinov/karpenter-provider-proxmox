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
	"fmt"
	"strconv"
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
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

		if i, ok := nodeIfaces[params.Bridge]; ok {
			if i.Address4 != "" {
				iface.NodeAddress4 = i.Address4
			}

			if i.Address6 != "" {
				iface.NodeAddress6 = i.Address6
			}

			if i.Gateway4 != "" {
				iface.NodeGateway4 = i.Gateway4
			}

			if i.Gateway6 != "" {
				iface.NodeGateway6 = i.Gateway6
			}
		}

		ipparams := goproxmox.VMCloudInitIPConfig{}
		if ipconfig, ok := ipconfigs[fmt.Sprintf("ipconfig%d", inx)]; ok {
			if err := ipparams.UnmarshalString(ipconfig); err != nil {
				continue
			}

			if ipparams.IPv4 != "" {
				if ipparams.IPv4 == "dhcp" {
					iface.DHCPv4 = true
				} else {
					iface.Address4 = []string{ipparams.IPv4}
				}
			}

			if ipparams.GatewayIPv4 != "" {
				iface.Gateway4 = ipparams.GatewayIPv4
			}

			if ipparams.IPv6 != "" {
				switch ipparams.IPv6 {
				case "dhcp":
					iface.DHCPv6 = true
				case "auto":
					iface.SLAAC = true
					// Some CNI plugins block SLAAC address assignment, so we generate
					// a static IPv6 address from the node address if it's available.
					//
					// if iface.NodeAddress6 != "" {
					// 	ipv6, err := slaac(iface.MacAddr, iface.NodeAddress6)
					// 	if err == nil {
					// 		iface.Address6 = []string{ipv6}
					// 	}

					// 	if iface.Gateway4 == "" {
					// 		ipv6, err := cidrhost(iface.NodeAddress6)
					// 		if err == nil {
					// 			iface.Gateway6 = ipv6
					// 		}
					// 	}
					// }
				default:
					iface.Address6 = []string{ipparams.IPv6}
				}
			}

			if ipparams.GatewayIPv6 != "" {
				iface.Gateway6 = ipparams.GatewayIPv6
			}
		}

		network.Interfaces = append(network.Interfaces, iface)
	}

	return network
}
