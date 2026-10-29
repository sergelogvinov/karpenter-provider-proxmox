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
	// Partition confines every floating VM to a slice of the shared pool (--shared-policy=partition).
	// Without it floating VMs are left as they are.
	Partition bool
	// MinWidth is the fewest CPUs a floating VM is scaled down to when the shared pool
	// is oversubscribed (--shared-min-width); 0 means one physical core.
	MinWidth int
}

// Plan is where the zone's running VMs' CPUs go.
type Plan struct {
	// Changed are the VMs whose cpuset must change; a VM absent from it is left as is.
	Changed map[int]cpuset.CPUSet
	// OneToOne marks the VMs in Changed whose vCPUs are pinned one per CPU, not masked.
	OneToOne map[int]bool
	// Busy are the CPUs some VM's cpuset holds once the plan is applied.
	Busy cpuset.CPUSet
	// Idle are the rest of the usable CPUs.
	Idle cpuset.CPUSet
	// Notes explain what the plan could not do: VMs left as they are and why.
	Notes []string
}

// Plan works out where every tracked VM's CPUs go, given the cpusets current that the
// caller has actually applied. Pinned VMs get their affinity one CPU per vCPU, constrained
// VMs are masked to it, and with o.Partition floating VMs share what is left. It is
// deterministic in the records, current and o, and never fails: problems come back in Notes.
func (z *Zone) Plan(current map[int]cpuset.CPUSet, o PlanOptions) Plan {
	z.mu.Lock()
	defer z.mu.Unlock()

	usable := z.allCPUs.Difference(z.reservedCPUs)
	exclusive := cpuset.New()
	target := map[int]cpuset.CPUSet{}

	plan := Plan{Changed: map[int]cpuset.CPUSet{}, OneToOne: map[int]bool{}}

	var floating []cpumanager.SharedRequest

	for _, id := range slices.Sorted(maps.Keys(z.vms)) {
		rec := z.vms[id]

		if rec.cpuSet.IsEmpty() {
			if o.Partition && rec.cpus > 0 {
				floating = append(floating, cpumanager.SharedRequest{
					VMID:     id,
					CPUs:     rec.cpus,
					NUMAHint: z.numaHint(current[id]),
					Current:  current[id],
				})
			} else if cur, ok := current[id]; ok {
				target[id] = cur
			}

			continue
		}

		exclusive = exclusive.Union(rec.cpuSet)

		if !rec.cpuSet.IsSubsetOf(z.allCPUs) {
			plan.Notes = append(plan.Notes, fmt.Sprintf("VM %d: affinity %s is outside the host CPUs %s, left as is", id, rec.cpuSet, z.allCPUs))

			if cur, ok := current[id]; ok {
				target[id] = cur
			}

			continue
		}

		target[id] = rec.cpuSet

		if !rec.cpuSet.Equals(current[id]) {
			plan.Changed[id] = rec.cpuSet
			plan.OneToOne[id] = rec.cpuSet.Size() == rec.cpus
		}
	}

	if len(floating) > 0 {
		pool := usable.Difference(exclusive)

		shared, err := cpumanager.PlanShared(logr.Discard(), &z.topo.CPUTopology, pool, floating, cpumanager.SharedOptions{MinWidth: o.MinWidth})
		if err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("shared pool %s: %v", pool, err))
		}

		for _, req := range floating {
			cpus, ok := shared[req.VMID]
			if !ok {
				plan.Notes = append(plan.Notes, fmt.Sprintf("VM %d: no room for %d CPUs in the shared pool %s, left as is", req.VMID, req.CPUs, pool))

				if cur, ok := current[req.VMID]; ok {
					target[req.VMID] = cur
				}

				continue
			}

			target[req.VMID] = cpus

			if !cpus.Equals(current[req.VMID]) {
				plan.Changed[req.VMID] = cpus
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
