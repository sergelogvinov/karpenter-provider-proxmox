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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
)

func TestDefaultNetworkV1(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name     string
		template string
		network  cloudinit.NetworkConfig
		result   string
	}{
		{
			"NetworkV1-Static",
			cloudinit.DefaultNetworkV1,
			cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:     "eth0",
						MacAddr:  "AA:11:22:33:44:55",
						MTU:      1500,
						DHCPv4:   false,
						DHCPv6:   false,
						Address4: []string{"192.168.1.100"},
						Gateway4: "192.168.1.1",
						Address6: []string{"2000:db8::5"},
						Gateway6: "2000:db8::1",
					},
				},
				NameServers: []string{
					"4.3.2.1",
					"1.2.3.4",
				},
				SearchDomains: []string{
					"example.com",
				},
			},
			`version: 1
config:
- type: physical
  name: eth0
  mac_address: "aa:11:22:33:44:55"
  mtu: 1500
  subnets:
  - type: static
    address: "192.168.1.100"
    gateway: "192.168.1.1"
  - type: static6
    address: "2000:db8::5"
    gateway: "2000:db8::1"
- type: nameserver
  address:
  - "4.3.2.1"
  - "1.2.3.4"
  search:
  - "example.com"
`,
		},
		{
			"NetworkV1-DHCP",
			cloudinit.DefaultNetworkV1,
			cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:     "eth0",
						MacAddr:  "00:11:22:33:44:55",
						MTU:      1500,
						DHCPv4:   true,
						DHCPv6:   true,
						Address4: []string{"192.168.1.100"},
						Gateway4: "192.168.1.1",
						Address6: []string{"2000:db8::5"},
						Gateway6: "2000:db8::1",
					},
				},
				NameServers: []string{
					"4.3.2.1",
					"1.2.3.4",
				},
			},
			`version: 1
config:
- type: physical
  name: eth0
  mac_address: "00:11:22:33:44:55"
  mtu: 1500
  subnets:
  - type: dhcp
  - type: dhcp6
- type: nameserver
  address:
  - "4.3.2.1"
  - "1.2.3.4"
`,
		},
		{
			"NetworkV1-Saac",
			cloudinit.DefaultNetworkV1,
			cloudinit.NetworkConfig{
				Interfaces: []cloudinit.InterfaceConfig{
					{
						Name:    "eth0",
						MacAddr: "00:11:22:33:44:55",
						MTU:     1500,
						DHCPv4:  false,
						DHCPv6:  false,
					},
				},
				NameServers: []string{
					"4.3.2.1",
					"1.2.3.4",
				},
			},
			`version: 1
config:
- type: physical
  name: eth0
  mac_address: "00:11:22:33:44:55"
  mtu: 1500
  subnets:
  - type: ipv6_slaac
- type: nameserver
  address:
  - "4.3.2.1"
  - "1.2.3.4"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := cloudinit.ExecuteTemplate(tt.template, tt.network)
			assert.NoError(err)
			assert.Equal(data, tt.result)
		})
	}
}
