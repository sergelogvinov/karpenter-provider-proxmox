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

package zonecapacity_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"

	"k8s.io/utils/cpuset"
)

// schedulerHost is the 64-CPU / 4-NUMA host of docs/scheduler.md: 32 cores with their
// siblings 32 apart, 8 cores per NUMA node (0-7,32-39 is node 0).
var schedulerHost = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 4,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-7,32-39", MemSize: 64 * gib},
		1: {CPUs: "8-15,40-47", MemSize: 64 * gib},
		2: {CPUs: "16-23,48-55", MemSize: 64 * gib},
		3: {CPUs: "24-31,56-63", MemSize: 64 * gib},
	},
	ReservedMemory: gib,
}

var partition = zonecapacity.PlanOptions{Partition: true}

func track(t *testing.T, z *zonecapacity.Zone, vms ...zonecapacity.VM) {
	t.Helper()

	for _, vm := range vms {
		require.NoError(t, z.Track(vm))
	}
}

// TestPlanSharedWorkedExample is docs/scheduler-shared.md §10.
func TestPlanSharedWorkedExample(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 100, CPUs: 8, Memory: 8 * gib, Affinity: "8-15"},
		zonecapacity.VM{ID: 200, CPUs: 4, Memory: 4 * gib},
		zonecapacity.VM{ID: 201, CPUs: 4, Memory: 4 * gib},
		zonecapacity.VM{ID: 202, CPUs: 16, Memory: 16 * gib},
	)

	plan := z.Plan(nil, partition)

	require.Equal(t, map[int]cpuset.CPUSet{
		100: cpuset.New(8, 9, 10, 11, 12, 13, 14, 15),
		200: cpuset.New(1, 2, 33, 34),
		201: cpuset.New(3, 4, 35, 36),
		202: mustParse(t, "16-23,48-55"),
	}, plan.Changed)
	require.Equal(t, map[int]bool{100: true}, plan.OneToOne, "only the pinned VM is pinned 1:1")
	require.Equal(t, mustParse(t, "1-4,8-23,33-36,48-55"), plan.Busy)
	require.Equal(t, mustParse(t, "5-7,24-31,37-47,56-63"), plan.Idle)
	require.Empty(t, plan.Notes)

	// A 32-vCPU VM 203 oversubscribes the 54-CPU pool: floating VMs then share CPUs only
	// with VMs in the same NUMA node, and VM 100 keeps 8-15 to itself.
	track(t, z, zonecapacity.VM{ID: 203, CPUs: 32, Memory: 32 * gib})

	applied := plan.Changed

	plan = z.Plan(applied, partition)
	require.NotContains(t, plan.Changed, 100, "the pinned VM is already applied")
	require.Contains(t, plan.Changed, 203)
	maps.Copy(applied, plan.Changed)

	floating := map[int]cpuset.CPUSet{}
	for _, id := range []int{200, 201, 202, 203} {
		floating[id] = applied[id]
		require.False(t, floating[id].IsEmpty(), "VM %d is placed", id)
		require.True(t, floating[id].Intersection(mustParse(t, "0,8-15,32")).IsEmpty(), "VM %d on %s", id, floating[id])
	}

	for a, cpusA := range floating {
		for b, cpusB := range floating {
			if a != b && !cpusA.Intersection(cpusB).IsEmpty() {
				require.Equal(t, numaNodes(cpusA), numaNodes(cpusB), "VMs %d and %d overlap across NUMA nodes", a, b)
			}
		}
	}
}

// TestPlanWithoutPartition checks that --shared-policy=none leaves floating VMs as they are
// while pinned and constrained VMs still get their affinity.
func TestPlanWithoutPartition(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 100, CPUs: 2, Memory: gib, Affinity: "8,40"},
		zonecapacity.VM{ID: 101, CPUs: 4, Memory: gib, Description: "Karpenter, affinity=16-17,48-49, other"},
		zonecapacity.VM{ID: 102, CPUs: 2, Memory: gib, Affinity: "24-27"},
		zonecapacity.VM{ID: 200, CPUs: 4, Memory: gib},
	)

	plan := z.Plan(map[int]cpuset.CPUSet{100: cpuset.New(8, 40)}, zonecapacity.PlanOptions{})

	require.Equal(t, map[int]cpuset.CPUSet{
		101: cpuset.New(16, 17, 48, 49),
		102: cpuset.New(24, 25, 26, 27),
	}, plan.Changed, "VM 100 is applied already, the floating VM 200 is left as is")
	require.Equal(t, map[int]bool{101: true, 102: false}, plan.OneToOne, "VM 102 is constrained: masked, not pinned")
	require.Equal(t, mustParse(t, "8,16-17,24-27,40,48-49"), plan.Busy)
	require.Equal(t, mustParse(t, "1-7,9-15,18-23,28-31,33-39,41-47,50-63"), plan.Idle)
}

// TestPlanKeepsAppliedCPUs checks that a plan that was applied is stable: planning again
// with what was applied changes nothing, and a floating VM only moves when its CPUs are taken.
func TestPlanKeepsAppliedCPUs(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 200, CPUs: 4, Memory: gib},
		zonecapacity.VM{ID: 201, CPUs: 4, Memory: gib},
	)

	applied := map[int]cpuset.CPUSet{200: cpuset.New(24, 25, 56, 57), 201: cpuset.New(8, 9, 40, 41)}

	plan := z.Plan(maps.Clone(applied), partition)
	require.Empty(t, plan.Changed, "an applied plan is kept")

	// A pinned VM starts on VM 201's CPUs: only VM 201 moves, and stays on its NUMA node.
	track(t, z, zonecapacity.VM{ID: 100, CPUs: 4, Memory: gib, Affinity: "8-9,40-41"})

	plan = z.Plan(maps.Clone(applied), partition)
	require.Equal(t, []int{100, 201}, slices.Sorted(maps.Keys(plan.Changed)))
	require.True(t, plan.Changed[201].Intersection(mustParse(t, "8-9,40-41")).IsEmpty())
	require.Equal(t, []int{1}, numaNodes(plan.Changed[201]))
}

// TestPlanSkipsWhatItCannotPlace checks that the node-capacity guest is never planned, a VM with
// an unreadable affinity floats, and an affinity outside the host is left as is with a note.
func TestPlanSkipsWhatItCannotPlace(t *testing.T) {
	t.Parallel()

	z := newZone(t, schedulerHost, 0, 32)
	track(t, z,
		zonecapacity.VM{ID: 9000, Name: zonecapacity.NodeCapacityName, Tags: []string{zonecapacity.NodeCapacityTag}, CPUs: 64, Memory: gib, Affinity: "0-63"},
		zonecapacity.VM{ID: 100, CPUs: 2, Memory: gib, Affinity: "70-71"},
	)
	require.ErrorIs(t, z.Track(zonecapacity.VM{ID: 200, CPUs: 2, Memory: gib, Affinity: "x"}), zonecapacity.ErrInvalid)

	plan := z.Plan(nil, partition)

	require.Equal(t, []int{200}, slices.Sorted(maps.Keys(plan.Changed)), "only the VM with an unreadable affinity floats")
	require.Equal(t, 2, plan.Changed[200].Size())
	require.Len(t, plan.Notes, 1)
	require.Contains(t, plan.Notes[0], "VM 100")
}

// TestPlanDeterministic checks that Plan does not depend on the order VMs were tracked in,
// nor on map order, in both fitting and oversubscribed pools.
func TestPlanDeterministic(t *testing.T) {
	t.Parallel()

	for _, n := range []int{6, 30} {
		rng := rand.New(rand.NewPCG(270, uint64(n)))

		var vms []zonecapacity.VM

		for id := 1; id <= n; id++ {
			vm := zonecapacity.VM{ID: id, CPUs: 1 + rng.IntN(8), Memory: gib}
			if id%5 == 0 {
				vm.CPUs = 2
				vm.Affinity = fmt.Sprintf("%d,%d", 8+id/5, 40+id/5)
			}

			vms = append(vms, vm)
		}

		// Half the VMs already run somewhere, the rest are new.
		current := map[int]cpuset.CPUSet{}
		for _, vm := range vms[:n/2] {
			current[vm.ID] = cpuset.New(16+vm.ID%8, 48+vm.ID%8)
		}

		var want zonecapacity.Plan

		for i := range 10 {
			rng.Shuffle(len(vms), func(a, b int) { vms[a], vms[b] = vms[b], vms[a] })

			z := newZone(t, schedulerHost, 0, 32)
			track(t, z, vms...)

			got := z.Plan(maps.Clone(current), zonecapacity.PlanOptions{Partition: true, MinWidth: 2})
			if i == 0 {
				want = got

				continue
			}

			require.Equal(t, want, got, "%d VMs, shuffle %d", n, i)
		}
	}
}

// numaNodes are the schedulerHost NUMA nodes cpus touches.
func numaNodes(cpus cpuset.CPUSet) []int {
	nodes := cpuset.New()
	for _, cpu := range cpus.List() {
		nodes = nodes.Union(cpuset.New(cpu % 32 / 8))
	}

	return nodes.List()
}

func mustParse(t *testing.T, s string) cpuset.CPUSet {
	t.Helper()

	cpus, err := cpuset.Parse(s)
	require.NoError(t, err)

	return cpus
}
