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

// VM is one virtual machine as read from its config. The module parses the raw fields.
type VM struct {
	ID int
	// Name is the VM name; with the `karpenter` tag it marks the node-capacity guest.
	Name   string
	CPUs   int
	Memory uint64
	// Affinity is the `affinity` option, "" when unset.
	Affinity string
	// Description holds the `affinity=` fallback token, comma separated from other text.
	Description string
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
	// ignored VMs are never counted.
	classIgnored
)

// classify returns vm's class and its host CPUs.
func (vm VM) classify() (class, cpuset.CPUSet, error) {
	if vm.Name == NodeCapacityName && slices.Contains(vm.Tags, NodeCapacityTag) {
		return classIgnored, cpuset.New(), nil
	}

	raw := vm.Affinity
	if raw == "" {
		raw = affinityFromDescription(vm.Description)
	}

	if raw == "" {
		return classShared, cpuset.New(), nil
	}

	cpus, err := cpuset.Parse(raw)
	if err != nil {
		return classShared, cpuset.New(), fmt.Errorf("%w: VM %d affinity %q: %v", ErrInvalid, vm.ID, raw, err)
	}

	if cpus.IsEmpty() {
		return classShared, cpus, nil
	}

	if cpus.Size() == vm.CPUs {
		return classPinned, cpus, nil
	}

	return classConstrained, cpus, nil
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
