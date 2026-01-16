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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/utils/cpuset"
)

func TestAllocate(t *testing.T) {
	testCases := []struct {
		name         string
		topo         *topology.CPUTopology
		reservedCPUs cpuset.CPUSet

		allocateNumCPUs int
		status          string
	}{
		{
			name:         "allocate zero CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 0,
			status:          "Free: 12, Static: [], All: [0-11], Reserved: []",
		},
		{
			name:         "allocate some CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 4,
			status:          "Free: 8, Static: [], All: [0-11], Reserved: []",
		},
		{
			name:         "allocate some CPUs with specific reserved CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(0, 1),

			allocateNumCPUs: 8,
			status:          "Free: 2, Static: [], All: [2-11], Reserved: [0-1]",
		},
		{
			name:         "allocate all available CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 12,
			status:          "Free: 0, Static: [], All: [0-11], Reserved: []",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewSimplePolicy(tc.topo, tc.reservedCPUs)
			assert.NoError(t, err)

			init := policy.Status()

			_, err = policy.Allocate(tc.allocateNumCPUs)
			assert.NoError(t, err)
			assert.Equal(t, tc.status, policy.Status())

			err = policy.Release(tc.allocateNumCPUs, cpuset.New())
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}

func TestAllocateOrUpdate(t *testing.T) {
	testCases := []struct {
		name         string
		topo         *topology.CPUTopology
		reservedCPUs cpuset.CPUSet

		allocateNumCPUs int
		allocateCPUs    cpuset.CPUSet
		status          string
	}{
		{
			name:         "allocate zero CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 0,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 12, Static: [], All: [0-11], Reserved: []",
		},
		{
			name:         "allocate some CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 4,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 8, Static: [], All: [0-11], Reserved: []",
		},
		{
			name:         "allocate specific CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(),

			allocateNumCPUs: 4,
			allocateCPUs:    cpuset.New(2, 3, 14, 15),
			status:          "Free: 8, Static: [2-3,14-15], All: [0-11], Reserved: []",
		},
		{
			name:         "allocate some CPUs with some specific reserved CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(0, 1),

			allocateNumCPUs: 8,
			allocateCPUs:    cpuset.New(),
			status:          "Free: 2, Static: [], All: [2-11], Reserved: [0-1]",
		},
		{
			name:         "allocate specific CPUs with specific reserved CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(0, 1),

			allocateNumCPUs: 2,
			allocateCPUs:    cpuset.New(3, 4),
			status:          "Free: 8, Static: [3-4], All: [2-11], Reserved: [0-1]",
		},
		{
			name:         "allocate specific CPUs overlapped with specific reserved CPUs",
			topo:         topoDualSocketHT,
			reservedCPUs: cpuset.New(0, 1),

			allocateNumCPUs: 2,
			allocateCPUs:    cpuset.New(1, 2),
			status:          "Free: 9, Static: [2], All: [2-11], Reserved: [0-1]",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewSimplePolicy(tc.topo, tc.reservedCPUs)
			assert.NoError(t, err)

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
