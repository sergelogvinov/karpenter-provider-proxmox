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

package nodesettings

import (
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
)

func TestNodeSettingsAMDEPYC(t *testing.T) {
	testCases := []struct {
		name     string
		node     *proxmox.Node
		settings *settings.NodeSettings
		error    error
	}{
		{
			name: "AMD EPYC 9454P 48-Core Processor",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "96 x AMD EPYC 9454P 48-Core Processor (1 Socket)",
					Sockets: 1,
					Cores:   48,
					CPUs:    96,
				},
				Memory: proxmox.Memory{
					Total: 256 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      2,
				NumUncoreCaches: 8,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-11,48-59",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					1: {
						CPUs:    "12-23,60-71",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					2: {
						CPUs:    "24-35,72-83",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					3: {
						CPUs:    "36-47,84-95",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
				},
			},
		},
		{
			name: "AMD EPYC 9554 64-Core Processor",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "128 x AMD EPYC 9554 64-Core Processor (1 Socket)",
					Sockets: 1,
					Cores:   64,
					CPUs:    128,
				},
				Memory: proxmox.Memory{
					Total: 256 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      2,
				NumUncoreCaches: 8,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-15,64-79",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					1: {
						CPUs:    "16-31,80-95",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					2: {
						CPUs:    "32-47,96-111",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					3: {
						CPUs:    "48-63,112-127",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
				},
			},
		},
		{
			// https://www.spec.org/cpu2017/results/res2026q1/cpu2017-20251217-50889.pdf
			name: "AMD EPYC 9355 32-Core Processor",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "128 x AMD EPYC 9355 32-Core Processor (2 Socket)",
					Sockets: 2,
					Cores:   64,
					CPUs:    128,
				},
				Memory: proxmox.Memory{
					Total: 512 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 8,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-7,64-71",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					1: {
						CPUs:    "8-15,72-79",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					2: {
						CPUs:    "16-23,80-87",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					3: {
						CPUs:    "24-31,88-95",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					4: {
						CPUs:    "32-39,96-103",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					5: {
						CPUs:    "40-47,104-111",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					6: {
						CPUs:    "48-55,112-119",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
					7: {
						CPUs:    "56-63,120-127",
						MemSize: 64 * 1024 * 1024 * 1024,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error

			settings, err := nodeSettingsAMDEPYC(tc.node)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.settings, settings)
		})
	}
}

func TestNodeSettingsAMD(t *testing.T) {
	testCases := []struct {
		name     string
		node     *proxmox.Node
		settings *settings.NodeSettings
		error    error
	}{
		{
			name: "AMD Ryzen 7 PRO 8700GE",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "16 x AMD Ryzen 7 PRO 8700GE w/ Radeon 780M Graphics (1 Socket)",
					Sockets: 1,
					Cores:   8,
					CPUs:    16,
				},
				Memory: proxmox.Memory{
					Total: 32 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets: 1,
				NumThreads: 2,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-15",
						MemSize: 32 * 1024 * 1024 * 1024,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error

			settings, err := nodeSettingsAMD(tc.node)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.settings, settings)
		})
	}
}

func TestNodeSettingsIntel(t *testing.T) {
	testCases := []struct {
		name     string
		node     *proxmox.Node
		settings *settings.NodeSettings
		error    error
	}{
		{
			name: "Intel i7-8700",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "12 x Intel(R) Core(TM) i7-8700 CPU @ 3.20GHz (1 Socket)",
					Sockets: 1,
					Cores:   6,
					CPUs:    12,
				},
				Memory: proxmox.Memory{
					Total: 32 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets:      1,
				NumThreads:      2,
				NumUncoreCaches: 1,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-11",
						MemSize: 32 * 1024 * 1024 * 1024,
					},
				},
			},
		},
		{
			name: "Intel Xeon E5-2690 v4 dual socket",
			node: &proxmox.Node{
				CPUInfo: proxmox.CPUInfo{
					Model:   "56 x Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz (2 Socket)",
					Sockets: 2,
					Cores:   28,
					CPUs:    56,
				},
				Memory: proxmox.Memory{
					Total: 32 * 1024 * 1024 * 1024,
				},
			},
			settings: &settings.NodeSettings{
				NumSockets:      2,
				NumThreads:      2,
				NumUncoreCaches: 2,
				NUMANodes: settings.NUMANodes{
					0: {
						CPUs:    "0-13,28-41",
						MemSize: 16 * 1024 * 1024 * 1024,
					},
					1: {
						CPUs:    "14-27,42-55",
						MemSize: 16 * 1024 * 1024 * 1024,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error

			settings, err := nodeSettingsIntel(tc.node)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.settings, settings)
		})
	}
}
