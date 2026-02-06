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

package vmresources_test

import (
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	resources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	vmresources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources/vm"

	"k8s.io/utils/cpuset"
)

func TestGetResourceFromVM(t *testing.T) {
	t.Parallel()

	testCases := []struct { //nolint:dupl
		name     string
		vm       *proxmox.VirtualMachine
		expected *resources.VMResources
		error    error
	}{
		{
			name: "dynamic VM",
			vm: &proxmox.VirtualMachine{
				VMID:                 100,
				CPUs:                 4,
				MaxMem:               8192 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: cpuset.New(),
				Memory: 8192 * 1024 * 1024,
			},
		},
		{
			name: "static VM",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   4,
				MaxMem: 8192 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-3",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-3")),
				Memory: 8192 * 1024 * 1024,
			},
		},
		{
			name: "static VM with numa binding",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   4,
				MaxMem: 8 * 1024 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-1,8-9",
					Numa:     1,
					Numa0:    "cpus=0-3,hostnodes=0,memory=8192",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-1,8-9")),
				Memory: 8192 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-3")),
					},
				},
			},
		},
		{
			name: "static VM with numa binding 2",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   4,
				MaxMem: 8 * 1024 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-1,8-9",
					Numa:     1,
					Numa0:    "cpus=0-3,hostnodes=1,memory=8192",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-1,8-9")),
				Memory: 8192 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					1: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-3")),
					},
				},
			},
		},
		{
			name: "static VM with cross numa binding",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   4,
				MaxMem: 16 * 1024 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-1,8-9",
					Numa:     1,
					Numa0:    "cpus=0-1,hostnodes=0,memory=8192,policy=bind",
					Numa1:    "cpus=2-3,hostnodes=1,memory=8192,policy=bind",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-1,8-9")),
				Memory: 16384 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-1")),
						Policy: "bind",
					},
					1: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("2-3")),
						Policy: "bind",
					},
				},
			},
		},
		{
			name: "static VM with multi cpu numa binding",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   8,
				MaxMem: 16 * 1024 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-3,8-11",
					Numa:     1,
					Numa0:    "cpus=0-3,hostnodes=0-1,memory=8192,policy=bind",
					Numa1:    "cpus=4-7,hostnodes=2-3,memory=8192,policy=bind",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   8,
				CPUSet: lo.Must(cpuset.Parse("0-3,8-11")),
				Memory: 16384 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("0-1")),
						Policy: "bind",
					},
					1: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("2-3")),
						Policy: "bind",
					},
					2: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("4-5")),
						Policy: "bind",
					},
					3: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("6-7")),
						Policy: "bind",
					},
				},
			},
		},
		{
			name: "static VM with multi cpu cross numa binding",
			vm: &proxmox.VirtualMachine{
				VMID:   100,
				CPUs:   8,
				MaxMem: 16 * 1024 * 1024 * 1024,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Affinity: "0-3,8-11",
					Numa:     1,
					Numa0:    "cpus=0-3,hostnodes=0-1,memory=8192,policy=bind",
					Numa1:    "cpus=4-7,hostnodes=0-1,memory=8192,policy=bind",
				},
			},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   8,
				CPUSet: lo.Must(cpuset.Parse("0-3,8-11")),
				Memory: 16384 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-1,4-5")),
						Policy: "bind",
					},
					1: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("2-3,6-7")),
						Policy: "bind",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := vmresources.GetResourceFromVM(tc.vm)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, req)
		})
	}
}

func TestGenerateVMOptionsFromResources(t *testing.T) {
	t.Parallel()

	testCases := []struct { //nolint:dupl
		name      string
		resources *resources.VMResources
		expected  map[string]any
		error     error
	}{
		{
			name: "dynamic VM",
			resources: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: cpuset.New(),
				Memory: 8192 * 1024 * 1024,
			},
			expected: map[string]any{
				"cores":  4,
				"memory": uint64(8192),
			},
		},
		{
			name: "static VM",
			resources: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-3")),
				Memory: 8192 * 1024 * 1024,
			},
			expected: map[string]any{
				"cores":    4,
				"memory":   uint64(8192),
				"affinity": "0-3",
			},
		},
		{
			name: "static VM with numa binding",
			resources: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: lo.Must(cpuset.Parse("0-1,8-9")),
				Memory: 8192 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					1: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-3")),
						Policy: "bind",
					},
				},
			},
			expected: map[string]any{
				"cores":    4,
				"memory":   uint64(8192),
				"affinity": "0-1,8-9",
				"numa":     1,
				"numa0":    "cpus=0-3,hostnodes=1,memory=8192,policy=bind",
			},
		},
		{
			name: "static VM with multi cpu numa binding",
			resources: &resources.VMResources{
				ID:     100,
				CPUs:   8,
				CPUSet: lo.Must(cpuset.Parse("0-3,8-11")),
				Memory: 16384 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("0-1")),
						Policy: "bind",
					},
					1: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("2-3")),
						Policy: "bind",
					},
					2: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("4-5")),
						Policy: "bind",
					},
					3: {
						Memory: 4096,
						CPUs:   lo.Must(cpuset.Parse("6-7")),
						Policy: "bind",
					},
				},
			},
			expected: map[string]any{
				"cores":    8,
				"memory":   uint64(16384),
				"affinity": "0-3,8-11",
				"numa":     1,
				"numa0":    "cpus=0-1,hostnodes=0,memory=4096,policy=bind",
				"numa1":    "cpus=2-3,hostnodes=1,memory=4096,policy=bind",
				"numa2":    "cpus=4-5,hostnodes=2,memory=4096,policy=bind",
				"numa3":    "cpus=6-7,hostnodes=3,memory=4096,policy=bind",
			},
		},
		{
			name: "static VM with multi cpu cross numa binding",
			resources: &resources.VMResources{
				ID:     100,
				CPUs:   8,
				CPUSet: lo.Must(cpuset.Parse("0-3,8-11")),
				Memory: 16384 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("0-3")),
						Policy: "bind",
					},
					1: {
						Memory: 8192,
						CPUs:   lo.Must(cpuset.Parse("4-7")),
						Policy: "bind",
					},
				},
			},
			expected: map[string]any{
				"cores":    8,
				"memory":   uint64(16384),
				"affinity": "0-3,8-11",
				"numa":     1,
				"numa0":    "cpus=0-3,hostnodes=0,memory=8192,policy=bind",
				"numa1":    "cpus=4-7,hostnodes=1,memory=8192,policy=bind",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := vmresources.GenerateVMOptionsFromResources(tc.resources)
			if tc.error != nil {
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.expected, req)
		})
	}
}
