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
	"math"
	"sort"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/utils/cpuset"
)

// SharedRequest is one VM's request for a slice of the shared CPU pool —
// the pool of host CPUs left over once every pinned/constrained VM's
// affinity has been removed from it.
type SharedRequest struct {
	// VMID identifies the VM this request belongs to.
	VMID int
	// CPUs is the number of vCPUs the VM needs placed.
	CPUs int
	// NUMAHint restricts allocation to these NUMA nodes when non-empty —
	// the nodes the VM's memory already lives on, or the nodes its
	// current cpuset already sits in, so a rebalance does not needlessly
	// move a VM's vCPUs away from its memory. Best-effort: if the hint
	// cannot be satisfied, the planner falls back to the whole pool.
	NUMAHint cpuset.CPUSet
	// Current is the cpuset presently applied to the VM's threads, if
	// any. A request whose Current is still a valid, disjoint slice of
	// the pool and matches CPUs in size is retained as-is, so a
	// rebalance does not needlessly reshuffle a host that has not
	// meaningfully changed.
	Current cpuset.CPUSet
}

// SharedPlan maps a VM id to the cpuset the planner assigned it. A VMID
// absent from the plan could not be placed (see PlanShared's doc
// comment) — callers should treat that as "no change", not as "confine
// to nothing".
type SharedPlan map[int]cpuset.CPUSet

// SharedOptions fine-tunes PlanShared's behavior.
type SharedOptions struct {
	// MinWidth is the minimum number of CPUs any VM is scaled down to in
	// oversubscribed mode. 0 defaults to one physical core
	// (topo.CPUsPerCore()).
	MinWidth int
}

// PlanShared computes where every requester in reqs should run within pool.
// It is a pure function of its arguments:
// the same topology, pool and requests (in any order)
// always produce the same plan, which is what makes a rebalance
// idempotent and testable without a live host.
//
// Two modes, chosen automatically:
//
//   - Fitting (sum(CPUs) <= pool.Size()): every VM gets a disjoint
//     cpuset of exactly its requested width. No two shared VMs ever
//     share a host CPU.
//   - Oversubscribed (sum(CPUs) > pool.Size()): full isolation is
//     impossible, so widths are scaled down proportionally and VMs are
//     bin-packed into topology groups (NUMA nodes); CPUs may then be
//     shared, but only among VMs placed in the same group.
//
// A request that cannot be placed at all (topology fragmented beyond
// what takeByTopologyNUMAPacked can satisfy) is simply left out of the
// returned plan and logged; it never causes PlanShared itself to fail.
func PlanShared(logger logr.Logger, topo *topology.CPUTopology, pool cpuset.CPUSet, reqs []SharedRequest, opts SharedOptions) (SharedPlan, error) {
	if topo == nil {
		return nil, fmt.Errorf("cpu topology must be provided")
	}

	// The pool may only ever contain CPUs the topology actually knows
	// about — anything else is a caller bug (e.g. a stale reserved-CPU
	// list after a topology change) and would otherwise be silently
	// dropped deep inside the accumulator.
	pool = pool.Intersection(topo.CPUDetails.CPUs())

	filtered := make([]SharedRequest, 0, len(reqs))

	for _, req := range reqs {
		if req.CPUs <= 0 {
			logger.V(2).Info("Skipping shared CPU request with no CPUs", "vmID", req.VMID)

			continue
		}

		filtered = append(filtered, req)
	}

	// Sort deterministically: largest requests first (so they get first
	// pick of whole NUMA nodes/cores), VMID as a stable tiebreaker. Order
	// of the input slice must never affect the outcome.
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CPUs != filtered[j].CPUs {
			return filtered[i].CPUs > filtered[j].CPUs
		}

		return filtered[i].VMID < filtered[j].VMID
	})

	total := 0
	for _, req := range filtered {
		total += req.CPUs
	}

	if total <= pool.Size() {
		return planFitting(logger, topo, pool, filtered), nil
	}

	return planOversubscribed(logger, topo, pool, filtered, opts), nil
}

// planFitting implements: every request gets a disjoint slice of
// pool. Requests whose current assignment is still valid are retained
// first, so a rebalance triggered by an unrelated VM starting or
// stopping does not reshuffle everyone else.
func planFitting(logger logr.Logger, topo *topology.CPUTopology, pool cpuset.CPUSet, reqs []SharedRequest) SharedPlan {
	plan := make(SharedPlan, len(reqs))
	remaining := pool

	for _, req := range reqs {
		if req.Current.Size() == req.CPUs && !req.Current.IsEmpty() && req.Current.IsSubsetOf(remaining) {
			plan[req.VMID] = req.Current
			remaining = remaining.Difference(req.Current)
		}
	}

	for _, req := range reqs {
		if _, ok := plan[req.VMID]; ok {
			continue
		}

		candidate := remaining

		if !req.NUMAHint.IsEmpty() {
			if preferred := remaining.Intersection(req.NUMAHint); preferred.Size() >= req.CPUs {
				candidate = preferred
			}
		}

		cpus, err := takeByTopologyNUMAPacked(logger, topo, candidate, req.CPUs, CPUSortingStrategyPacked, true)
		if err != nil && candidate.Size() != remaining.Size() {
			// The NUMA hint could not satisfy the request on its own —
			// spill over to the whole remaining pool.
			cpus, err = takeByTopologyNUMAPacked(logger, topo, remaining, req.CPUs, CPUSortingStrategyPacked, true)
		}

		if err != nil {
			logger.Error(err, "Failed to place shared VM in fitting mode", "vmID", req.VMID, "cpus", req.CPUs)

			continue
		}

		plan[req.VMID] = cpus
		remaining = remaining.Difference(cpus)
	}

	return plan
}

// planOversubscribed implements: the shared pool is smaller than
// the sum of what is requested, so full isolation is impossible. Widths
// are scaled down proportionally (never below opts.MinWidth, and always
// rounded up to a whole physical core so a VM never owns a lone SMT
// sibling), then requests are bin-packed into per-NUMA-node groups,
// least-loaded group first. CPUs may repeat within a group — that is the
// intended, bounded degradation — but never across groups.
func planOversubscribed(logger logr.Logger, topo *topology.CPUTopology, pool cpuset.CPUSet, reqs []SharedRequest, opts SharedOptions) SharedPlan {
	plan := make(SharedPlan, len(reqs))

	total := 0
	for _, req := range reqs {
		total += req.CPUs
	}

	if total == 0 || pool.IsEmpty() {
		return plan
	}

	cpusPerCore := topo.CPUsPerCore()
	if cpusPerCore <= 0 {
		cpusPerCore = 1
	}

	minWidth := opts.MinWidth
	if minWidth <= 0 {
		minWidth = cpusPerCore
	}

	factor := float64(pool.Size()) / float64(total)

	type group struct {
		numaID int
		cpus   cpuset.CPUSet
		// remaining is the CPUs of this group not yet handed out in the
		// current round — drawn down as requests land in the group, and
		// reset back to cpus once it can no longer satisfy the next
		// request. This is what spreads allocations across the whole
		// group before any CPU repeats, instead of every request in the
		// group landing on the same packed prefix.
		remaining cpuset.CPUSet
		load      int
	}

	var groups []group

	for _, numaID := range topo.CPUDetails.NUMANodes().List() {
		g := pool.Intersection(topo.CPUDetails.CPUsInNUMANodes(numaID))
		if g.Size() > 0 {
			groups = append(groups, group{numaID: numaID, cpus: g, remaining: g})
		}
	}

	if len(groups) == 0 {
		return plan
	}

	for _, req := range reqs {
		width := int(math.Ceil(float64(req.CPUs) * factor))
		width = roundUpToCore(max(width, minWidth), cpusPerCore)

		candidates := groups
		if !req.NUMAHint.IsEmpty() {
			hinted := make([]group, 0, len(groups))

			for _, g := range groups {
				if g.cpus.Intersection(req.NUMAHint).Size() > 0 {
					hinted = append(hinted, g)
				}
			}

			if len(hinted) > 0 {
				candidates = hinted
			}
		}

		best := -1

		for i, g := range candidates {
			if best == -1 || g.load < candidates[best].load ||
				(g.load == candidates[best].load && g.numaID < candidates[best].numaID) {
				best = i
			}
		}

		if best == -1 {
			continue
		}

		chosen := candidates[best]
		w := min(width, chosen.cpus.Size())

		// Draw from what's left of the group this round; once it can no
		// longer satisfy the request, start a fresh round over the
		// whole group rather than letting requests pile onto whatever
		// sliver remains.
		available := chosen.remaining
		if available.Size() < w {
			available = chosen.cpus
		}

		cpus, err := takeByTopologyNUMAPacked(logger, topo, available, w, CPUSortingStrategyPacked, true)
		if err != nil {
			logger.Error(err, "Failed to place shared VM in oversubscribed mode", "vmID", req.VMID, "cpus", req.CPUs, "width", w, "numaNode", chosen.numaID)

			continue
		}

		plan[req.VMID] = cpus

		for i := range groups {
			if groups[i].numaID == chosen.numaID {
				groups[i].load += w
				groups[i].remaining = available.Difference(cpus)

				break
			}
		}
	}

	return plan
}

// roundUpToCore rounds n up to the nearest multiple of cpusPerCore, so a
// VM in oversubscribed mode never ends up owning a single SMT sibling
// without its pair.
func roundUpToCore(n, cpusPerCore int) int {
	if cpusPerCore <= 1 {
		return n
	}

	return ((n + cpusPerCore - 1) / cpusPerCore) * cpusPerCore
}
