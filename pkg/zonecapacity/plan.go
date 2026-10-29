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

package zonecapacity

import (
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/internal/cpumanager"

	"k8s.io/utils/cpuset"
)

// PlanOptions are the scheduler's settings for a plan.
type PlanOptions struct {
	// Partition divides the unpinned CPUs among the VMs under shared placement
	// (--shared-policy=partition). Without it those VMs are left as they are.
	Partition bool
	// MinWidth is the fewest CPUs a VM under shared placement is scaled down to when
	// the unpinned CPUs are oversubscribed (--shared-min-width); 0 means one physical core.
	MinWidth int
}

// Placement is how a VM's threads are put on its CPUs.
type Placement int

const (
	// PlacementShared is shared placement: the VM's threads are masked to its partition
	// of the unpinned CPUs.
	PlacementShared Placement = iota
	// PlacementPinned is pinned placement: one vCPU thread per host CPU of the affinity.
	PlacementPinned
	// PlacementConstrained masks a constrained VM's threads to its affinity.
	PlacementConstrained
)

// Assignment is where one VM's threads go.
type Assignment struct {
	CPUs      cpuset.CPUSet
	Placement Placement
}

// OneToOne reports whether the VM's vCPU threads are pinned one per CPU, not masked.
func (a Assignment) OneToOne() bool {
	return a.Placement == PlacementPinned
}

// Plan is where the zone's running VMs' CPUs go.
type Plan struct {
	// Changed are the VMs whose cpuset must change; a VM absent from it is left as is.
	Changed map[int]Assignment
	// Pinned are the host CPUs pinned and constrained VMs hold.
	Pinned cpuset.CPUSet
	// Unpinned are the usable CPUs no pinned or constrained VM holds: where VMs under
	// shared placement run.
	Unpinned cpuset.CPUSet
	// Busy are the CPUs some VM's cpuset holds once the plan is applied.
	Busy cpuset.CPUSet
	// Idle are the rest of the usable CPUs.
	Idle cpuset.CPUSet
	// Notes explain what the plan could not do: VMs left as they are and why.
	Notes []string
}

// Plan works out where every tracked VM's CPUs go. current holds the cpusets the caller
// has actually applied to each VM. Pinned VMs get their affinity one CPU per vCPU,
// constrained VMs are masked to it, and with o.Partition the VMs under shared placement
// divide the unpinned CPUs. It is deterministic in the records, current and o, and never
// fails: problems come back in Notes.
func (z *Zone) Plan(current map[int]cpuset.CPUSet, o PlanOptions) Plan {
	z.mu.Lock()
	defer z.mu.Unlock()

	usable := z.allCPUs.Difference(z.reservedCPUs)
	pinned := cpuset.New()
	target := map[int]cpuset.CPUSet{}

	// keep leaves VM id on what it has applied, if anything.
	keep := func(id int) {
		if cur, ok := current[id]; ok {
			target[id] = cur
		}
	}

	plan := Plan{Changed: map[int]Assignment{}}

	var shared []cpumanager.SharedRequest

	for _, id := range slices.Sorted(maps.Keys(z.vms)) {
		rec := z.vms[id]

		if rec.cpuSet.IsEmpty() {
			if o.Partition && rec.cpus > 0 {
				shared = append(shared, cpumanager.SharedRequest{
					VMID:     id,
					CPUs:     rec.cpus,
					NUMAHint: z.numaHint(current[id]),
					Current:  current[id],
				})
			} else {
				keep(id)
			}

			continue
		}

		pinned = pinned.Union(rec.cpuSet)

		if !rec.cpuSet.IsSubsetOf(z.allCPUs) {
			plan.Notes = append(plan.Notes, fmt.Sprintf("VM %d: affinity %s is outside the host CPUs %s, left as is", id, rec.cpuSet, z.allCPUs))
			keep(id)

			continue
		}

		target[id] = rec.cpuSet

		if !rec.cpuSet.Equals(current[id]) {
			placement := PlacementConstrained
			if rec.cpuSet.Size() == rec.cpus {
				placement = PlacementPinned
			}

			plan.Changed[id] = Assignment{CPUs: rec.cpuSet, Placement: placement}
		}
	}

	plan.Pinned = pinned.Intersection(z.allCPUs)
	plan.Unpinned = usable.Difference(pinned)

	if len(shared) > 0 {
		partitions, err := cpumanager.PlanShared(logr.Discard(), &z.topo.CPUTopology, plan.Unpinned, shared, cpumanager.SharedOptions{MinWidth: o.MinWidth})
		if err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("unpinned CPUs %s: %v", plan.Unpinned, err))
		}

		for _, req := range shared {
			cpus, ok := partitions[req.VMID]
			if !ok {
				plan.Notes = append(plan.Notes, fmt.Sprintf("VM %d: no room for %d CPUs in the unpinned CPUs %s, left as is", req.VMID, req.CPUs, plan.Unpinned))
				keep(req.VMID)

				continue
			}

			target[req.VMID] = cpus

			if !cpus.Equals(current[req.VMID]) {
				plan.Changed[req.VMID] = Assignment{CPUs: cpus, Placement: PlacementShared}
			}
		}
	}

	busy := cpuset.New()
	for _, cpus := range target {
		busy = busy.Union(cpus)
	}

	plan.Busy = busy.Intersection(z.allCPUs)
	plan.Idle = usable.Difference(busy)

	return plan
}

// numaHint is the CPUs of every NUMA node cpus touches; empty for no cpus.
func (z *Zone) numaHint(cpus cpuset.CPUSet) cpuset.CPUSet {
	nodes := []int{}

	for _, cpu := range cpus.List() {
		if info, ok := z.topo.CPUDetails[cpu]; ok {
			nodes = append(nodes, info.NUMANodeID)
		}
	}

	return z.topo.CPUDetails.CPUsInNUMANodes(nodes...)
}
