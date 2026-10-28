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
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// pendingGracePeriod is how long a pending reservation outlives a VM that never appears in the cluster.
const pendingGracePeriod = 10 * time.Minute

// errInvalidRequest means a VM request lacks the CPUs or memory, or the VM ID Reserve needs.
var errInvalidRequest = errors.New("cannot reserve resources: a VM ID, CPUs and memory are required")

// ErrNoRoom marks a reservation that fails because the zone has no room for the VM,
// as opposed to an invalid request.
var ErrNoRoom = errors.New("no room in zone")

// noRoomError is a lack-of-room failure that keeps err's message.
type noRoomError struct {
	err error
}

func (e noRoomError) Error() string { return e.err.Error() }

func (e noRoomError) Unwrap() []error { return []error{e.err, ErrNoRoom} }

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

	now func() time.Time
}

// record is what one VM holds: shared vCPUs or a pinned CPU set, and its memory.
type record struct {
	cpus   int
	cpuSet cpuset.CPUSet
	memory uint64
	// numaMemory is the VM's memory bound to each NUMA node, if any.
	numaMemory map[int]uint64
	// reservedAt is when Reserve admitted the VM; zero for a tracked VM.
	reservedAt time.Time
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
		now:          time.Now,
	}, nil
}

// Fit is what placing a VM would leave a zone with.
type Fit struct {
	// NUMANodes is how many NUMA nodes the VM would span; zero for a shared VM.
	NUMANodes int
	// Leftover is the free share of the zone's scarcer resource, CPU or memory,
	// after placing the VM: 0 when that resource is used up, 1 when the zone is empty.
	Leftover float64
}

// Reserve admits a new VM if the zone has room for it. A failure for lack of room is ErrNoRoom.
func (z *ZoneCapacity) Reserve(op *resources.VMResources) error {
	if op == nil || op.ID <= 0 {
		return errInvalidRequest
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	rec, numaNodes, err := z.admit(op)
	if err != nil {
		return err
	}

	rec.reservedAt = z.now()
	z.vms[op.ID] = rec

	if !rec.cpuSet.IsEmpty() {
		op.CPUSet = rec.cpuSet.Clone()
		op.NUMANodes = numaNodes
	}

	return nil
}

// Fit is what reserving op would leave the zone with, without reserving it.
// It fails exactly when Reserve would, except that op may have no ID yet.
func (z *ZoneCapacity) Fit(op resources.VMResources) (Fit, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	rec, numaNodes, err := z.admit(&op)
	if err != nil {
		return Fit{}, err
	}

	left := z.available(z.usageWith(op.ID, rec))

	return Fit{
		NUMANodes: len(numaNodes),
		Leftover:  scarcerShare(left.CPUs, z.sharedCapacity(usage{}), left.Memory, z.memory),
	}, nil
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

// KeepPending copies prev's pending reservations into z: reserved VMs that z has not tracked.
// A reservation older than the grace period is dropped when its VM does not exist in the cluster.
func (z *ZoneCapacity) KeepPending(prev *ZoneCapacity, exists func(id int) bool) {
	if prev == nil || prev == z {
		return
	}

	prev.mu.Lock()
	defer prev.mu.Unlock()

	z.mu.Lock()
	defer z.mu.Unlock()

	for id, rec := range prev.vms {
		if rec.reservedAt.IsZero() {
			continue
		}

		if _, ok := z.vms[id]; ok {
			continue
		}

		if z.now().Sub(rec.reservedAt) > pendingGracePeriod && !exists(id) {
			continue
		}

		z.vms[id] = rec
	}
}

// HasPending reports whether the zone holds any reservation not yet seen running.
func (z *ZoneCapacity) HasPending() bool {
	z.mu.Lock()
	defer z.mu.Unlock()

	for _, rec := range z.vms {
		if !rec.reservedAt.IsZero() {
			return true
		}
	}

	return false
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

// admit places op and checks the zone has room for it, without recording it.
func (z *ZoneCapacity) admit(op *resources.VMResources) (record, map[int]resources.NUMANodeState, error) {
	// A zero ID is a VM that Fit is asked about before it has one; Reserve rejects it.
	if op.ID < 0 || op.CPUs <= 0 || op.Memory == 0 {
		return record{}, nil, errInvalidRequest
	}

	if _, ok := z.vms[op.ID]; ok {
		return record{}, nil, fmt.Errorf("resources for VM %d are already reserved", op.ID)
	}

	cpus, numaNodes, err := z.placement.place(z.free(), op)
	if err != nil {
		return record{}, nil, noRoomError{err}
	}

	rec := newRecord(op.CPUs, cpus, op.Memory, numaNodes)

	u := z.usageWith(op.ID, rec)
	if u.memory > z.memory {
		return record{}, nil, noRoomError{fmt.Errorf("not enough memory available: requested=%d, available=%d", op.Memory, z.available(z.usage()).Memory)}
	}

	for i := range rec.numaMemory {
		if u.numaMemory[i] > z.topo.NUMANodes[i] {
			return record{}, nil, noRoomError{fmt.Errorf("not enough memory available on NUMA node %d", i)}
		}
	}

	if u.sharedCPUs > z.sharedCapacity(u) {
		return record{}, nil, noRoomError{fmt.Errorf("not enough CPUs available: requested=%d, available=%d", op.CPUs, z.available(z.usage()).CPUs)}
	}

	return rec, numaNodes, nil
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

	usable := z.allCPUs.Difference(z.reservedCPUs)

	return freeResources{
		topo:       z.topo,
		usable:     usable,
		cpus:       usable.Difference(u.pinnedCPUs),
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
