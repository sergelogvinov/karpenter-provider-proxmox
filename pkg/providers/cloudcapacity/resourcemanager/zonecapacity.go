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

package resourcemanager

import (
	"fmt"
	"math"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// Capacity is what a new shared VM could still get in a zone.
type Capacity struct {
	CPUs   int
	Memory uint64
}

// ZoneCapacity tracks the CPU and memory one zone gives to VMs, one record per VM ID.
// Usage is always recomputed from the records, so reserving, tracking and releasing
// the same VM any number of times never drifts.
type ZoneCapacity struct {
	mu sync.Mutex

	topo      *topology.Topology
	placement Placement

	allCPUs      cpuset.CPUSet
	reservedCPUs cpuset.CPUSet
	// memory is the zone memory left after the reservation.
	memory uint64

	vms map[int]record
}

// record is what one VM holds: shared vCPUs or a pinned CPU set, and its memory.
type record struct {
	cpus   int
	cpuSet cpuset.CPUSet
	memory uint64
	// numaMemory is the VM's memory bound to each NUMA node, if any.
	numaMemory map[int]uint64
}

// usage is the sum of all records.
type usage struct {
	pinnedCPUs cpuset.CPUSet
	sharedCPUs int
	memory     uint64
	numaMemory map[int]uint64
}

// NewZoneCapacity returns an empty zone on topo that places new VMs with placement.
func NewZoneCapacity(topo *topology.Topology, reservedCPUs []int, reservedMemory uint64, placement Placement) (*ZoneCapacity, error) {
	if topo == nil {
		return nil, fmt.Errorf("system topology must be provided")
	}

	reserved := cpuset.New(reservedCPUs...)
	if topo.NumCPUs < reserved.Size() {
		return nil, fmt.Errorf("not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", topo.NumCPUs, reserved.Size())
	}

	if reservedMemory >= topo.TotalMemory {
		return nil, fmt.Errorf("reserved memory %d must be less than max memory %d", reservedMemory, topo.TotalMemory)
	}

	ratio := placement.ratio()
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 1 {
		return nil, fmt.Errorf("CPU overcommit ratio must be a finite number greater than or equal to 1")
	}

	if float64(topo.NumCPUs)*ratio >= float64(math.MaxInt) {
		return nil, fmt.Errorf("CPU overcommit ratio exceeds the supported CPU budget")
	}

	return &ZoneCapacity{
		topo:         topo,
		placement:    placement,
		allCPUs:      topo.CPUDetails.CPUs(),
		reservedCPUs: reserved,
		memory:       topo.TotalMemory - reservedMemory,
		vms:          map[int]record{},
	}, nil
}

// Reserve admits a new VM if the zone has room for it.
func (z *ZoneCapacity) Reserve(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 || op.CPUs <= 0 || op.Memory == 0 {
		return fmt.Errorf("cannot reserve resources: a VM ID, CPUs and memory are required")
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	if _, ok := z.vms[op.ID]; ok {
		return fmt.Errorf("resources for VM %d are already reserved", op.ID)
	}

	cpus, numaNodes, err := z.placement.place(z.free(), op)
	if err != nil {
		return err
	}

	rec := newRecord(op.CPUs, cpus, op.Memory, numaNodes)

	u := z.usageWith(op.ID, rec)
	if u.memory > z.memory {
		return fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, z.available(z.usage()).Memory)
	}

	for i := range rec.numaMemory {
		if u.numaMemory[i] > z.topo.NUMANodes[i] {
			return fmt.Errorf("not enough memory available on NUMA node %d", i)
		}
	}

	if u.sharedCPUs > z.sharedCapacity(u) {
		return fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, z.available(z.usage()).CPUs)
	}

	z.vms[op.ID] = rec

	if !cpus.IsEmpty() {
		op.CPUSet = cpus.Clone()
		op.NUMANodes = numaNodes
	}

	return nil
}

// Track records a VM that already exists in the zone, replacing any earlier record.
// It never fails on budget: an over-committed zone just has nothing available.
func (z *ZoneCapacity) Track(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 {
		return fmt.Errorf("cannot track resources: a VM ID is required")
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	z.vms[op.ID] = newRecord(op.CPUs, op.CPUSet, op.Memory, op.NUMANodes)

	return nil
}

// Release forgets VM id. Releasing a VM without a record changes nothing.
func (z *ZoneCapacity) Release(id int) {
	z.mu.Lock()
	defer z.mu.Unlock()

	delete(z.vms, id)
}

// Available is what a new shared VM could still get, clamped at zero.
func (z *ZoneCapacity) Available() Capacity {
	z.mu.Lock()
	defer z.mu.Unlock()

	return z.available(z.usage())
}

// String describes the zone's capacity for logs.
func (z *ZoneCapacity) String() string {
	z.mu.Lock()
	defer z.mu.Unlock()

	u := z.usage()
	free := z.available(u)

	return fmt.Sprintf("VMs: %d, CPU: Free: %d, Pinned: [%v], Reserved: [%v], Mem: %dM",
		len(z.vms), free.CPUs, u.pinnedCPUs, z.reservedCPUs, free.Memory/1024/1024)
}

func (z *ZoneCapacity) available(u usage) Capacity {
	c := Capacity{CPUs: max(0, z.sharedCapacity(u)-u.sharedCPUs)}
	if u.memory < z.memory {
		c.Memory = z.memory - u.memory
	}

	return c
}

// sharedCapacity is how many shared vCPUs the unpinned, unreserved CPUs offer.
func (z *ZoneCapacity) sharedCapacity(u usage) int {
	unpinned := z.allCPUs.Difference(z.reservedCPUs).Difference(u.pinnedCPUs).Size()

	return int(math.Floor(float64(unpinned) * z.placement.ratio()))
}

// free is what the zone has left for a new VM.
func (z *ZoneCapacity) free() freeResources {
	u := z.usage()
	zoneFree := z.available(u).Memory

	numaMemory := make(map[int]uint64, len(z.topo.NUMANodes))
	for i, mem := range z.topo.NUMANodes {
		var nodeFree uint64
		if u.numaMemory[i] < mem {
			nodeFree = mem - u.numaMemory[i]
		}

		numaMemory[i] = min(nodeFree, zoneFree)
	}

	return freeResources{
		topo:       z.topo,
		cpus:       z.allCPUs.Difference(z.reservedCPUs).Difference(u.pinnedCPUs),
		numaMemory: numaMemory,
	}
}

func (z *ZoneCapacity) usage() usage {
	u := usage{pinnedCPUs: cpuset.New(), numaMemory: map[int]uint64{}}

	for _, rec := range z.vms {
		if rec.cpuSet.IsEmpty() {
			u.sharedCPUs += rec.cpus
		} else {
			u.pinnedCPUs = u.pinnedCPUs.Union(rec.cpuSet)
		}

		u.memory += rec.memory

		for i, mem := range rec.numaMemory {
			u.numaMemory[i] += mem
		}
	}

	u.pinnedCPUs = u.pinnedCPUs.Intersection(z.allCPUs).Difference(z.reservedCPUs)

	return u
}

// usageWith is the usage if VM id held rec instead of its current record.
func (z *ZoneCapacity) usageWith(id int, rec record) usage {
	prev, ok := z.vms[id]
	z.vms[id] = rec

	u := z.usage()

	if ok {
		z.vms[id] = prev
	} else {
		delete(z.vms, id)
	}

	return u
}

func newRecord(cpus int, cpuSet cpuset.CPUSet, memory uint64, numaNodes map[int]resources.NUMANodeState) record {
	rec := record{cpus: cpus, cpuSet: cpuSet.Clone(), memory: memory}

	if len(numaNodes) > 0 {
		rec.numaMemory = make(map[int]uint64, len(numaNodes))
		for i, node := range numaNodes {
			rec.numaMemory[i] = node.Memory * 1024 * 1024
		}
	}

	return rec
}
