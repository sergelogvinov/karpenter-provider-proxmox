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
)

func GetNetworkConfigFromVirtualMachineConfig(vmc *proxmox.VirtualMachineConfig) NetworkConfig {
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
