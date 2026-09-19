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

package cloudinit_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
)

func TestGetNetworkConfigFromVirtualMachineConfig(t *testing.T) {
	assert := assert.New(t)

	nodeIfaces := map[string]cloudcapacity.NetworkIfaceInfo{
		"vmbr0": {
			MTU: 9000,
		},
	}

	tests := []struct {
		name     string
		template *qemu.Config
		network  cloudinit.NetworkConfig
	}{
		{
			name:     "empty",
			template: &qemu.Config{},
			network:  cloudinit.NetworkConfig{},
		},
		{
			name: "1-interface-defaults-with-mtu",
			template: &qemu.Config{
				Net: map[int]qemu.Net{
					0: {Model: "virtio", MACAddr: "BC:24:11:CD:B9:41", Bridge: "vmbr0", Firewall: new(true), MTU: new(1), Tag: new(70), Trunks: []string{"70", "100", "200"}},
				},
				IPConfig: map[int]qemu.IPConfig{
					0: {IPv4: "dhcp", IPv6: "auto"},
				},
				Nameserver: "1.1.1.1 2001:4860:4860::8888",
			},
			network: cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:    "eth0",
						MacAddr: "BC:24:11:CD:B9:41",
						DHCPv4:  true,
						SLAAC:   true,
						MTU:     9000,
					},
				},
				NameServers: []string{"1.1.1.1", "2001:4860:4860::8888"},
			},
		},
		{
			name: "1-interface-defaults",
			template: &qemu.Config{
				Net: map[int]qemu.Net{
					0: {Model: "virtio", MACAddr: "BC:24:11:CD:B9:41", Bridge: "vmbr0", Firewall: new(true), Tag: new(70), Trunks: []string{"70", "100", "200"}},
				},
				IPConfig: map[int]qemu.IPConfig{
					0: {IPv4: "dhcp", IPv6: "auto"},
				},
				Nameserver: "1.1.1.1 2001:4860:4860::8888",
			},
			network: cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:    "eth0",
						MacAddr: "BC:24:11:CD:B9:41",
						DHCPv4:  true,
						SLAAC:   true,
						MTU:     9000,
					},
				},
				NameServers: []string{"1.1.1.1", "2001:4860:4860::8888"},
			},
		},
		{
			name: "1-interface-defaults-no-mtu-defined-in-node-iface",
			template: &qemu.Config{
				Net: map[int]qemu.Net{
					0: {Model: "virtio", MACAddr: "BC:24:11:CD:B9:41", Bridge: "vmbr1", Firewall: new(true), Tag: new(70), Trunks: []string{"70", "100", "200"}},
				},
				IPConfig: map[int]qemu.IPConfig{
					0: {IPv4: "dhcp", IPv6: "auto"},
				},
				Nameserver: "1.1.1.1 2001:4860:4860::8888",
			},
			network: cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:    "eth0",
						MacAddr: "BC:24:11:CD:B9:41",
						DHCPv4:  true,
						SLAAC:   true,
						MTU:     1500,
					},
				},
				NameServers: []string{"1.1.1.1", "2001:4860:4860::8888"},
			},
		},
		{
			name: "2-interfaces",
			template: &qemu.Config{
				Net: map[int]qemu.Net{
					0: {Model: "virtio", MACAddr: "BC:24:11:CD:B9:41", Bridge: "vmbr0", Firewall: new(true), MTU: new(1500)},
					1: {Model: "virtio", MACAddr: "BC:24:11:EE:9A:23", Bridge: "vmbr1", Firewall: new(false), MTU: new(1400)},
				},
				IPConfig: map[int]qemu.IPConfig{
					0: {IPv4: "dhcp", IPv6: "auto"},
					1: {IPv4: "1.2.3.4/24"},
				},
				Nameserver:   "1.1.1.1 2001:4860:4860::8888",
				SearchDomain: "example.com",
			},
			network: cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:    "eth0",
						MacAddr: "BC:24:11:CD:B9:41",
						DHCPv4:  true,
						SLAAC:   true,
						MTU:     1500,
					},
					{
						Name:     "eth1",
						MacAddr:  "BC:24:11:EE:9A:23",
						Address4: []string{"1.2.3.4/24"},
						MTU:      1400,
					},
				},
				NameServers:   []string{"1.1.1.1", "2001:4860:4860::8888"},
				SearchDomains: []string{"example.com"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.name), func(t *testing.T) {
			result := cloudinit.GetNetworkConfigFromVirtualMachineConfig(tt.template, nodeIfaces)

			assert.ElementsMatch(tt.network.Interfaces, result.Interfaces)
			assert.Equal(tt.network.NameServers, result.NameServers)
			assert.Equal(tt.network.SearchDomains, result.SearchDomains)
		})
	}
}
