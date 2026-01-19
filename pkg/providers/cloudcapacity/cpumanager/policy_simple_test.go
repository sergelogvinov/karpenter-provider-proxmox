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

	"k8s.io/utils/cpuset"
)

func TestSimpleAllocate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		topo     *topology.CPUTopology
		reserved []int

		allocateNumCPUs int
		status          string
		error           error
	}{
		{
			name:     "allocate zero CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 0,
			status:          "Free: 12, Static: [], Common: [0-11], Reserved: []",
		},
		{
			name:     "allocate some CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 4,
			status:          "Free: 8, Static: [], Common: [0-11], Reserved: []",
		},
		{
			name:     "allocate some CPUs with specific reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 8,
			status:          "Free: 2, Static: [], Common: [2-11], Reserved: [0-1]",
		},
		{
			name:     "allocate all available CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 12,
			status:          "Free: 0, Static: [], Common: [0-11], Reserved: []",
		},
		{
			name:     "allocate more than available CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 16,
			error:           fmt.Errorf("not enough CPUs available to satisfy request: requested=16, available=12"),
		},
		{
			name:     "allocate more than available CPUs with reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 16,
			error:           fmt.Errorf("not enough CPUs available to satisfy request: requested=16, available=10"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewSimplePolicy(tc.topo, tc.reserved)
			assert.NoError(t, err)

			if tc.error != nil {
				_, err = policy.Allocate(tc.allocateNumCPUs)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			init := policy.Status()

			cpus, err := policy.Allocate(tc.allocateNumCPUs)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			err = policy.Release(tc.allocateNumCPUs, cpus)
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}

func TestSimpleAllocateOrUpdate(t *testing.T) {
	t.Parallel()

	testCases := []struct { //nolint:dupl
		name     string
		topo     *topology.CPUTopology
		reserved []int

		allocateNumCPUs int
		allocateCPUs    cpuset.CPUSet
		status          string
		error           error
	}{
		{
			name:     "allocate zero CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 0,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 12, Static: [], Common: [0-11], Reserved: []",
		},
		{
			name:     "allocate some CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 4,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 8, Static: [], Common: [0-11], Reserved: []",
		},
		{
			name:     "allocate specific CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 4,
			allocateCPUs:    cpuset.New(2, 3, 10, 11),
			status:          "Free: 8, Static: [2-3,10-11], Common: [0-1,4-9], Reserved: []",
		},
		{
			name:     "allocate some CPUs with some specific reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 8,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 2, Static: [], Common: [2-11], Reserved: [0-1]",
		},
		{
			name:     "allocate specific CPUs with specific reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 2,
			allocateCPUs:    cpuset.New(3, 4),
			status:          "Free: 8, Static: [3-4], Common: [2,5-11], Reserved: [0-1]",
		},
		{
			name:     "allocate specific CPUs overlapped with specific reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 2,
			allocateCPUs:    cpuset.New(1, 2),
			status:          "Free: 9, Static: [2], Common: [3-11], Reserved: [0-1]",
		},
		{
			name:     "allocate more CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 16,
			allocateCPUs:    cpuset.New(),
			error:           fmt.Errorf("not enough CPUs available to satisfy request: requested=16, available=12"),
		},
		{
			name:     "allocate more specific CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			allocateNumCPUs: 16,
			allocateCPUs:    lo.Must(cpuset.Parse("0-15")),
			error:           fmt.Errorf("not enough CPUs available to satisfy request: requested=16, available=12"),
		},
		{
			name:     "allocate more specific CPUs with reserved CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{0, 1},

			allocateNumCPUs: 16,
			allocateCPUs:    lo.Must(cpuset.Parse("0-15")),
			error:           fmt.Errorf("not enough CPUs available to satisfy request: requested=16, available=12"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewSimplePolicy(tc.topo, tc.reserved)
			assert.NoError(t, err)

			if tc.error != nil {
				_, err = policy.AllocateOrUpdate(tc.allocateNumCPUs, tc.allocateCPUs)
				assert.EqualError(t, err, tc.error.Error())

				return
			}

			init := policy.Status()

			_, err = policy.AllocateOrUpdate(tc.allocateNumCPUs, tc.allocateCPUs)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			err = policy.Release(tc.allocateNumCPUs, tc.allocateCPUs)
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}
