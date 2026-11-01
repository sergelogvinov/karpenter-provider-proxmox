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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/zonesettings"
)

const gib = 1024 * 1024 * 1024

func status(model string, sockets, cores, cpus int, memory int64) *nodes.Status {
	return &nodes.Status{
		CPUInfo: &nodes.CPUInfo{Model: model, Sockets: sockets, Cores: cores, CPUs: cpus},
		Memory:  &nodes.Memory{Total: memory},
	}
}

// TestResolveGuess covers the guess from the node's CPU model, the layer a zone
// without a node-capacity guest or a node settings file is left with.
func TestResolveGuess(t *testing.T) {
	testCases := []struct {
		name     string
		status   *nodes.Status
		expected settings.NodeSettings
	}{
		{
			name:   "AMD EPYC 9454P is four NUMA nodes of 12 cores, a 6-core CCX",
			status: status("96 x AMD EPYC 9454P 48-Core Processor (1 Socket)", 1, 48, 96, 256*gib),
			expected: settings.NodeSettings{
				NumCores: 48, NumSockets: 1, NumThreads: 2, NumUncoreCaches: 8, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-11,48-59", MemSize: 64 * gib},
					1: {CPUs: "12-23,60-71", MemSize: 64 * gib},
					2: {CPUs: "24-35,72-83", MemSize: 64 * gib},
					3: {CPUs: "36-47,84-95", MemSize: 64 * gib},
				},
			},
		},
		{
			name:   "AMD EPYC 9554 is four NUMA nodes of 16 cores, an 8-core CCX",
			status: status("128 x AMD EPYC 9554 64-Core Processor (1 Socket)", 1, 64, 128, 256*gib),
			expected: settings.NodeSettings{
				NumCores: 64, NumSockets: 1, NumThreads: 2, NumUncoreCaches: 8, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-15,64-79", MemSize: 64 * gib},
					1: {CPUs: "16-31,80-95", MemSize: 64 * gib},
					2: {CPUs: "32-47,96-111", MemSize: 64 * gib},
					3: {CPUs: "48-63,112-127", MemSize: 64 * gib},
				},
			},
		},
		{
			// https://www.spec.org/cpu2017/results/res2026q1/cpu2017-20251217-50889.pdf
			name:   "dual AMD EPYC 9355 is four NUMA nodes per socket, a 4-core CCX",
			status: status("128 x AMD EPYC 9355 32-Core Processor (2 Socket)", 2, 64, 128, 512*gib),
			expected: settings.NodeSettings{
				NumCores: 64, NumSockets: 2, NumThreads: 2, NumUncoreCaches: 16, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7,64-71", MemSize: 64 * gib},
					1: {CPUs: "8-15,72-79", MemSize: 64 * gib},
					2: {CPUs: "16-23,80-87", MemSize: 64 * gib},
					3: {CPUs: "24-31,88-95", MemSize: 64 * gib},
					4: {CPUs: "32-39,96-103", MemSize: 64 * gib},
					5: {CPUs: "40-47,104-111", MemSize: 64 * gib},
					6: {CPUs: "48-55,112-119", MemSize: 64 * gib},
					7: {CPUs: "56-63,120-127", MemSize: 64 * gib},
				},
			},
		},
		{
			name:   "AMD EPYC 7763, a third generation, is one NUMA node per socket",
			status: status("128 x AMD EPYC 7763 64-Core Processor (1 Socket)", 1, 64, 128, 256*gib),
			expected: settings.NodeSettings{
				NumCores: 64, NumSockets: 1, NumThreads: 2, NumUncoreCaches: 8, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-127", MemSize: 256 * gib},
				},
			},
		},
		{
			name:   "an AMD desktop CPU is one NUMA node per socket with no known uncore caches",
			status: status("16 x AMD Ryzen 7 PRO 8700GE w/ Radeon 780M Graphics (1 Socket)", 1, 8, 16, 32*gib),
			expected: settings.NodeSettings{
				NumCores: 8, NumSockets: 1, NumThreads: 2, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-15", MemSize: 32 * gib},
				},
			},
		},
		{
			name:   "an Intel CPU is one NUMA node and one uncore cache per socket",
			status: status("12 x Intel(R) Core(TM) i7-8700 CPU @ 3.20GHz (1 Socket)", 1, 6, 12, 32*gib),
			expected: settings.NodeSettings{
				NumCores: 6, NumSockets: 1, NumThreads: 2, NumUncoreCaches: 1, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-11", MemSize: 32 * gib},
				},
			},
		},
		{
			name:   "dual Intel Xeon is two NUMA nodes, each a socket's cores and their threads",
			status: status("56 x Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz (2 Socket)", 2, 28, 56, 32*gib),
			expected: settings.NodeSettings{
				NumCores: 28, NumSockets: 2, NumThreads: 2, NumUncoreCaches: 2, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-13,28-41", MemSize: 16 * gib},
					1: {CPUs: "14-27,42-55", MemSize: 16 * gib},
				},
			},
		},
		{
			name:   "a CPU without threads lists only its cores",
			status: status("8 x Intel(R) Xeon(R) CPU E5-2620 v4 @ 2.10GHz (1 Socket)", 1, 8, 8, 16*gib),
			expected: settings.NodeSettings{
				NumCores: 8, NumSockets: 1, NumThreads: 1, NumUncoreCaches: 1, ReservedMemory: gib,
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-7", MemSize: 16 * gib},
				},
			},
		},
		{
			name:     "an unknown CPU vendor gives no guess",
			status:   status("8 x ARM Neoverse-N1 (1 Socket)", 1, 8, 8, 16*gib),
			expected: settings.NodeSettings{ReservedMemory: gib},
		},
		{
			name:     "an EPYC model without a model number gives no guess",
			status:   status("64 x AMD EPYC Processor (1 Socket)", 1, 32, 64, 128*gib),
			expected: settings.NodeSettings{ReservedMemory: gib},
		},
		{
			name:     "no node status gives no guess",
			expected: settings.NodeSettings{ReservedMemory: gib},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := zonesettings.Resolve(tc.status, nil, nil)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}
