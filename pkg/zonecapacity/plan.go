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
	// Partition divides the ungrouped CPUs among the VMs under shared placement
	// (--shared-policy=partition). Without it those VMs are masked to all the ungrouped
	// CPUs while a CPU share group exists or once they were masked, and otherwise left
	// as they are. CPU share VMs are confined to their groups either way.
	Partition bool
	// MinWidth is the fewest CPUs a VM under shared placement is scaled down to when
	// the ungrouped CPUs are oversubscribed (--shared-min-width); 0 means one physical core.
	MinWidth int
}

// Placement is how a VM's threads are put on its CPUs.
type Placement int

const (
	// PlacementShared is shared placement: the VM's threads are masked to its partition
	// of the ungrouped CPUs, or to all of them.
	PlacementShared Placement = iota
	// PlacementPinned is pinned placement: one vCPU thread per host CPU of the affinity.
	PlacementPinned
	// PlacementConstrained masks a constrained VM's threads to its affinity.
	PlacementConstrained
	// PlacementCPUShare masks a CPU share VM's threads to its CPU share group.
	PlacementCPUShare
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
	// Unpinned are the usable CPUs no pinned or constrained VM holds: where the CPU
	// share groups and the VMs under shared placement run.
	Unpinned cpuset.CPUSet
	// Groups are the unpinned CPUs the CPU share groups hold.
	Groups cpuset.CPUSet
	// Ungrouped are the unpinned CPUs no CPU share group holds: where VMs under shared
	// placement run.
	Ungrouped cpuset.CPUSet
	// Busy are the CPUs some VM's cpuset holds once the plan is applied.
	Busy cpuset.CPUSet
	// Idle are the rest of the usable CPUs.
	Idle cpuset.CPUSet
	// Notes explain what the plan could not do: VMs left as they are and why, CPU share
	// groups that spilled across NUMA nodes or did not fit, and drift from the CPUs
	// accounting charges for the groups.
	Notes []string
}

// Plan works out where every tracked VM's CPUs go. current holds the cpusets the caller
// has actually applied to each VM. Pinned VMs get their affinity one CPU per vCPU,
// constrained VMs are masked to it, CPU share VMs are masked to their CPU share group
// (docs/scheduler-cpushare.md §8), and VMs under shared placement get the ungrouped CPUs:
// with o.Partition they divide them. Priority is pinned > CPU share groups > shared
// placement. It is deterministic in the records, current and o, and never fails: problems
// come back in Notes.
func (z *Zone) Plan(current map[int]cpuset.CPUSet, o PlanOptions) Plan {
	z.mu.Lock()
	defer z.mu.Unlock()

	p := &planner{
		z:       z,
		current: current,
		target:  map[int]cpuset.CPUSet{},
		plan:    Plan{Changed: map[int]Assignment{}},
	}

	usable := z.allCPUs.Difference(z.reservedCPUs)
	pinned := cpuset.New()

	var (
		sharedVMs []int
		members   []cpumanager.CPUShareRequest
	)

	for _, id := range slices.Sorted(maps.Keys(z.vms)) {
		rec := z.vms[id]

		switch {
		case rec.share > 0:
			members = append(members, cpumanager.CPUShareRequest{VMID: id, CPUs: rec.cpus, Share: rec.share, Current: current[id]})

			continue
		case rec.cpuSet.IsEmpty():
			sharedVMs = append(sharedVMs, id)

			continue
		}

		pinned = pinned.Union(rec.cpuSet)

		if !rec.cpuSet.IsSubsetOf(z.allCPUs) {
			p.notef("VM %d: affinity %s is outside the host CPUs %s, left as is", id, rec.cpuSet, z.allCPUs)
			p.keep(id)

			continue
		}

		placement := PlacementConstrained
		if rec.cpuSet.Size() == rec.cpus {
			placement = PlacementPinned
		}

		p.assign(id, rec.cpuSet, placement)
	}

	p.plan.Pinned = pinned.Intersection(z.allCPUs)
	p.plan.Unpinned = usable.Difference(pinned)

	p.planCPUShare(members)
	p.planShared(sharedVMs, o)

	busy := cpuset.New()
	for _, cpus := range p.target {
		busy = busy.Union(cpus)
	}

	p.plan.Busy = busy.Intersection(z.allCPUs)
	p.plan.Idle = usable.Difference(busy)

	return p.plan
}

// planner builds one Plan of z from the cpusets current holds as applied.
type planner struct {
	z       *Zone
	current map[int]cpuset.CPUSet
	// target is every VM's cpuset once the plan is applied.
	target map[int]cpuset.CPUSet
	plan   Plan
}

// keep leaves VM id on what it has applied, if anything.
func (p *planner) keep(id int) {
	if cur, ok := p.current[id]; ok {
		p.target[id] = cur
	}
}

// assign sets VM id's target, and changes it when it differs from what was applied.
func (p *planner) assign(id int, cpus cpuset.CPUSet, placement Placement) {
	p.target[id] = cpus

	if !cpus.Equals(p.current[id]) {
		p.plan.Changed[id] = Assignment{CPUs: cpus, Placement: placement}
	}
}

// notef adds a note to the plan.
func (p *planner) notef(format string, args ...any) {
	p.plan.Notes = append(p.plan.Notes, fmt.Sprintf(format, args...))
}

// planCPUShare seats the CPU share VMs in their groups on the unpinned CPUs, and sets
// plan.Groups and plan.Ungrouped. A member no group has CPUs for is left as it is. It
// notes when the groups use other than the CPUs static accounting charges for them: the
// scheduler builds CPU share groups for the static node policy.
func (p *planner) planCPUShare(members []cpumanager.CPUShareRequest) {
	p.plan.Groups = cpuset.New()
	p.plan.Ungrouped = p.plan.Unpinned

	charged := p.z.usage(Policy{Kind: PolicyStatic}).cpuShareCPUs

	if len(members) > 0 {
		groups, notes, err := cpumanager.PlanCPUShare(logr.Discard(), &p.z.topo.CPUTopology, p.plan.Unpinned, p.plan.Pinned, members)
		if err != nil {
			p.notef("CPU share groups: %v", err)

			return
		}

		p.plan.Notes = append(p.plan.Notes, notes...)

		for _, g := range groups {
			for _, id := range g.Members {
				if g.CPUs.IsEmpty() {
					p.keep(id)

					continue
				}

				p.assign(id, g.CPUs, PlacementCPUShare)
			}

			p.plan.Groups = p.plan.Groups.Union(g.CPUs)
		}

		p.plan.Ungrouped = p.plan.Unpinned.Difference(p.plan.Groups)
	}

	if used := p.plan.Groups.Size(); used != charged {
		p.notef("CPU share groups drift: the groups use %d CPUs, accounting charges %d", used, charged)
	}
}

// planShared places the VMs under shared placement on plan.Ungrouped. With o.Partition
// they divide it; otherwise each is masked to all of it while a CPU share group exists or
// once it was masked, and left as it is otherwise.
func (p *planner) planShared(ids []int, o PlanOptions) {
	var reqs []cpumanager.SharedRequest

	for _, id := range ids {
		rec := p.z.vms[id]

		switch {
		case o.Partition && rec.cpus > 0:
			reqs = append(reqs, cpumanager.SharedRequest{
				VMID:     id,
				CPUs:     rec.cpus,
				NUMAHint: p.z.numaHint(p.current[id]),
				Current:  p.current[id],
			})
		case !o.Partition && (!p.plan.Groups.IsEmpty() || !p.current[id].IsEmpty()) && !p.plan.Ungrouped.IsEmpty():
			p.assign(id, p.plan.Ungrouped, PlacementShared)
		default:
			p.keep(id)
		}
	}

	if len(reqs) == 0 {
		return
	}

	partitions, err := cpumanager.PlanShared(logr.Discard(), &p.z.topo.CPUTopology, p.plan.Ungrouped, reqs, cpumanager.SharedOptions{MinWidth: o.MinWidth})
	if err != nil {
		p.notef("ungrouped CPUs %s: %v", p.plan.Ungrouped, err)
	}

	for _, req := range reqs {
		cpus, ok := partitions[req.VMID]
		if !ok {
			p.notef("VM %d: no room for %d CPUs in the ungrouped CPUs %s, left as is", req.VMID, req.CPUs, p.plan.Ungrouped)
			p.keep(req.VMID)

			continue
		}

		p.assign(req.VMID, cpus, PlacementShared)
	}
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
