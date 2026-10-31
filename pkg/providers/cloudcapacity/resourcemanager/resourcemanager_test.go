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

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
)

// TestNodeSettingsFromGuest covers the merge of the node-capacity guest into the node
// settings; nodecapacity.Decode covers reading the guest.
func TestNodeSettingsFromGuest(t *testing.T) {
	testCases := []struct { //nolint:dupl
		name         string
		cfg          *qemu.Config
		nodeSettings *settings.NodeSettings

		expected *settings.NodeSettings
		error    error
	}{
		{
			name: "the guest's NUMA nodes replace those the node status gave",
			cfg: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(8192)},
				},
			},
			nodeSettings: &settings.NodeSettings{
				ReservedMemory: 1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7"},
					1: {CPUs: "8-15"},
				},
			},
			expected: &settings.NodeSettings{
				ReservedMemory: 1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-3", MemSize: 8192 * 1024 * 1024},
					1: {CPUs: "8-15"},
				},
			},
		},
		{
			name: "a guest without NUMA nodes is one node of its affinity",
			cfg: &qemu.Config{
				Affinity: "0-7",
				Memory:   &qemu.Memory{Current: new(16384)},
			},
			nodeSettings: &settings.NodeSettings{},
			expected: &settings.NodeSettings{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7", MemSize: 16384 * 1024 * 1024},
				},
			},
		},
		{
			name: "a hugepage pool takes no host reserve",
			cfg: &qemu.Config{
				Affinity:  "0-1,4-5,2-3,6-7",
				Hugepages: new("1024"),
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(16384)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"1"}, Memory: new(0)},
				},
			},
			nodeSettings: &settings.NodeSettings{ReservedMemory: 1024 * 1024 * 1024},
			expected: &settings.NodeSettings{
				HugePageSize: 1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-1,4-5",
						MemSize: 16384 * 1024 * 1024,
					},
					1: {
						CPUs: "2-3,6-7",
					},
				},
			},
		},
		{
			name: "a guest without a hugepage pool clears a stale page size",
			cfg: &qemu.Config{
				Affinity: "0-7",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-7"}, HostNodes: []string{"0"}, Memory: new(16384)},
				},
			},
			nodeSettings: &settings.NodeSettings{HugePageSize: 1024 * 1024 * 1024, ReservedMemory: 1024 * 1024 * 1024},
			expected: &settings.NodeSettings{
				ReservedMemory: 1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-7",
						MemSize: 16384 * 1024 * 1024,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := nodeSettingsFromGuest(tc.cfg, tc.nodeSettings)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, tc.nodeSettings)
		})
	}
}
