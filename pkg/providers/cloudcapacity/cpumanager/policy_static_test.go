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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cloudresources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/klog/v2/ktesting"
	"k8s.io/utils/cpuset"
)

func TestStaticAllocate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct {
		name     string
		topo     *topology.CPUTopology
		reserved []int

		request    *cloudresources.VMResources
		status     string
		numaStatus map[int]goproxmox.NUMANodeState
		error      error
	}{
		{
			name:     "allocate zero CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 0},
			status:  "Free: 16, Static: [], Common: [0-15], Reserved: []",
		},
		{
			name:     "allocate some CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 4},
			status:  "Free: 12, Static: [0-1,8-9], Common: [2-7,10-15], Reserved: []",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-3")), Policy: "bind"},
			},
		},
		{
			name:     "allocate some CPUs with specific reserved CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 1},

			request: &cloudresources.VMResources{CPUs: 8},
			status:  "Free: 6, Static: [4-7,12-15], Common: [2-3,8-11], Reserved: [0-1]",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-7")), Policy: "bind"},
			},
		},
		{
			name:     "allocate some CPUs with specific reserved physical CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 8},

			request: &cloudresources.VMResources{CPUs: 8},
			status:  "Free: 6, Static: [4-7,12-15], Common: [1-3,9-11], Reserved: [0,8]",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-7")), Policy: "bind"},
			},
		},
		{
			name:     "allocate all available CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 16},
			status:  "Free: 0, Static: [0-15], Common: [], Reserved: []",
			numaStatus: map[int]goproxmox.NUMANodeState{
				0: {CPUs: lo.Must(cpuset.Parse("0-15")), Policy: "bind"},
			},
		},
		{
			name:     "allocate more than available CPUs",
			topo:     topoDualSocketHT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 32},
			error:   fmt.Errorf("not enough cpus available to satisfy request: requested=32, available=12"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewStaticPolicy(logger, tc.topo, tc.reserved)
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

			if tc.numaStatus != nil {
				assert.Equal(t, tc.numaStatus, tc.request.NUMANodes)
			}

			err = policy.Release(tc.request)
			assert.NoError(t, err)
			assert.Equal(t, init, policy.Status())
		})
	}
}

func TestStaticAllocateOrUpdate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct { //nolint:dupl
		name     string
		topo     *topology.CPUTopology
		reserved []int

		request *cloudresources.VMResources
		status  string
		error   error
	}{
		{
			name:     "allocate zero CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 0},
			status:  "Free: 16, Static: [], Common: [0-15], Reserved: []",
		},
		{
			name:     "allocate some CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 4},
			status:  "Free: 12, Static: [], Common: [0-15], Reserved: []",
		},
		{
			name:     "allocate specific CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 4, CPUSet: cpuset.New(2, 3, 14, 15)},
			status:  "Free: 12, Static: [2-3,14-15], Common: [0-1,4-13], Reserved: []",
		},
		{
			name:     "allocate some CPUs with some specific reserved CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 1},

			request: &cloudresources.VMResources{CPUs: 8},
			status:  "Free: 6, Static: [], Common: [2-15], Reserved: [0-1]",
		},
		{
			name:     "allocate specific CPUs with specific reserved CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 1},

			request: &cloudresources.VMResources{CPUs: 2, CPUSet: cpuset.New(3, 4)},
			status:  "Free: 12, Static: [3-4], Common: [2,5-15], Reserved: [0-1]",
		},
		{
			name:     "allocate specific CPUs overlapped with specific reserved CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 1},

			request: &cloudresources.VMResources{CPUs: 2, CPUSet: cpuset.New(1, 2)},
			status:  "Free: 13, Static: [2], Common: [3-15], Reserved: [0-1]",
		},
		{
			name:     "allocate more CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{},

			request: &cloudresources.VMResources{CPUs: 32},
			error:   fmt.Errorf("not enough CPUs available to satisfy request: requested=32, available=16"),
		},
		{
			name:     "allocate more specific CPUs with reserved CPUs",
			topo:     topoUncoreSingleSocketSMT,
			reserved: []int{0, 1},

			request: &cloudresources.VMResources{CPUs: 32, CPUSet: lo.Must(cpuset.Parse("0-31"))},
			error:   fmt.Errorf("not enough CPUs available to satisfy request: requested=32, available=16"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := NewStaticPolicy(logger, tc.topo, tc.reserved)
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
