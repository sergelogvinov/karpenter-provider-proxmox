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
	"slices"
	"strconv"
	"strings"

	"k8s.io/utils/cpuset"
)

// NodeCapacityName and NodeCapacityTag together identify the node-capacity guest, which describes
// the host and is never a workload. Workload VMs often carry the `karpenter` tag too, so the tag
// alone is not enough.
const (
	NodeCapacityName = "node-capacity"
	NodeCapacityTag  = "karpenter"
)

// affinityToken is the description token that carries a VM's host CPUs when the affinity option is unset.
const affinityToken = "affinity="

// cpuShareToken is the description token that seats a VM in a CPU share group of N seats.
const cpuShareToken = "cpushare="

// CPUShareSeats names the numbers of seats a CPU share group can have, for messages.
const CPUShareSeats = "2, 3 or 4"

// ValidCPUShare reports whether n is a number of seats a CPU share group can have: 2, 3 or 4.
func ValidCPUShare(n int) bool {
	return n >= 2 && n <= 4
}

// VM is one virtual machine as read from its config. The module parses the raw fields.
type VM struct {
	ID int
	// Name is the VM name; with the `karpenter` tag it marks the node-capacity guest.
	Name   string
	CPUs   int
	Memory uint64
	// Affinity is the `affinity` option, "" when unset.
	Affinity string
	// Description holds the `affinity=` fallback and `cpushare=N` tokens, comma separated from other text.
	Description string
	// CPUShare asks Reserve and Fit for a CPU share VM of that many seats (2, 3 or 4); 0 asks for none.
	// A tracked VM is read from its `cpushare=N` token instead, unless this is set.
	CPUShare int
	// Slot asks Reserve and Fit for a strict NUMA slot of this shape; CPUs and Memory must match it.
	// A tracked VM is read from its affinity and NUMA memory instead: a pinned VM covering its slot.
	Slot *SlotShape
	// Tags are the VM's tags; `karpenter` on a VM named `node-capacity` marks the node-capacity guest, which is ignored.
	Tags []string
	// NUMAMemory is the memory bound to each host NUMA node, in bytes, from numaN hostnodes.
	NUMAMemory map[int]uint64
}

// class is what a VM is to the zone (docs/zonecapacity.md §3.1). Accounting treats pinned and
// constrained VMs alike, both holding their affinity; Plan tells them apart (1:1 pinning or a mask).
type class int

const (
	// shared VMs run on any unpinned CPU.
	classShared class = iota
	// pinned VMs own one host CPU per vCPU.
	classPinned
	// constrained VMs have an affinity of a different size than their vCPUs.
	classConstrained
	// CPU share VMs sit in a CPU share group with others of the same seats and vCPUs.
	classCPUShare
	// ignored VMs are never counted.
	classIgnored
)

// classified is what classify found out about a VM.
type classified struct {
	class class
	// cpus are the host CPUs of a pinned or constrained VM.
	cpus cpuset.CPUSet
	// share is the seats of a CPU share VM's group.
	share int
}

// classify returns vm's class with its host CPUs or seats. An affinity wins over a `cpushare` token.
func (vm VM) classify() (classified, error) {
	shared := classified{class: classShared, cpus: cpuset.New()}

	if vm.Name == NodeCapacityName && slices.Contains(vm.Tags, NodeCapacityTag) {
		return classified{class: classIgnored, cpus: cpuset.New()}, nil
	}

	raw := vm.Affinity
	if raw == "" {
		raw = affinityFromDescription(vm.Description)
	}

	if raw != "" {
		cpus, err := cpuset.Parse(raw)
		if err != nil {
			return shared, fmt.Errorf("%w: VM %d affinity %q: %v", ErrInvalid, vm.ID, raw, err)
		}

		var c classified

		switch {
		case cpus.Size() == vm.CPUs:
			c = classified{class: classPinned, cpus: cpus}
		case !cpus.IsEmpty():
			c = classified{class: classConstrained, cpus: cpus}
		}

		if !cpus.IsEmpty() {
			if vm.CPUShare != 0 || cpuShareTokenOf(vm.Description) != "" {
				return c, fmt.Errorf("%w: VM %d holds its affinity %s", ErrTokenIgnored, vm.ID, cpus)
			}

			return c, nil
		}
	}

	share, err := vm.cpuShare()
	if err != nil || share == 0 {
		return shared, err
	}

	return classified{class: classCPUShare, cpus: cpuset.New(), share: share}, nil
}

// cpuShare is the seats vm asks for, or reads from its `cpushare=N` token; 0 for none.
func (vm VM) cpuShare() (int, error) {
	if vm.CPUShare != 0 {
		if !ValidCPUShare(vm.CPUShare) {
			return 0, fmt.Errorf("%w: VM %d CPU share %d must be %s", ErrInvalid, vm.ID, vm.CPUShare, CPUShareSeats)
		}

		return vm.CPUShare, nil
	}

	token := cpuShareTokenOf(vm.Description)
	if token == "" {
		return 0, nil
	}

	share, err := strconv.Atoi(strings.TrimPrefix(token, cpuShareToken))
	if err != nil || !ValidCPUShare(share) {
		return 0, fmt.Errorf("%w: VM %d token %q must be %s%s", ErrInvalid, vm.ID, token, cpuShareToken, CPUShareSeats)
	}

	return share, nil
}

// checkSlot rejects a slot request the policy never offers, or one that is not the size of its slot.
func (vm VM) checkSlot(p Policy, share int) error {
	if vm.Slot == nil {
		return nil
	}

	if p.Kind != PolicyStatic {
		return fmt.Errorf("%w: NUMA slot for VM %d under shared placement", ErrNotOffered, vm.ID)
	}

	if share > 0 {
		return fmt.Errorf("%w: VM %d asks for both a NUMA slot and a CPU share", ErrInvalid, vm.ID)
	}

	if !ValidSlotFraction(vm.Slot.Fraction) || vm.CPUs != vm.Slot.CPUs || vm.Memory != vm.Slot.Memory {
		return fmt.Errorf("%w: VM %d of %d CPUs and %dM does not match its NUMA slot %+v", ErrInvalid, vm.ID, vm.CPUs, vm.Memory/MiB, *vm.Slot)
	}

	return nil
}

// cpuShareTokenOf returns the description's `cpushare=` token, "" when it has none.
func cpuShareTokenOf(description string) string {
	for part := range strings.SplitSeq(description, ",") {
		if part = strings.TrimSpace(part); strings.HasPrefix(part, cpuShareToken) {
			return part
		}
	}

	return ""
}

// affinityFromDescription returns the CPU list of the description's `affinity=` token.
// Description parts are comma separated, and so are the ranges of a CPU list
// ("affinity=1-2,9-10, class=default"), so the parts after the token that are
// CPU ranges themselves belong to it.
func affinityFromDescription(description string) string {
	parts := strings.Split(description, ",")

	for i, part := range parts {
		value, ok := strings.CutPrefix(strings.TrimSpace(part), affinityToken)
		if !ok {
			continue
		}

		ranges := []string{value}

		for _, next := range parts[i+1:] {
			next = strings.TrimSpace(next)
			if _, err := cpuset.Parse(next); next == "" || err != nil {
				break
			}

			ranges = append(ranges, next)
		}

		return strings.Join(ranges, ",")
	}

	return ""
}
