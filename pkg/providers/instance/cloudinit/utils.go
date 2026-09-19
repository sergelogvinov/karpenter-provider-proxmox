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
	"sort"
	"strconv"
	"strings"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
)

const (
	IPv4DHCP  = "dhcp"
	IPv6DHCP  = "dhcp"
	IPv6SLAAC = "auto"
)

// GetNetworkConfigFromVirtualMachineConfig derives cloud-init network
// config from a QEMU guest's configuration.
func GetNetworkConfigFromVirtualMachineConfig(cfg *qemu.Config, nodeIfaces map[string]cloudcapacity.NetworkIfaceInfo) NetworkConfig {
	network := NetworkConfig{}

	if cfg.Nameserver != "" {
		network.NameServers = strings.Split(cfg.Nameserver, " ")
	}

	if cfg.SearchDomain != "" {
		network.SearchDomains = strings.Split(cfg.SearchDomain, " ")
	}

	if len(cfg.Net) == 0 {
		return network
	}

	netIdx := make([]int, 0, len(cfg.Net))
	for idx := range cfg.Net {
		netIdx = append(netIdx, idx)
	}

	sort.Ints(netIdx)

	for _, idx := range netIdx {
		net := cfg.Net[idx]

		iface := InterfaceConfig{
			Name:    fmt.Sprintf("eth%d", idx),
			MacAddr: net.MACAddr,
		}

		if net.MTU != nil {
			iface.MTU = uint32(*net.MTU)
		}

		if i, ok := nodeIfaces[net.Bridge]; ok {
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

		if ipconfig, ok := cfg.IPConfig[idx]; ok {
			iface.Gateway4 = ipconfig.GatewayIPv4
			if ipconfig.IPv4 != "" {
				if ipconfig.IPv4 == IPv4DHCP {
					iface.DHCPv4 = true
				} else {
					iface.Address4 = []string{ipconfig.IPv4}
				}
			}

			iface.Gateway6 = ipconfig.GatewayIPv6
			if ipconfig.IPv6 != "" {
				switch ipconfig.IPv6 {
				case IPv6DHCP:
					iface.DHCPv6 = true
				case IPv6SLAAC:
					iface.SLAAC = true
				default:
					iface.Address6 = []string{ipconfig.IPv6}
				}
			}
		}

		network.Interfaces = append(network.Interfaces, iface)
	}

	return network
}

// SetNetworkConfig writes networkConfig's nameservers/search domains and
// per-interface cloud-init IP configuration (ipconfigN) onto the guest,
// for Proxmox's native cloud-init drive to pick up on its next
// regeneration (see instanceNetworkSetup).
func SetNetworkConfig(ctx context.Context, px *proxmoxrest.Client, zone string, vmID int, networkConfig NetworkConfig) error {
	if len(networkConfig.Interfaces) == 0 {
		return fmt.Errorf("no network interfaces found")
	}

	cfg := &qemu.Config{
		IPConfig: make(map[int]qemu.IPConfig, len(networkConfig.Interfaces)),
	}

	if len(networkConfig.NameServers) > 0 {
		cfg.Nameserver = strings.Join(networkConfig.NameServers, " ")
	}

	if len(networkConfig.SearchDomains) > 0 {
		cfg.SearchDomain = strings.Join(networkConfig.SearchDomains, " ")
	}

	for _, iface := range networkConfig.Interfaces {
		idx, err := strconv.Atoi(strings.TrimPrefix(iface.Name, "eth"))
		if err != nil {
			return fmt.Errorf("failed to parse interface index from name %q: %w", iface.Name, err)
		}

		ipconfig := qemu.IPConfig{
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

		cfg.IPConfig[idx] = ipconfig
	}

	return px.Nodes(zone).Qemu().UpdateConfig(ctx, vmID, cfg)
}
