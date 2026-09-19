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

package topology

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
)

var (
	topoUncoreSingleSocketMultiNuma = CPUTopology{
		NumCPUs:        16,
		NumSockets:     1,
		NumCores:       16,
		NumUncoreCache: 4,
		NumNUMANodes:   2,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			8:  {CoreID: 8, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
			9:  {CoreID: 9, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
			10: {CoreID: 10, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
			11: {CoreID: 11, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
			12: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
			13: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
			14: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
			15: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
		},
	}

	topoUncoreDualSocketNoSMT = CPUTopology{
		NumCPUs:        16,
		NumSockets:     2,
		NumCores:       16,
		NumUncoreCache: 4,
		NumNUMANodes:   2,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			8:  {CoreID: 8, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 2},
			9:  {CoreID: 9, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 2},
			10: {CoreID: 10, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 2},
			11: {CoreID: 11, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 2},
			12: {CoreID: 12, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 3},
			13: {CoreID: 13, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 3},
			14: {CoreID: 14, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 3},
			15: {CoreID: 15, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 3},
		},
	}

	topoUncoreSingleSocketSMT = CPUTopology{
		NumCPUs:        16,
		NumSockets:     1,
		NumCores:       8,
		NumUncoreCache: 1,
		NumNUMANodes:   1,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			8:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			9:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			10: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			11: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			12: {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			13: {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			14: {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			15: {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
		},
	}
)

func TestDiscoverFromSettings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		settings *settings.NodeSettings
		topo     *Topology
		error    error
	}{
		{
			name:  "empty settings",
			error: fmt.Errorf("could not detect cpu topology from incomplete node settings"),
		},
		{
			name: "single socket machine with SMT",
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      2,
				NumUncoreCaches: 1,
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						CPUs:    "0-15",
						MemSize: 16 * 1024 * 1024 * 1024,
					},
				},
			},
			topo: &Topology{
				CPUTopology: topoUncoreSingleSocketSMT,
				MemTopology: MemTopology{
					TotalMemory: 16 * 1024 * 1024 * 1024,
					NUMANodes:   map[int]uint64{0: 16 * 1024 * 1024 * 1024},
				},
			},
		},
		{
			name: "single socket machine with multiple numa nodes",
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumUncoreCaches: 4,
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						CPUs:    "0-7",
						MemSize: 8 * 1024 * 1024 * 1024,
					},
					1: settings.NUMAInfo{
						CPUs:    "8-15",
						MemSize: 8 * 1024 * 1024 * 1024,
					},
				},
			},
			topo: &Topology{
				CPUTopology: topoUncoreSingleSocketMultiNuma,
				MemTopology: MemTopology{
					TotalMemory: 16 * 1024 * 1024 * 1024,
					NUMANodes:   map[int]uint64{0: 8 * 1024 * 1024 * 1024, 1: 8 * 1024 * 1024 * 1024},
				},
			},
		},
		{
			name: "dual socket machine",
			settings: &settings.NodeSettings{
				NumSockets:      2,
				NumUncoreCaches: 4,
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						CPUs:    "0-7",
						MemSize: 8 * 1024 * 1024 * 1024,
					},
					1: settings.NUMAInfo{
						CPUs:    "8-15",
						MemSize: 8 * 1024 * 1024 * 1024,
					},
				},
			},
			topo: &Topology{
				CPUTopology: topoUncoreDualSocketNoSMT,
				MemTopology: MemTopology{
					TotalMemory: 16 * 1024 * 1024 * 1024,
					NUMANodes:   map[int]uint64{0: 8 * 1024 * 1024 * 1024, 1: 8 * 1024 * 1024 * 1024},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topo, err := DiscoverFromSettings(tc.settings)
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
