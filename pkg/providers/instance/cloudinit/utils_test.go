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

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"

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
		template *proxmox.VirtualMachineConfig
		network  cloudinit.NetworkConfig
	}{
		{
			name:     "empty",
			template: &proxmox.VirtualMachineConfig{},
			network:  cloudinit.NetworkConfig{},
		},
		{
			name: "1-interface-defaults-with-mtu",
			template: &proxmox.VirtualMachineConfig{
				Net0:       "virtio=BC:24:11:CD:B9:41,bridge=vmbr0,firewall=1,mtu=1,tag=70,trunks=70,100,200",
				IPConfig0:  "ip=dhcp,ip6=auto",
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
			template: &proxmox.VirtualMachineConfig{
				Net0:       "virtio=BC:24:11:CD:B9:41,bridge=vmbr0,firewall=1,tag=70,trunks=70,100,200",
				IPConfig0:  "ip=dhcp,ip6=auto",
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
			template: &proxmox.VirtualMachineConfig{
				Net0:       "virtio=BC:24:11:CD:B9:41,bridge=vmbr1,firewall=1,tag=70,trunks=70,100,200",
				IPConfig0:  "ip=dhcp,ip6=auto",
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
			template: &proxmox.VirtualMachineConfig{
				Net0:         "virtio=BC:24:11:CD:B9:41,bridge=vmbr0,firewall=1,mtu=1500",
				Net1:         "virtio=BC:24:11:EE:9A:23,bridge=vmbr1,firewall=0,mtu=1400",
				IPConfig0:    "ip=dhcp,ip6=auto",
				IPConfig1:    "ip=1.2.3.4/24",
				Nameserver:   "1.1.1.1 2001:4860:4860::8888",
				Searchdomain: "example.com",
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
			assert.Equal(tt.network, result)
		})
	}
}
