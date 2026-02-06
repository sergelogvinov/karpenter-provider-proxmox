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
	cputopology "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
)

var (
	MemTopoUncoreDualSocketNoSMT16G = &cputopology.MemTopology{
		TotalMemory: 16384 * 1024 * 1024,
		NUMANodes: map[int]uint64{
			0: 8192 * 1024 * 1024,
			1: 8192 * 1024 * 1024,
		},
	}

	CPUTopoUncoreSingleSocketSMT = &cputopology.CPUTopology{
		NumCPUs:        16,
		NumSockets:     1,
		NumCores:       8,
		NumUncoreCache: 4,
		NumNUMANodes:   2,
		CPUDetails: map[int]cputopology.CPUInfo{
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
)
