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

package memmanager_test

import (
	"fmt"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cloudresources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/memmanager/topology"
	testTopology "github.com/sergelogvinov/karpenter-provider-proxmox/test/topology"

	"k8s.io/klog/v2/ktesting"
	"k8s.io/utils/cpuset"
)

func TestStaticAllocate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct {
		name     string
		topo     *topology.MemTopology
		reserved uint64

		request *cloudresources.VMResources
		status  string
		error   error
	}{
		{
			name:    "empty",
			topo:    testTopology.MemTopoUncoreDualSocketNoSMT16G,
			request: &cloudresources.VMResources{},
			status:  "16384M, N0:8192M, N1:8192M",
		},
		{
			name:    "allocate memory",
			topo:    testTopology.MemTopoUncoreDualSocketNoSMT16G,
			request: &cloudresources.VMResources{Memory: 4096 * 1024 * 1024},
			status:  "12288M, N0:8192M, N1:8192M",
		},
		{
			name:     "allocate with reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2048 * 1024 * 1024,
			request:  &cloudresources.VMResources{Memory: 4096 * 1024 * 1024},
			status:   "10240M, N0:8192M, N1:8192M",
		},
		{
			name:     "allocate more with reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2 * 1024 * 1024 * 1024,
			request:  &cloudresources.VMResources{Memory: 16 * 1024 * 1024 * 1024},
			error:    fmt.Errorf("not enough memory available"),
		},
		{
			name:     "allocate with numa and reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2 * 1024 * 1024 * 1024,
			request: &cloudresources.VMResources{
				CPUs:   2,
				Memory: 6 * 1024 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					1: {CPUs: lo.Must(cpuset.Parse("0-1"))},
				},
			},
			status: "8192M, N0:8192M, N1:2048M",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := memmanager.NewStaticPolicy(logger, tc.topo, tc.reserved)
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

func TestStaticAllocateOrUpdate(t *testing.T) {
	t.Parallel()
	logger, _ := ktesting.NewTestContext(t)

	testCases := []struct { //nolint:dupl
		name     string
		topo     *topology.MemTopology
		reserved uint64

		request *cloudresources.VMResources
		status  string
		error   error
	}{
		{
			name:    "empty",
			topo:    testTopology.MemTopoUncoreDualSocketNoSMT16G,
			request: &cloudresources.VMResources{},
			status:  "16384M, N0:8192M, N1:8192M",
		},
		{
			name: "allocate memory",
			topo: testTopology.MemTopoUncoreDualSocketNoSMT16G,
			request: &cloudresources.VMResources{
				Memory: 4096 * 1024 * 1024,
			},
			status: "12288M, N0:8192M, N1:8192M",
		},
		{
			name:     "allocate with reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2 * 1024 * 1024 * 1024,
			request: &cloudresources.VMResources{
				Memory: 4096 * 1024 * 1024,
			},
			status: "10240M, N0:8192M, N1:8192M",
		},
		{
			name:     "allocate more with reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2 * 1024 * 1024 * 1024,
			request: &cloudresources.VMResources{
				Memory: 16 * 1024 * 1024 * 1024,
			},
			error: fmt.Errorf("not enough memory available"),
		},
		{
			name:     "allocate with numa and reserved memory",
			topo:     testTopology.MemTopoUncoreDualSocketNoSMT16G,
			reserved: 2 * 1024 * 1024 * 1024,
			request: &cloudresources.VMResources{
				Memory: 6 * 1024 * 1024 * 1024,
				NUMANodes: map[int]goproxmox.NUMANodeState{
					0: {Memory: 4096},
					1: {Memory: 2048},
				},
			},
			status: "8192M, N0:4096M, N1:6144M",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := memmanager.NewStaticPolicy(logger, tc.topo, tc.reserved)
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
