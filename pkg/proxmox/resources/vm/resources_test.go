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

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	resources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	vmresources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources/vm"

	"k8s.io/utils/cpuset"
)

func TestGetResourceFromVMConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct { //nolint:dupl
		name     string
		vmr      *cluster.Resource
		cfg      *qemu.Config
		expected *resources.VMResources
		error    error
	}{
		{
			name: "dynamic VM",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 4, MaxMem: 8192 * 1024},
			cfg:  &qemu.Config{},
			expected: &resources.VMResources{
				ID:     100,
				CPUs:   4,
				CPUSet: cpuset.New(),
				Memory: 8192 * 1024,
			},
		},
		{
			name: "static VM",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 4, MaxMem: 8192 * 1024},
			cfg:  &qemu.Config{Affinity: "0-3"},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     4,
				CPUSet:   lo.Must(cpuset.Parse("0-3")),
				Affinity: "0-3",
				Memory:   8192 * 1024,
			},
		},
		{
			name: "static VM with numa binding",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 4, MaxMem: 8 * 1024 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-1,8-9",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(8192)},
				},
			},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     4,
				CPUSet:   lo.Must(cpuset.Parse("0-1,8-9")),
				Affinity: "0-1,8-9",
				Memory:   8 * 1024 * 1024,
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   "0-3",
					},
				},
			},
		},
		{
			name: "static VM with numa binding 2",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 4, MaxMem: 8192 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-1,8-9",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"1"}, Memory: new(8192)},
				},
			},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     4,
				CPUSet:   lo.Must(cpuset.Parse("0-1,8-9")),
				Affinity: "0-1,8-9",
				Memory:   8192 * 1024,
				NUMANodes: map[int]resources.NUMANodeState{
					1: {
						Memory: 8192,
						CPUs:   "0-3",
					},
				},
			},
		},
		{
			name: "static VM with cross numa binding",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 96, MaxMem: 124 * 4 * 1024 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-11,48-59,12-23,60-71,24-35,72-83,36-47,84-95",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-23"}, HostNodes: []string{"0"}, Memory: new(126976)},
					1: {CPUIDs: []string{"24-47"}, HostNodes: []string{"1"}, Memory: new(126976)},
					2: {CPUIDs: []string{"48-71"}, HostNodes: []string{"2"}, Memory: new(126976)},
					3: {CPUIDs: []string{"72-95"}, HostNodes: []string{"3"}, Memory: new(126976)},
				},
			},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     96,
				CPUSet:   lo.Must(cpuset.Parse("0-11,48-59,12-23,60-71,24-35,72-83,36-47,84-95")),
				Affinity: "0-11,48-59,12-23,60-71,24-35,72-83,36-47,84-95",
				Memory:   496 * 1024 * 1024,
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 126976,
						CPUs:   "0-23",
					},
					1: {
						Memory: 126976,
						CPUs:   "24-47",
					},
					2: {
						Memory: 126976,
						CPUs:   "48-71",
					},
					3: {
						Memory: 126976,
						CPUs:   "72-95",
					},
				},
			},
		},
		{
			name: "static VM with multi cpu numa binding",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 8, MaxMem: 16 * 1024 * 1024 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0-1"}, Memory: new(8192), Policy: "bind"},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"2-3"}, Memory: new(8192), Policy: "bind"},
				},
			},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     8,
				CPUSet:   lo.Must(cpuset.Parse("0-3,8-11")),
				Affinity: "0-3,8-11",
				Memory:   16384 * 1024 * 1024,
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 4096,
						CPUs:   "0-1",
						Policy: "bind",
					},
					1: {
						Memory: 4096,
						CPUs:   "2-3",
						Policy: "bind",
					},
					2: {
						Memory: 4096,
						CPUs:   "4-5",
						Policy: "bind",
					},
					3: {
						Memory: 4096,
						CPUs:   "6-7",
						Policy: "bind",
					},
				},
			},
		},
		{
			name: "static VM with multi cpu cross numa binding",
			vmr:  &cluster.Resource{VMID: 100, MaxCPU: 8, MaxMem: 16 * 1024 * 1024 * 1024},
			cfg: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0-1"}, Memory: new(8192), Policy: "bind"},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"0-1"}, Memory: new(8192), Policy: "bind"},
				},
			},
			expected: &resources.VMResources{
				ID:       100,
				CPUs:     8,
				CPUSet:   lo.Must(cpuset.Parse("0-3,8-11")),
				Affinity: "0-3,8-11",
				Memory:   16384 * 1024 * 1024,
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   "0-1,4-5",
						Policy: "bind",
					},
					1: {
						Memory: 8192,
						CPUs:   "2-3,6-7",
						Policy: "bind",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := vmresources.GetResourceFromVMConfig(tc.vmr, tc.cfg)
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
				NUMANodes: map[int]resources.NUMANodeState{
					1: {
						Memory: 8192,
						CPUs:   "0-3",
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
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 4096,
						CPUs:   "0-1",
						Policy: "bind",
					},
					1: {
						Memory: 4096,
						CPUs:   "2-3",
						Policy: "bind",
					},
					2: {
						Memory: 4096,
						CPUs:   "4-5",
						Policy: "bind",
					},
					3: {
						Memory: 4096,
						CPUs:   "6-7",
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
				NUMANodes: map[int]resources.NUMANodeState{
					0: {
						Memory: 8192,
						CPUs:   "0-3",
						Policy: "bind",
					},
					1: {
						Memory: 8192,
						CPUs:   "4-7",
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
