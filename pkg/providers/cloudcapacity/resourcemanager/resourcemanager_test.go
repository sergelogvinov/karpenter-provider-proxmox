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

package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
)

var testNodeSettings = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: settings.NUMAInfo{
			CPUs:    "0-7",
			MemSize: 16 * 1024 * 1024 * 1024,
		},
		1: settings.NUMAInfo{
			CPUs:    "8-15",
			MemSize: 16 * 1024 * 1024 * 1024,
		},
	},
	ReservedCPUs:   []int{},
	ReservedMemory: 1024 * 1024 * 1024,
}

func TestNodeSettingsFromVM(t *testing.T) {
	testCases := []struct { //nolint:dupl
		name         string
		vmr          *cluster.Resource
		cfg          *qemu.Config
		nodeSettings *settings.NodeSettings

		expected *settings.NodeSettings
		error    error
	}{
		{
			name: "VM with Affinity and NUMA nodes",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 96, MaxMem: 124 * 4 * 1024 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-11,48-59,12-23,60-71,24-35,72-83,36-47,84-95",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-23"}, HostNodes: []string{"0"}, Memory: new(126976)},
					1: {CPUIDs: []string{"24-47"}, HostNodes: []string{"1"}, Memory: new(126976)},
					2: {CPUIDs: []string{"48-71"}, HostNodes: []string{"2"}, Memory: new(126976)},
					3: {CPUIDs: []string{"72-95"}, HostNodes: []string{"3"}, Memory: new(126976)},
				},
			},
			nodeSettings: &settings.NodeSettings{},
			expected: &settings.NodeSettings{
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-11,48-59",
						MemSize: 126976 * 1024 * 1024,
					},
					1: {
						CPUs:    "12-23,60-71",
						MemSize: 126976 * 1024 * 1024,
					},
					2: {
						CPUs:    "24-35,72-83",
						MemSize: 126976 * 1024 * 1024,
					},
					3: {
						CPUs:    "36-47,84-95",
						MemSize: 126976 * 1024 * 1024,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := nodeSettingsFromVM(tc.vmr, tc.cfg, tc.nodeSettings)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, tc.nodeSettings)
		})
	}
}
