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

package goproxmox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	goproxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmox"

	"k8s.io/utils/ptr"
)

func TestVMCloudInitIPConfig_UnmarshalString(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name     string
		template string
		ipconfig goproxmox.VMCloudInitIPConfig
	}{
		{
			name:     "empty",
			template: "",
			ipconfig: goproxmox.VMCloudInitIPConfig{},
		},
		{
			name:     "ipv4-only",
			template: "ip=1.2.3.4,gw=1.2.3.1",
			ipconfig: goproxmox.VMCloudInitIPConfig{
				GatewayIPv4: "1.2.3.1",
				IPv4:        "1.2.3.4",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := goproxmox.VMCloudInitIPConfig{}

			err := res.UnmarshalString(tt.template)
			assert.NoError(err)
			assert.Equal(tt.ipconfig, res)
		})
	}
}

func TestVMNetworkDevice_UnmarshalString(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name     string
		template string
		iface    goproxmox.VMNetworkDevice
	}{
		{
			name:     "empty",
			template: "",
			iface:    goproxmox.VMNetworkDevice{},
		},
		{
			name:     "virtio",
			template: "virtio=32:90:AC:10:00:91,bridge=vmbr0,firewall=1,mtu=1500,queues=8",
			iface: goproxmox.VMNetworkDevice{
				Virtio:   "32:90:AC:10:00:91",
				Bridge:   "vmbr0",
				Firewall: goproxmox.NewIntOrBool(true),
				MTU:      ptr.To(1500),
				Queues:   ptr.To(8),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := goproxmox.VMNetworkDevice{}

			err := res.UnmarshalString(tt.template)
			assert.NoError(err)
			assert.Equal(tt.iface, res)
		})
	}
}

func TestVMNetworkDevice_ToString(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name  string
		iface goproxmox.VMNetworkDevice
		res   string
	}{
		{
			name:  "empty",
			iface: goproxmox.VMNetworkDevice{},
			res:   "",
		},
		{
			name: "virtio",
			iface: goproxmox.VMNetworkDevice{
				Virtio:   "32:90:AC:10:00:91",
				Bridge:   "vmbr0",
				Firewall: goproxmox.NewIntOrBool(true),
				MTU:      ptr.To(1500),
				Queues:   ptr.To(8),
				Trunks:   []int{1, 2},
			},
			res: "virtio=32:90:AC:10:00:91,bridge=vmbr0,firewall=1,mtu=1500,queues=8,trunks=1;2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.iface.ToString()

			assert.NoError(err)
			assert.Equal(tt.res, res)
		})
	}
}
