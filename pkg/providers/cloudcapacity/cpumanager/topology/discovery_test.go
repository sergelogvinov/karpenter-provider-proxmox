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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	"github.com/stretchr/testify/assert"

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
		NumUncoreCache: 2,
		NumNUMANodes:   1,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			8:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			9:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			10: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			11: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			12: {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			13: {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			14: {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
			15: {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
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
			// dual xeon gold 6230
			name: "DualSocketMultiNumaPerSocketHT",
			machineInfo: proxmox.CPUInfo{
				Model:   "Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz",
				CPUs:    80,
				Cores:   40,
				Sockets: 2,
			},
			want: &CPUTopology{
				NumCPUs:        80,
				NumSockets:     2,
				NumCores:       40,
				NumNUMANodes:   4,
				NumUncoreCache: 1,
				CPUDetails: map[int]CPUInfo{
					0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					8:  {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					9:  {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					10: {CoreID: 10, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					11: {CoreID: 11, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					12: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					13: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					14: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					15: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					16: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					17: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					18: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					19: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					20: {CoreID: 20, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					21: {CoreID: 21, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					22: {CoreID: 22, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					23: {CoreID: 23, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					24: {CoreID: 24, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					25: {CoreID: 25, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					26: {CoreID: 26, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					27: {CoreID: 27, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					28: {CoreID: 28, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					29: {CoreID: 29, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					30: {CoreID: 30, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					31: {CoreID: 31, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					32: {CoreID: 32, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					33: {CoreID: 33, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					34: {CoreID: 34, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					35: {CoreID: 35, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					36: {CoreID: 36, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					37: {CoreID: 37, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					38: {CoreID: 38, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					39: {CoreID: 39, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					40: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					41: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					42: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					43: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					44: {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					45: {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					46: {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					47: {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					48: {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					49: {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					50: {CoreID: 10, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					51: {CoreID: 11, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					52: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					53: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					54: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					55: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					56: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					57: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					58: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					59: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 0},
					60: {CoreID: 20, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					61: {CoreID: 21, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					62: {CoreID: 22, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					63: {CoreID: 23, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					64: {CoreID: 24, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					65: {CoreID: 25, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					66: {CoreID: 26, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					67: {CoreID: 27, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					68: {CoreID: 28, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					69: {CoreID: 29, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 0},
					70: {CoreID: 30, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					71: {CoreID: 31, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					72: {CoreID: 32, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					73: {CoreID: 33, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					74: {CoreID: 34, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					75: {CoreID: 35, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					76: {CoreID: 36, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					77: {CoreID: 37, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					78: {CoreID: 38, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
					79: {CoreID: 39, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 0},
				},
			},
			wantErr: false,
		},
		{
			// Topology from AMD EPYC 7452 (Rome) 32-Core Processor
			// 4 cores per LLC
			// Single-socket SMT-disabled
			// NPS=1
			name: "UncoreOneSocketNoSMT",
			machineInfo: proxmox.CPUInfo{
				Model:   "AMD EPYC 7452 (Rome) 32-Core Processor",
				CPUs:    32,
				Cores:   32,
				Sockets: 1,
			},
			want: &CPUTopology{
				NumCPUs:        32,
				NumSockets:     1,
				NumCores:       32,
				NumNUMANodes:   4,
				NumUncoreCache: 8,
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
					16: {CoreID: 16, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					17: {CoreID: 17, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					18: {CoreID: 18, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					19: {CoreID: 19, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 4},
					20: {CoreID: 20, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					21: {CoreID: 21, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					22: {CoreID: 22, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					23: {CoreID: 23, SocketID: 0, NUMANodeID: 2, UncoreCacheID: 5},
					24: {CoreID: 24, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					25: {CoreID: 25, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					26: {CoreID: 26, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					27: {CoreID: 27, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 6},
					28: {CoreID: 28, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					29: {CoreID: 29, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					30: {CoreID: 30, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
					31: {CoreID: 31, SocketID: 0, NUMANodeID: 3, UncoreCacheID: 7},
				},
			},
			wantErr: false,
		},
		{
			// Topology from AMD EPYC 24-Core Processor
			// 4 cores per LLC
			// Dual-socket SMT-enabled
			// NPS=2
			name: "UncoreDualSocketSMT",
			machineInfo: proxmox.CPUInfo{
				Model:   "AMD EPYC 24-Core Processor",
				CPUs:    96,
				Cores:   48,
				Sockets: 2,
			},
			want: &CPUTopology{
				NumCPUs:        96,
				NumSockets:     2,
				NumCores:       48,
				NumNUMANodes:   4,
				NumUncoreCache: 12,
				CPUDetails: map[int]CPUInfo{
					0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					1:  {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					3:  {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					5:  {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					6:  {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					7:  {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					8:  {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					9:  {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					10: {CoreID: 10, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					11: {CoreID: 11, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					12: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					13: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					14: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					15: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					16: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					17: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					18: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					19: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					20: {CoreID: 20, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					21: {CoreID: 21, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					22: {CoreID: 22, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					23: {CoreID: 23, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					24: {CoreID: 24, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					25: {CoreID: 25, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					26: {CoreID: 26, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					27: {CoreID: 27, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					28: {CoreID: 28, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					29: {CoreID: 29, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					30: {CoreID: 30, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					31: {CoreID: 31, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					32: {CoreID: 32, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					33: {CoreID: 33, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					34: {CoreID: 34, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					35: {CoreID: 35, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					36: {CoreID: 36, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					37: {CoreID: 37, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					38: {CoreID: 38, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					39: {CoreID: 39, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					40: {CoreID: 40, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					41: {CoreID: 41, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					42: {CoreID: 42, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					43: {CoreID: 43, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					44: {CoreID: 44, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					45: {CoreID: 45, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					46: {CoreID: 46, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					47: {CoreID: 47, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					48: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					49: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					50: {CoreID: 2, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					51: {CoreID: 3, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
					52: {CoreID: 4, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					53: {CoreID: 5, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					54: {CoreID: 6, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					55: {CoreID: 7, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 1},
					56: {CoreID: 8, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					57: {CoreID: 9, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					58: {CoreID: 10, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					59: {CoreID: 11, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 2},
					60: {CoreID: 12, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					61: {CoreID: 13, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					62: {CoreID: 14, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					63: {CoreID: 15, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 3},
					64: {CoreID: 16, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					65: {CoreID: 17, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					66: {CoreID: 18, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					67: {CoreID: 19, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 4},
					68: {CoreID: 20, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					69: {CoreID: 21, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					70: {CoreID: 22, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					71: {CoreID: 23, SocketID: 0, NUMANodeID: 1, UncoreCacheID: 5},
					72: {CoreID: 24, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					73: {CoreID: 25, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					74: {CoreID: 26, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					75: {CoreID: 27, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 6},
					76: {CoreID: 28, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					77: {CoreID: 29, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					78: {CoreID: 30, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					79: {CoreID: 31, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 7},
					80: {CoreID: 32, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					81: {CoreID: 33, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					82: {CoreID: 34, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					83: {CoreID: 35, SocketID: 1, NUMANodeID: 2, UncoreCacheID: 8},
					84: {CoreID: 36, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					85: {CoreID: 37, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					86: {CoreID: 38, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					87: {CoreID: 39, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 9},
					88: {CoreID: 40, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					89: {CoreID: 41, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					90: {CoreID: 42, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					91: {CoreID: 43, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 10},
					92: {CoreID: 44, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					93: {CoreID: 45, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					94: {CoreID: 46, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
					95: {CoreID: 47, SocketID: 1, NUMANodeID: 3, UncoreCacheID: 11},
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
		settings settings.NodeSettings
		topo     *CPUTopology
		error    error
	}{
		{
			name:  "empty settings",
			error: fmt.Errorf("could not detect cpu topology from incomplete node settings"),
		},
		{
			name: "single socket machine with SMT",
			settings: settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      2,
				NumUncoreCaches: 4,
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
			settings: settings.NodeSettings{
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
			settings: settings.NodeSettings{
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
