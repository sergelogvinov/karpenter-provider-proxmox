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

package resources

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	"k8s.io/utils/cpuset"
)

// NUMANodeState describes the CPUs, memory, and NUMA policy assigned to a
// single host NUMA node.
type NUMANodeState struct {
	CPUs   string
	Memory uint64
	Policy string
}

type VMResources struct {
	ID int
	// CPUs is the number of CPUs assigned to the VM.
	CPUs int
	// Affinity is the CPU affinity string assigned to the VM.
	Affinity string
	// Memory is the amount of memory in bytes assigned to the VM.
	Memory uint64
	// CPUShare is the number of VMs that share the VM's CPUs in a CPU share group: 2, 3 or 4.
	// Zero means the VM does not sit in a CPU share group.
	CPUShare int
	// Slot asks for a strict NUMA slot of this shape: the VM of a NUMA type. Nil asks for none.
	Slot *zonecapacity.SlotShape
	// DiskGBytes is the amount of system disk in gigabytes assigned to the VM.
	DiskGBytes uint64
	// StorageID is the ID of the storage where the VM's disk is located.
	StorageID string

	// CPUSet represents the specific CPUs on the Host assigned to the VM.
	CPUSet cpuset.CPUSet
	// NUMANodes represents the topology on the Host assigned to the VM.
	NUMANodes map[int]NUMANodeState
	// HugePageSize > 0 launches the VM with hugepages of this size, in bytes.
	HugePageSize uint64
	// DescriptionTokens are written into the VM description at launch, e.g. "affinity=8-15" or "cpushare=2".
	DescriptionTokens []string
}
