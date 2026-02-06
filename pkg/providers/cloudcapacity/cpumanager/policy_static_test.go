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

package cpumanager

import (
	"fmt"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/klog/v2/ktesting"
	"k8s.io/utils/cpuset"
)

func TestStaticAllocate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct {
		name     string
		topo     *topology.Topology
		reserved []int

		request    *resources.VMResources
		status     string
		numaStatus map[int]goproxmox.NUMANodeState
		error      error
	}{
		{
			name: "allocate zero CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 0},
			status:  "CPU: Free: 16, Static: [], Common: [0-15], Reserved: [], Mem: 32768M, N0:32768M",
		},
		{
			name: "allocate some CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 4, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 12, Static: [0-1,8-9], Common: [2-7,10-15], Reserved: [], Mem: 28672M, N0:28672M",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-3")), Memory: 4 * 1024, Policy: "bind"},
			},
		},
		{
			name: "allocate some CPUs with specific reserved physical CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 8},

			request: &resources.VMResources{CPUs: 8, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 6, Static: [4-7,12-15], Common: [1-3,9-11], Reserved: [0,8], Mem: 28672M, N0:28672M",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-7")), Memory: 4 * 1024, Policy: "bind"},
			},
		},
		{
			name: "allocate some CPUs with specific reserved physical CPUs and NUMA0 node",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketMultiNuma,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 16 * 1024 * 1024 * 1024,
						1: 16 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 3},

			request: &resources.VMResources{CPUs: 4, Memory: 12 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 10, Static: [4-7], Common: [1-2,8-15], Reserved: [0,3], Mem: 20480M, N0:4096M, N1:16384M",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-3")), Memory: 12 * 1024, Policy: "bind"},
			},
		},
		{
			name: "allocate some CPUs with specific reserved physical CPUs and NUMA1 node",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketMultiNuma,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 16 * 1024 * 1024 * 1024,
						1: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 3},

			request: &resources.VMResources{CPUs: 6, Memory: 18 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 8, Static: [8-13], Common: [1-2,4-7,14-15], Reserved: [0,3], Mem: 14336M, N0:16384M, N1:14336M",
			numaStatus: map[int]goproxmox.NUMANodeState{
				1: {CPUs: lo.Must(cpuset.Parse("0-5")), Memory: 18 * 1024, Policy: "bind"},
			},
		},
		{
			name: "allocate all available CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 16, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 0, Static: [0-15], Common: [], Reserved: [], Mem: 28672M, N0:28672M",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-15")), Memory: 4 * 1024, Policy: "bind"},
			},
		},
		{
			name: "allocate more than available CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoDualSocketHT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 16 * 1024 * 1024 * 1024,
						1: 16 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 32, Memory: 4 * 1024 * 1024 * 1024},
			error:   fmt.Errorf("not enough CPUs available: requested=32, available=12"),
		},
		{
			name: "allocate more than available memory",
			topo: &topology.Topology{
				CPUTopology: *topoDualSocketHT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 16 * 1024 * 1024 * 1024,
						1: 16 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 8, Memory: 34 * 1024 * 1024 * 1024},
			error:   fmt.Errorf("not enough memory available: requested=36507222016, available=34359738368"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewStaticPolicy(logger, tc.topo, tc.reserved, 0)
			assert.NoError(t, err)

			if tc.error != nil {
				err = policy.Allocate(tc.request)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			// init := policy.Status()

			err = policy.Allocate(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			if tc.numaStatus != nil {
				assert.Equal(t, tc.numaStatus, tc.request.NUMANodes)
			}

			// err = policy.Release(tc.request)
			// assert.NoError(t, err)
			// assert.Equal(t, init, policy.Status())
		})
	}
}

func TestStaticAllocateOrUpdate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct { //nolint:dupl
		name     string
		topo     *topology.Topology
		reserved []int

		request *resources.VMResources
		status  string
		error   error
	}{
		{
			name: "allocate zero CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 0},
			status:  "CPU: Free: 16, Static: [], Common: [0-15], Reserved: [], Mem: 32768M, N0:32768M",
		},
		{
			name: "allocate some CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 4, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 12, Static: [], Common: [0-15], Reserved: [], Mem: 28672M, N0:32768M",
		},
		{
			name: "allocate specific CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 4, CPUSet: cpuset.New(2, 3, 14, 15), Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 12, Static: [2-3,14-15], Common: [0-1,4-13], Reserved: [], Mem: 28672M, N0:32768M",
		},
		{
			name: "allocate some CPUs with some specific reserved CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 8, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 6, Static: [], Common: [2-15], Reserved: [0-1], Mem: 28672M, N0:32768M",
		},
		{
			name: "allocate specific CPUs and NUMA nodes with specific reserved CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 1},

			request: &resources.VMResources{
				CPUs:   2,
				CPUSet: cpuset.New(3, 4),
				Memory: 4 * 1024 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						CPUs:   lo.Must(cpuset.Parse("0-1")),
						Memory: 4 * 1024,
					},
				},
			},
			status: "CPU: Free: 12, Static: [3-4], Common: [2,5-15], Reserved: [0-1], Mem: 28672M, N0:28672M",
		},
		{
			name: "allocate specific CPUs overlapped with specific reserved CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 2, CPUSet: cpuset.New(1, 2), Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 13, Static: [2], Common: [3-15], Reserved: [0-1], Mem: 28672M, N0:32768M",
		},
		{
			name: "allocate more than available CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 32},
			error:   fmt.Errorf("not enough CPUs available: requested=32, available=16"),
		},
		{
			name: "allocate more specific CPUs with reserved CPUs",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 32, CPUSet: lo.Must(cpuset.Parse("0-31"))},
			error:   fmt.Errorf("not enough CPUs available: requested=32, available=16"),
		},
		{
			name: "allocate more than available memory",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 32 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 8, Memory: 34 * 1024 * 1024 * 1024},
			error:   fmt.Errorf("not enough memory available: requested=36507222016, available=34359738368"),
		},
		{
			name: "allocate more than available memory in NUMA node",
			topo: &topology.Topology{
				CPUTopology: *topoUncoreSingleSocketSMT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
					NUMANodes: map[int]uint64{
						0: 16 * 1024 * 1024 * 1024,
						1: 16 * 1024 * 1024 * 1024,
					},
				},
			},
			reserved: []int{},

			request: &resources.VMResources{
				CPUs:   8,
				Memory: 18 * 1024 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {
						CPUs:   lo.Must(cpuset.Parse("0-15")),
						Memory: 18 * 1024,
					},
				},
			},
			error: fmt.Errorf("not enough memory available on NUMA node 0: requested=18432M, available=16384M"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewStaticPolicy(logger, tc.topo, tc.reserved, 0)
			assert.NoError(t, err)

			if tc.error != nil {
				err = policy.AllocateOrUpdate(tc.request)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			init := policy.Status()

			err = policy.AllocateOrUpdate(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			err = policy.Release(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}
