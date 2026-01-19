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

package topology_test

import (
	"fmt"
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
)

func TestDiscover(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		machineInfo *proxmox.Node
		topo        *topology.MemTopology
		error       error
	}{
		{
			name:        "nil machine info",
			machineInfo: nil,
			error:       fmt.Errorf("cannot discover memory topology from nil node info"),
		},
		{
			name: "single socket machine",
			machineInfo: &proxmox.Node{
				Memory: proxmox.Memory{
					Total: 65536,
				},
				CPUInfo: proxmox.CPUInfo{
					Sockets: 1,
				},
			},
			topo: &topology.MemTopology{
				TotalMemory: 65536,
				NUMANodes: map[int]uint64{
					0: 65536,
				},
			},
		},
		{
			name: "dual socket machine",
			machineInfo: &proxmox.Node{
				Memory: proxmox.Memory{
					Total: 128000,
				},
				CPUInfo: proxmox.CPUInfo{
					Sockets: 2,
				},
			},
			topo: &topology.MemTopology{
				TotalMemory: 128000,
				NUMANodes: map[int]uint64{
					0: 64000,
					1: 64000,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topo, err := topology.Discover(tc.machineInfo)
			if tc.error != nil {
				assert.Error(t, err)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.topo, topo)
		})
	}
}

func TestDiscoverFromSettings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		settings settings.NodeSettings
		topo     *topology.MemTopology
		error    error
	}{
		{
			name:  "empty settings",
			error: fmt.Errorf("could not detect memory topology from incomplete node settings"),
		},
		{
			name: "single socket machine",
			settings: settings.NodeSettings{
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						MemSize: 65536,
					},
				},
			},
			topo: &topology.MemTopology{
				TotalMemory: 65536,
				NUMANodes: map[int]uint64{
					0: 65536,
				},
			},
		},
		{
			name: "dual socket machine",
			settings: settings.NodeSettings{
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						MemSize: 64000,
					},
					1: settings.NUMAInfo{
						MemSize: 64000,
					},
				},
			},
			topo: &topology.MemTopology{
				TotalMemory: 128000,
				NUMANodes: map[int]uint64{
					0: 64000,
					1: 64000,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topo, err := topology.DiscoverFromSettings(tc.settings)
			if tc.error != nil {
				assert.Error(t, err)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.topo, topo)
		})
	}
}
