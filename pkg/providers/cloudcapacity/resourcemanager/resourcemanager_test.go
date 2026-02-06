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

package resourcemanager

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager"
	topology "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/klog/v2/ktesting"
	"k8s.io/utils/cpuset"
)

var testNodeSettings = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: settings.NUMAInfo{
			CPUs:    "0-7",
			MemSize: 16 * 1024 * 1024 * 1024,
		},
		1: settings.NUMAInfo{
			CPUs:    "8-15",
			MemSize: 16 * 1024 * 1024 * 1024,
		},
	},
	ReservedCPUs:   []int{},
	ReservedMemory: 1024 * 1024 * 1024,
}

func TestSimplePolicyAllocateOrUpdate(t *testing.T) {
	t.Parallel()

	sysPolicy := lo.Must(topology.DiscoverFromSettings(&testNodeSettings))

	testCases := []struct { //nolint:dupl
		name    string
		manager *resourceManager
		request []resources.VMResources
		status  string
		error   error
	}{
		{
			name: "empty",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewSimplePolicy(sysPolicy, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			status: "CPU: Free: 16, Static: [], Common: [0-15], Reserved: [], Mem: 31744M",
		},
		{
			name: "simple allocate",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewSimplePolicy(sysPolicy, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 12, Static: [], Common: [0-15], Reserved: [], Mem: 23552M",
		},
		{
			name: "simple allocate two VMs",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewSimplePolicy(sysPolicy, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 8, Static: [], Common: [0-15], Reserved: [], Mem: 15360M",
		},
		{
			name: "simple allocate two VMs with topology",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewSimplePolicy(sysPolicy, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(0, 1, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 8, Static: [0-1,8-9], Common: [2-7,10-15], Reserved: [], Mem: 15360M",
		},
		{
			name: "simple allocate two VMs with topology overlap",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewSimplePolicy(sysPolicy, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(0, 1, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(2, 3, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
			},
			status: "CPU: Free: 10, Static: [0-3,8-9], Common: [4-7,10-15], Reserved: [], Mem: 15360M",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var err error

			for _, r := range tc.request {
				err = tc.manager.AllocateOrUpdate(&r)
				if tc.error != nil {
					assert.EqualError(t, err, tc.error.Error())

					return
				}
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.status, tc.manager.Status())
		})
	}
}

func TestStaticPolicyAllocateOrUpdate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	sysTopology := lo.Must(topology.DiscoverFromSettings(&testNodeSettings))

	testCases := []struct { //nolint:dupl
		name    string
		manager *resourceManager
		request []resources.VMResources
		status  string
		error   error
	}{
		{
			name: "empty",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewStaticPolicy(logger, sysTopology, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			status: "CPU: Free: 16, Static: [], Common: [0-15], Reserved: [], Mem: 31744M, N0:16384M, N1:16384M",
		},
		{
			name: "simple allocate",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewStaticPolicy(logger, sysTopology, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 12, Static: [], Common: [0-15], Reserved: [], Mem: 23552M, N0:16384M, N1:16384M",
		},
		{
			name: "simple allocate two VMs",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewStaticPolicy(logger, sysTopology, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 8, Static: [], Common: [0-15], Reserved: [], Mem: 15360M, N0:16384M, N1:16384M",
		},
		{
			name: "simple allocate two VMs with topology",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewStaticPolicy(logger, sysTopology, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(0, 1, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
				},
			},
			status: "CPU: Free: 8, Static: [0-1,8-9], Common: [2-7,10-15], Reserved: [], Mem: 15360M, N0:12288M, N1:12288M",
		},
		{
			name: "simple allocate two VMs with topology overlap",
			manager: &resourceManager{
				nodeSettings: testNodeSettings,
				nodePolicy:   lo.Must(cpumanager.NewStaticPolicy(logger, sysTopology, testNodeSettings.ReservedCPUs, testNodeSettings.ReservedMemory)),
			},
			request: []resources.VMResources{
				{
					ID:     1,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(0, 1, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
				{
					ID:     2,
					CPUs:   4,
					Memory: 8192 * 1024 * 1024,
					CPUSet: cpuset.New(2, 3, 8, 9),
					NUMANodes: map[int]goproxmox.NUMANodeState{
						0: {
							CPUs:   cpuset.New(0, 1),
							Memory: 4 * 1024,
						},
						1: {
							CPUs:   cpuset.New(2, 3),
							Memory: 4 * 1024,
						},
					},
				},
			},
			status: "CPU: Free: 10, Static: [0-3,8-9], Common: [4-7,10-15], Reserved: [], Mem: 15360M, N0:8192M, N1:8192M",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var err error

			for _, r := range tc.request {
				err = tc.manager.AllocateOrUpdate(&r)
				if tc.error != nil {
					assert.EqualError(t, err, tc.error.Error())

					return
				}
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.status, tc.manager.Status())
		})
	}
}
