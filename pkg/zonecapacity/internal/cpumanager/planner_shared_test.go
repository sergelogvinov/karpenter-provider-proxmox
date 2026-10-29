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

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/utils/cpuset"
)

func TestPlanSharedFittingDisjoint(t *testing.T) {
	// topoDualSocketHT: 12 CPUs, 2 NUMA nodes of 6 CPUs each (3 cores x 2
	// threads), node0={0,2,4,6,8,10}, node1={1,3,5,7,9,11}.
	pool := topoDualSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
		{VMID: 102, CPUs: 2},
	}

	plan, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	require.Len(t, plan, 3)

	seen := cpuset.New()

	for vmid, cpus := range plan {
		assert.Equal(t, reqs[indexOf(reqs, vmid)].CPUs, cpus.Size(), "vmid %d", vmid)
		assert.True(t, seen.Intersection(cpus).IsEmpty(), "vmid %d overlaps a previous allocation: %v vs already-assigned %v", vmid, cpus, seen)
		seen = seen.Union(cpus)
	}

	assert.True(t, seen.IsSubsetOf(pool))
}

func TestPlanSharedFittingIsDeterministic(t *testing.T) {
	pool := topoDualSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
		{VMID: 102, CPUs: 2},
	}

	first, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)

	// Reversing input order must not change the outcome.
	reversed := []SharedRequest{reqs[2], reqs[1], reqs[0]}
	second, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reversed, SharedOptions{})
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestPlanSharedFittingRetainsStableAssignments(t *testing.T) {
	pool := topoDualSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
	}

	first, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)

	// Feed the previous plan back in as "Current", and add a third VM.
	next := []SharedRequest{
		{VMID: 100, CPUs: 4, Current: first[100]},
		{VMID: 101, CPUs: 4, Current: first[101]},
		{VMID: 102, CPUs: 2},
	}

	second, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, next, SharedOptions{})
	require.NoError(t, err)

	assert.True(t, first[100].Equals(second[100]), "VM 100 must not move: had %v, now %v", first[100], second[100])
	assert.True(t, first[101].Equals(second[101]), "VM 101 must not move: had %v, now %v", first[101], second[101])
	assert.Equal(t, 2, second[102].Size())
}

func TestPlanSharedFittingPrefersNUMAHint(t *testing.T) {
	pool := topoDualSocketHT.CPUDetails.CPUs()
	node1 := topoDualSocketHT.CPUDetails.CPUsInNUMANodes(1)

	reqs := []SharedRequest{
		{VMID: 100, CPUs: 2, NUMAHint: node1},
	}

	plan, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	assert.True(t, plan[100].IsSubsetOf(node1), "expected VM 100 confined to NUMA node 1, got %v", plan[100])
}

func TestPlanSharedFittingSkipsZeroCPURequests(t *testing.T) {
	pool := topoSingleSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 0},
		{VMID: 101, CPUs: 2},
	}

	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	assert.NotContains(t, plan, 100)
	assert.Contains(t, plan, 101)
}

func TestPlanSharedOversubscribedNoCrossGroupSharing(t *testing.T) {
	// 4 requests of 4 CPUs = 16 requested against a 12-CPU pool: must
	// switch to oversubscribed mode.
	pool := topoDualSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
		{VMID: 102, CPUs: 4},
		{VMID: 103, CPUs: 4},
	}

	plan, err := PlanShared(logr.Discard(), topoDualSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	require.Len(t, plan, 4)

	node0 := topoDualSocketHT.CPUDetails.CPUsInNUMANodes(0)
	node1 := topoDualSocketHT.CPUDetails.CPUsInNUMANodes(1)

	for vmid, cpus := range plan {
		assert.False(t, cpus.IsEmpty(), "vmid %d got an empty cpuset", vmid)

		inNode0 := cpus.IsSubsetOf(node0)
		inNode1 := cpus.IsSubsetOf(node1)
		assert.True(t, inNode0 || inNode1, "vmid %d spans both NUMA nodes: %v (this design never crosses a group boundary)", vmid, cpus)
	}
}

func TestPlanSharedOversubscribedRespectsMinWidth(t *testing.T) {
	pool := topoSingleSocketHT.CPUDetails.CPUs() // 8 CPUs, 4 cores
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 16},
		{VMID: 101, CPUs: 16},
		{VMID: 102, CPUs: 16},
		{VMID: 103, CPUs: 16},
		{VMID: 104, CPUs: 16},
	}

	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, pool, reqs, SharedOptions{MinWidth: 2})
	require.NoError(t, err)

	for vmid, cpus := range plan {
		assert.GreaterOrEqual(t, cpus.Size(), 2, "vmid %d below MinWidth", vmid)
	}
}

// TestPlanSharedOversubscribedDistributesWithinGroup exercises a group
// whose total demand exactly meets its size: the planner must spread
// requests across every CPU in the group instead of stacking them all
// onto the same packed prefix (i.e. reusing the same "best" cpuset on
// every call), which is what a naive takeByTopologyNUMAPacked(group,
// width) call on every request would do.
func TestPlanSharedOversubscribedDistributesWithinGroup(t *testing.T) {
	// Single NUMA node (topoSingleSocketHT: 8 CPUs, 4 cores). Four
	// requests of 4 CPUs each (total 16) against an 8-CPU pool forces
	// oversubscribed mode; the scaling factor (0.5) brings each down to
	// a width of one whole core (2 CPUs) — 4 requests * 2 CPUs exactly
	// meets the group's size of 8.
	pool := topoSingleSocketHT.CPUDetails.CPUs()
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
		{VMID: 102, CPUs: 4},
		{VMID: 103, CPUs: 4},
	}

	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	require.Len(t, plan, 4)

	covered := cpuset.New()
	seen := cpuset.New()

	for vmid, cpus := range plan {
		assert.True(t, seen.Intersection(cpus).IsEmpty(),
			"vmid %d's cpuset %v overlaps an earlier allocation (%v): allocations must spread across the group, not repeat", vmid, cpus, seen)
		seen = seen.Union(cpus)
		covered = covered.Union(cpus)
	}

	assert.True(t, covered.Equals(pool), "total demand meets the group's size, so allocations must collectively cover every CPU in it, got %v", covered)
}

// TestPlanSharedOversubscribedWrapsAroundGroup extends the above beyond
// one full round: once a group's remaining CPUs are exhausted, the next
// request must reset to the whole group rather than failing or
// collapsing to an empty cpuset — and the group must still end up fully
// covered by the round(s) that filled it.
func TestPlanSharedOversubscribedWrapsAroundGroup(t *testing.T) {
	pool := topoSingleSocketHT.CPUDetails.CPUs() // 8 CPUs, 4 cores
	reqs := []SharedRequest{
		{VMID: 100, CPUs: 4},
		{VMID: 101, CPUs: 4},
		{VMID: 102, CPUs: 4},
		{VMID: 103, CPUs: 4},
		{VMID: 104, CPUs: 4}, // forces a second round in the same group
	}

	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	require.Len(t, plan, 5)

	covered := cpuset.New()

	for vmid, cpus := range plan {
		assert.Equal(t, 2, cpus.Size(), "vmid %d", vmid)
		covered = covered.Union(cpus)
	}

	assert.True(t, covered.Equals(pool), "demand exceeds the group's size, so the group must still end up fully covered once it wraps around, got %v", covered)
}

func TestPlanSharedEmptyPool(t *testing.T) {
	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, cpuset.New(), []SharedRequest{{VMID: 100, CPUs: 2}}, SharedOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan)
}

func TestPlanSharedNilTopology(t *testing.T) {
	_, err := PlanShared(logr.Discard(), nil, cpuset.New(), nil, SharedOptions{})
	assert.Error(t, err)
}

func TestPlanSharedPoolClampedToTopology(t *testing.T) {
	// A pool containing CPUs the topology doesn't know about (e.g. a
	// stale reserved-CPU computation after a topology change) must not
	// leak into the plan.
	pool := topoSingleSocketHT.CPUDetails.CPUs().Union(cpuset.New(999))
	reqs := []SharedRequest{{VMID: 100, CPUs: 2}}

	plan, err := PlanShared(logr.Discard(), topoSingleSocketHT, pool, reqs, SharedOptions{})
	require.NoError(t, err)
	assert.False(t, plan[100].Contains(999))
}

func indexOf(reqs []SharedRequest, vmid int) int {
	for i, r := range reqs {
		if r.VMID == vmid {
			return i
		}
	}

	return -1
}
