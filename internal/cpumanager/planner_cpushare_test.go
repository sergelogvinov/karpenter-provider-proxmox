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
	"slices"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	"k8s.io/utils/cpuset"
)

// The tests use topoDualSocketHT: node 0 has the cores 0,6 / 2,8 / 4,10 and node 1 the
// cores 1,7 / 3,9 / 5,11.
func TestPlanCPUShare(t *testing.T) {
	t.Parallel()

	all := topoDualSocketHT.CPUDetails.CPUs()

	for _, tc := range []struct {
		name   string
		pool   cpuset.CPUSet
		pinned cpuset.CPUSet
		reqs   []CPUShareRequest
		groups []CPUShareGroup
		notes  []string
	}{
		{
			name: "keep: an applied group stays, a new one takes a whole core on the lowest node",
			pool: all,
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 2, Current: cpuset.New(2, 8)},
				{VMID: 2, CPUs: 2, Share: 2, Current: cpuset.New(2, 8)},
				{VMID: 3, CPUs: 2, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(2, 8), Members: []int{1, 2}},
				{CPUs: cpuset.New(0, 6), Members: []int{3}},
			},
		},
		{
			name: "keep: a group no longer inside the pool is placed again",
			pool: all.Difference(cpuset.New(8)),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 2, Current: cpuset.New(2, 8)},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 6), Members: []int{1}},
			},
		},
		{
			name: "merge: the tie between half-empty groups dissolves the one with the higher CPU IDs",
			pool: all,
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 2, Current: cpuset.New(4, 10)},
				{VMID: 2, CPUs: 2, Share: 2, Current: cpuset.New(2, 8)},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(2, 8), Members: []int{1, 2}},
			},
		},
		{
			name: "merge: the emptiest group is dissolved into the fullest",
			pool: all,
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 3, Current: cpuset.New(0, 6)},
				{VMID: 2, CPUs: 2, Share: 3, Current: cpuset.New(4, 10)},
				{VMID: 3, CPUs: 2, Share: 3, Current: cpuset.New(4, 10)},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(4, 10), Members: []int{1, 2, 3}},
			},
		},
		{
			name:   "place: the node with the most pinned CPUs goes first",
			pool:   all.Difference(cpuset.New(3)),
			pinned: cpuset.New(3),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(1, 7), Members: []int{1}},
			},
		},
		{
			name: "spill: no node has two whole cores, so the group takes one on each",
			pool: cpuset.New(0, 6, 2, 1, 7, 3),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 4, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 1, 6, 7), Members: []int{1}},
			},
			notes: []string{"CPU share group of VMs [1] spills across NUMA nodes [0 1]: 0-1,6-7"},
		},
		{
			name: "no half cores: an even group without whole cores does not fit",
			pool: cpuset.New(0, 6, 2, 4),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 4, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 2, 4, 6), Members: []int{1}},
			},
			notes: []string{"CPU share group of VMs [1]: no room for 4 CPUs in the group pool 0,2,4,6, masked to 0,2,4,6"},
		},
		{
			name: "odd width: the group takes any CPUs",
			pool: cpuset.New(0, 2, 4),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 1, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0), Members: []int{1}},
			},
		},
		{
			name: "unfit: a group is masked only to the CPUs the other groups left",
			pool: cpuset.New(0, 6, 2, 8, 4),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 4, Share: 2},
				{VMID: 2, CPUs: 2, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 2, 6, 8), Members: []int{1}},
				{CPUs: cpuset.New(4), Members: []int{2}},
			},
			notes: []string{"CPU share group of VMs [2]: no room for 2 CPUs in the group pool 0,2,4,6,8, masked to 4"},
		},
		{
			name: "unfit: groups that do not fit split what is left between them",
			pool: cpuset.New(0, 2, 4),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 1},
				{VMID: 2, CPUs: 2, Share: 1},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 2), Members: []int{1}},
				{CPUs: cpuset.New(4), Members: []int{2}},
			},
			notes: []string{
				"CPU share group of VMs [1]: no room for 2 CPUs in the group pool 0,2,4, masked to 0,2",
				"CPU share group of VMs [2]: no room for 2 CPUs in the group pool 0,2,4, masked to 4",
			},
		},
		{
			name: "unfit: with nothing left the members are not seated",
			pool: cpuset.New(0, 6),
			reqs: []CPUShareRequest{
				{VMID: 1, CPUs: 2, Share: 2},
				{VMID: 2, CPUs: 1, Share: 2},
			},
			groups: []CPUShareGroup{
				{CPUs: cpuset.New(0, 6), Members: []int{1}},
				{CPUs: cpuset.New(), Members: []int{2}},
			},
			notes: []string{"CPU share group of VMs [2]: no CPUs left in the group pool 0,6, not seated and left as is"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pinned := tc.pinned
			if pinned.IsEmpty() {
				pinned = cpuset.New()
			}

			groups, notes, err := PlanCPUShare(logr.Discard(), topoDualSocketHT, tc.pool, pinned, tc.reqs)
			require.NoError(t, err)
			require.Equal(t, len(tc.groups), len(groups), "groups %v", groups)

			for i := range groups {
				require.True(t, tc.groups[i].CPUs.Equals(groups[i].CPUs), "group %d: want %s, got %s", i, tc.groups[i].CPUs, groups[i].CPUs)
				require.Equal(t, tc.groups[i].Members, groups[i].Members, "group %d", i)
			}

			require.Equal(t, tc.notes, notes)
		})
	}
}

// TestPlanCPUShareIsDeterministic checks that the order of the requests does not change the plan.
func TestPlanCPUShareIsDeterministic(t *testing.T) {
	t.Parallel()

	reqs := []CPUShareRequest{
		{VMID: 1, CPUs: 2, Share: 2, Current: cpuset.New(4, 10)},
		{VMID: 2, CPUs: 2, Share: 2, Current: cpuset.New(2, 8)},
		{VMID: 3, CPUs: 2, Share: 2},
		{VMID: 4, CPUs: 4, Share: 3},
		{VMID: 5, CPUs: 2, Share: 4},
		{VMID: 6, CPUs: 2, Share: 2},
	}

	pool := topoDualSocketHT.CPUDetails.CPUs()

	first, firstNotes, err := PlanCPUShare(logr.Discard(), topoDualSocketHT, pool, cpuset.New(), reqs)
	require.NoError(t, err)

	for range 20 {
		shuffled := slices.Clone(reqs)
		slices.Reverse(shuffled)
		shuffled[0], shuffled[3] = shuffled[3], shuffled[0]

		again, notes, err := PlanCPUShare(logr.Discard(), topoDualSocketHT, pool, cpuset.New(), shuffled)
		require.NoError(t, err)
		require.Equal(t, firstNotes, notes)
		require.Len(t, again, len(first))

		for i := range first {
			require.True(t, first[i].CPUs.Equals(again[i].CPUs), "group %d", i)
			require.Equal(t, first[i].Members, again[i].Members, "group %d", i)
		}

		reqs = shuffled
	}
}
