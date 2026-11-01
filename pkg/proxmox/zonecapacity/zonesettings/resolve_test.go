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

package zonesettings_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/zonesettings"
)

// intelStatus is a dual-socket Intel node, guessed as two NUMA nodes of 14 cores.
var intelStatus = &nodes.Status{
	CPUInfo: &nodes.CPUInfo{Model: "56 x Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz (2 Socket)", Sockets: 2, Cores: 28, CPUs: 56},
	Memory:  &nodes.Memory{Total: 32 * 1024 * 1024 * 1024},
}

func TestResolve(t *testing.T) {
	testCases := []struct {
		name   string
		status *nodes.Status
		guest  *qemu.Config
		file   *settings.NodeSettings

		expected settings.NodeSettings
		error    string
	}{
		{
			name:   "without a guest or a file the node status guess holds, with the host reserve",
			status: intelStatus,
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 2,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-13,28-41", MemSize: 16 * 1024 * 1024 * 1024},
					1: {CPUs: "14-27,42-55", MemSize: 16 * 1024 * 1024 * 1024},
				},
			},
		},
		{
			name:   "the guest's NUMA nodes replace all the guessed ones",
			status: intelStatus,
			guest: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(8192)},
				},
			},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 2,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-3", MemSize: 8192 * 1024 * 1024},
				},
			},
		},
		{
			name:   "a guest without NUMA nodes is one node of its affinity, not the guess",
			status: intelStatus,
			guest: &qemu.Config{
				Affinity: "0-7",
				Memory:   &qemu.Memory{Current: new(16384)},
			},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 2,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7", MemSize: 16384 * 1024 * 1024},
				},
			},
		},
		{
			name: "a hugepage pool takes no host reserve",
			guest: &qemu.Config{
				Affinity:  "0-1,4-5,2-3,6-7",
				Hugepages: new("1024"),
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(16384)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"1"}, Memory: new(0)},
				},
			},
			expected: settings.NodeSettings{
				HugePageSize: 1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 16384 * 1024 * 1024},
					1: {CPUs: "2-3,6-7"},
				},
			},
		},
		{
			name:   "the guest's CPU shape replaces the guess",
			status: intelStatus,
			guest: &qemu.Config{
				Description: "Karpenter discovery service, threads=1, uncorecaches=4",
				Sockets:     new(1),
				Cores:       new(8),
				Affinity:    "0-7",
			},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      1,
				NumThreads:      1,
				NumUncoreCaches: 4,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7"},
				},
			},
		},
		{
			name:   "a guest without a CPU shape keeps the guess",
			status: intelStatus,
			guest: &qemu.Config{
				Description: "Karpenter discovery service",
				Sockets:     new(1),
				Cores:       new(8),
				Affinity:    "0-7",
			},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 2,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7"},
				},
			},
		},
		{
			name:   "the file's fields win over the guest, its NUMA nodes whole",
			status: intelStatus,
			guest: &qemu.Config{
				Affinity:  "0-7",
				Hugepages: new("1024"),
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(4096)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"1"}, Memory: new(4096)},
				},
			},
			file: &settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      1,
				NumUncoreCaches: 4,
				ReservedCPUs:    []int{0},
				ReservedMemory:  512 * 1024 * 1024,
				HugePageSize:    2 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7", MemSize: 8192 * 1024 * 1024},
				},
			},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      1,
				NumThreads:      1,
				NumUncoreCaches: 4,
				ReservedCPUs:    []int{0},
				ReservedMemory:  512 * 1024 * 1024,
				HugePageSize:    2 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7", MemSize: 8192 * 1024 * 1024},
				},
			},
		},
		{
			name:   "a field the file leaves unset is kept",
			status: intelStatus,
			file:   &settings.NodeSettings{NumUncoreCaches: 4},
			expected: settings.NodeSettings{
				NumCores:        28,
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 4,
				ReservedMemory:  1024 * 1024 * 1024,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-13,28-41", MemSize: 16 * 1024 * 1024 * 1024},
					1: {CPUs: "14-27,42-55", MemSize: 16 * 1024 * 1024 * 1024},
				},
			},
		},
		{
			name:   "a guest that won't decode fails the zone",
			status: intelStatus,
			guest:  &qemu.Config{Affinity: "x"},
			error:  `failed to read the node-capacity guest: failed to parse CPU affinity part "x": strconv.Atoi: parsing "x": invalid syntax`,
		},
		{
			name:   "an incomplete node status fails the zone",
			status: &nodes.Status{CPUInfo: &nodes.CPUInfo{Model: "Intel"}},
			error:  "guessing from the node status: incomplete node status",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := zonesettings.Resolve(tc.status, tc.guest, tc.file)
			if tc.error != "" {
				assert.ErrorContains(t, err, tc.error)

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}
