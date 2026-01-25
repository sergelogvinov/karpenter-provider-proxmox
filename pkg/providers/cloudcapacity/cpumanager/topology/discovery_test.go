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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"

	proxmox "github.com/luthermonson/go-proxmox"
)

var (
	topoUncoreSingleSocketMultiNuma = &CPUTopology{
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

	topoUncoreDualSocketNoSMT = &CPUTopology{
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

	topoUncoreSingleSocketSMT = &CPUTopology{
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

func Test_Discover(t *testing.T) {

	testCases := []struct {
		name        string
		machineInfo proxmox.CPUInfo
		want        *CPUTopology
		wantErr     bool
	}{
		{
			name: "OneSocketHT",
			machineInfo: proxmox.CPUInfo{
				Model:   "8 x Intel(R) Core(TM) i7-6700 CPU",
				CPUs:    8,
				Cores:   4,
				Sockets: 1,
			},
			want: &CPUTopology{
				NumCPUs:        8,
				NumSockets:     1,
				NumCores:       4,
				NumNUMANodes:   1,
				NumUncoreCache: 1,
				CPUDetails: map[int]CPUInfo{
					0: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					1: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					2: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					3: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					4: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					5: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					6: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					7: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
				},
			},
			wantErr: false,
		},
		{
			// Topology from AMD EPYC 9454P 48-Core Processor
			// NPS=4
			name: "UncoreOneSocketSMT",
			machineInfo: proxmox.CPUInfo{
				Model:   "96 x AMD EPYC 9454P 48-Core Processor (1 Socket)",
				CPUs:    96,
				Cores:   48,
				Sockets: 1,
			},
			want: &CPUTopology{
				NumCPUs:        96,
				NumSockets:     1,
				NumCores:       48,
				NumNUMANodes:   4,
				NumUncoreCache: 8,
				CPUDetails: map[int]CPUInfo{
					0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					8:  {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					9:  {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					10: {CoreID: 10, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					11: {CoreID: 11, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					12: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					13: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					14: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					15: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					16: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					17: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					18: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					19: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					20: {CoreID: 20, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					21: {CoreID: 21, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					22: {CoreID: 22, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					23: {CoreID: 23, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					24: {CoreID: 24, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					25: {CoreID: 25, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					26: {CoreID: 26, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					27: {CoreID: 27, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					28: {CoreID: 28, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					29: {CoreID: 29, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					30: {CoreID: 30, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					31: {CoreID: 31, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					32: {CoreID: 32, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					33: {CoreID: 33, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					34: {CoreID: 34, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					35: {CoreID: 35, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					36: {CoreID: 36, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					37: {CoreID: 37, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					38: {CoreID: 38, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					39: {CoreID: 39, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					40: {CoreID: 40, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					41: {CoreID: 41, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					42: {CoreID: 42, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					43: {CoreID: 43, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					44: {CoreID: 44, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					45: {CoreID: 45, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					46: {CoreID: 46, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					47: {CoreID: 47, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					48: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					49: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					50: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					51: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					52: {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					53: {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					54: {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					55: {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					56: {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					57: {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					58: {CoreID: 10, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					59: {CoreID: 11, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					60: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					61: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					62: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					63: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					64: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					65: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 2},
					66: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					67: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					68: {CoreID: 20, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					69: {CoreID: 21, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					70: {CoreID: 22, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					71: {CoreID: 23, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					72: {CoreID: 24, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					73: {CoreID: 25, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					74: {CoreID: 26, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					75: {CoreID: 27, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					76: {CoreID: 28, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					77: {CoreID: 29, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					78: {CoreID: 30, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					79: {CoreID: 31, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					80: {CoreID: 32, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					81: {CoreID: 33, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					82: {CoreID: 34, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					83: {CoreID: 35, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					84: {CoreID: 36, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					85: {CoreID: 37, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					86: {CoreID: 38, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					87: {CoreID: 39, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					88: {CoreID: 40, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					89: {CoreID: 41, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					90: {CoreID: 42, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					91: {CoreID: 43, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					92: {CoreID: 44, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					93: {CoreID: 45, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					94: {CoreID: 46, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					95: {CoreID: 47, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
				},
			},
			wantErr: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Discover(&proxmox.Node{
				CPUInfo: tc.machineInfo,
			})
			if err != nil {
				if tc.wantErr {
					t.Logf("Discover() expected error = %v", err)
				} else {
					t.Errorf("Discover() error = %v, wantErr %v", err, tc.wantErr)
				}
				return
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("Discover() = %v, want %v diff=%s", got, tc.want, diff)
			}
		})
	}
}

func TestDiscoverFromSettings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		settings *settings.NodeSettings
		topo     *CPUTopology
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
						CPUs: "0-15",
					},
				},
			},
			topo: topoUncoreSingleSocketSMT,
		},
		{
			name: "single socket machine with multiple numa nodes",
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumUncoreCaches: 4,
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						CPUs: "0-7",
					},
					1: settings.NUMAInfo{
						CPUs: "8-15",
					},
				},
			},
			topo: topoUncoreSingleSocketMultiNuma,
		},
		{
			name: "dual socket machine",
			settings: &settings.NodeSettings{
				NumSockets:      2,
				NumUncoreCaches: 4,
				NUMANodes: settings.NUMANodes{
					0: settings.NUMAInfo{
						CPUs: "0-7",
					},
					1: settings.NUMAInfo{
						CPUs: "8-15",
					},
				},
			},
			topo: topoUncoreDualSocketNoSMT,
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
