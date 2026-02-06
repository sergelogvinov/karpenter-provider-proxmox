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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

func TestSimpleAllocate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
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
				CPUTopology: *topoDualSocketHT,
				MemTopology: topology.MemTopology{
					TotalMemory: 32 * 1024 * 1024 * 1024,
				},
			},
			reserved: []int{},

			request: &resources.VMResources{CPUs: 0},
			status:  "CPU: Free: 12, Static: [], Common: [0-11], Reserved: [], Mem: 32768M",
		},
		{
			name: "allocate some CPUs",
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

			request: &resources.VMResources{CPUs: 4, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 8, Static: [], Common: [0-11], Reserved: [], Mem: 28672M",
		},
		{
			name: "allocate some CPUs with specific reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 8, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 2, Static: [], Common: [2-11], Reserved: [0-1], Mem: 28672M",
		},
		{
			name: "allocate all available CPUs",
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

			request: &resources.VMResources{CPUs: 12, Memory: 4 * 1024 * 1024 * 1024},
			status:  "CPU: Free: 0, Static: [], Common: [0-11], Reserved: [], Mem: 28672M",
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

			request: &resources.VMResources{CPUs: 16, Memory: 4 * 1024 * 1024 * 1024},
			error:   fmt.Errorf("not enough CPUs available: requested=16, available=12"),
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
		{
			name: "allocate more than available CPUs with reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 16, Memory: 32 * 1024 * 1024 * 1024},
			error:   fmt.Errorf("not enough CPUs available: requested=16, available=10"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewSimplePolicy(tc.topo, tc.reserved, 0)
			assert.NoError(t, err)

			if tc.error != nil {
				err = policy.Allocate(tc.request)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			init := policy.Status()

			err = policy.Allocate(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			err = policy.Release(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}

func TestSimpleAllocateOrUpdate(t *testing.T) {
	t.Parallel()

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

			request: &resources.VMResources{CPUs: 0},
			status:  "CPU: Free: 12, Static: [], Common: [0-11], Reserved: [], Mem: 32768M",
		},
		{
			name: "allocate some CPUs",
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

			request: &resources.VMResources{CPUs: 4},
			status:  "CPU: Free: 8, Static: [], Common: [0-11], Reserved: [], Mem: 32768M",
		},
		{
			name: "allocate specific CPUs",
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

			request: &resources.VMResources{CPUs: 4, CPUSet: cpuset.New(2, 3, 10, 11)},
			status:  "CPU: Free: 8, Static: [2-3,10-11], Common: [0-1,4-9], Reserved: [], Mem: 32768M",
		},
		{
			name: "allocate some CPUs with some specific reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 8},
			status:  "CPU: Free: 2, Static: [], Common: [2-11], Reserved: [0-1], Mem: 32768M",
		},
		{
			name: "allocate specific CPUs with specific reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 2, CPUSet: cpuset.New(3, 4)},
			status:  "CPU: Free: 8, Static: [3-4], Common: [2,5-11], Reserved: [0-1], Mem: 32768M",
		},
		{
			name: "allocate specific CPUs overlapped with specific reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 2, CPUSet: cpuset.New(1, 2)},
			status:  "CPU: Free: 9, Static: [2], Common: [3-11], Reserved: [0-1], Mem: 32768M",
		},
		{
			name: "allocate more CPUs",
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

			request: &resources.VMResources{CPUs: 16},
			error:   fmt.Errorf("not enough CPUs available: requested=16, available=12"),
		},
		{
			name: "allocate more specific CPUs",
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

			request: &resources.VMResources{CPUs: 16, CPUSet: lo.Must(cpuset.Parse("0-15"))},
			error:   fmt.Errorf("not enough CPUs available: requested=16, available=12"),
		},
		{
			name: "allocate more specific CPUs with reserved CPUs",
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
			reserved: []int{0, 1},

			request: &resources.VMResources{CPUs: 16, CPUSet: lo.Must(cpuset.Parse("0-15"))},
			error:   fmt.Errorf("not enough CPUs available: requested=16, available=12"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewSimplePolicy(tc.topo, tc.reserved, 0)
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
