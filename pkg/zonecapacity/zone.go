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
	"errors"
	"fmt"
	"maps"
	"math"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

var (
	// ErrNoRoom means the zone lacks the CPUs or memory for a VM. The error is wrapped with the reason.
	ErrNoRoom = errors.New("no room in zone")
	// ErrExists means Reserve was asked for a VM ID that already has a record.
	ErrExists = errors.New("VM already has a record")
	// ErrInvalid means a request or a VM config the zone cannot use: no ID, no CPUs or memory,
	// an unparseable affinity, or a bad CPU overcommit ratio.
	ErrInvalid = errors.New("invalid VM or policy")
)

// errRequired is the reason a request without a VM ID, CPUs or memory is ErrInvalid.
const errRequired = "a VM ID, CPUs and memory are required"

// noRoomError is a lack-of-room failure that keeps err's message.
type noRoomError struct {
	err error
}

func (e noRoomError) Error() string { return e.err.Error() }

func (e noRoomError) Unwrap() []error { return []error{e.err, ErrNoRoom} }

// Host is the zone's fixed shape. The node policy is not part of it.
type Host struct {
	Topology     *topology.Topology
	ReservedCPUs cpuset.CPUSet
	// ReservedMemory is held back for the host; ignored when HugePageSize > 0.
	ReservedMemory uint64
	// HugePageSize > 0 means each NUMA node's memory in Topology is its hugepage pool.
	HugePageSize uint64
}

// Policy is the node policy.
type Policy struct {
	// Pinned selects pinned placement: each VM gets its own host CPUs.
	Pinned bool
	// CPUOvercommitRatio is how many shared vCPUs the zone offers per unpinned host CPU
	// under shared placement; 0 means 1.
	CPUOvercommitRatio float64
}

// Capacity is what a new shared VM could still get in a zone.
type Capacity struct {
	CPUs   int
	Memory uint64
}

// Fit is what placing a VM would leave a zone with.
type Fit struct {
	// NUMANodes is how many NUMA nodes the VM would span; zero for a shared VM.
	NUMANodes int
	// Leftover is the free share of the zone's scarcer resource, CPU or memory,
	// after placing the VM: 0 when that resource is used up, 1 when the zone is empty.
	Leftover float64
}

// NUMABinding is one guest NUMA node bound to a host NUMA node.
type NUMABinding struct {
	// VCPUs are the guest vCPUs on the node, numbered from the VM's point of view.
	VCPUs cpuset.CPUSet
	// Memory is the guest memory bound to the node, in bytes.
	Memory uint64
}

// Grant is what Reserve gave a VM, ready to write into its config at launch.
type Grant struct {
	// CPUSet is the VM's host CPUs; empty for a shared VM.
	CPUSet cpuset.CPUSet
	// NUMA maps host NUMA nodes to the guest vCPUs and memory bound to them.
	NUMA map[int]NUMABinding
	// DescriptionTokens are written into the VM description, e.g. "affinity=8-15".
	DescriptionTokens []string
}

// NUMAMemory is the memory the grant binds to each host NUMA node, in bytes; nil without a binding.
func (g Grant) NUMAMemory() map[int]uint64 {
	if len(g.NUMA) == 0 {
		return nil
	}

	mem := make(map[int]uint64, len(g.NUMA))
	for node, b := range g.NUMA {
		mem[node] = b.Memory
	}

	return mem
}

// Zone is the capacity of one zone: a record per VM, and the rules to place new ones.
// Usage is always recomputed from the records, so tracking, reserving or releasing
// the same VM any number of times never drifts. It is safe for concurrent use.
type Zone struct {
	mu sync.Mutex

	topo         *topology.Topology
	allCPUs      cpuset.CPUSet
	reservedCPUs cpuset.CPUSet
	// memory is the zone memory left after the host reserve.
	memory uint64

	vms map[int]record
}

// record is what one VM holds: shared vCPUs or host CPUs, and its memory. It holds no policy.
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

// New returns an empty zone on h.
func New(h Host) (*Zone, error) {
	topo := h.Topology
	if topo == nil {
		return nil, fmt.Errorf("%w: system topology must be provided", ErrInvalid)
	}

	if topo.NumCPUs < h.ReservedCPUs.Size() {
		return nil, fmt.Errorf("%w: not enough CPUs available: maxCPUs=%d, reservedCPUs=%d", ErrInvalid, topo.NumCPUs, h.ReservedCPUs.Size())
	}

	reservedMemory := h.ReservedMemory
	if h.HugePageSize > 0 {
		reservedMemory = 0
	}

	if reservedMemory >= topo.TotalMemory {
		return nil, fmt.Errorf("%w: reserved memory %d must be less than max memory %d", ErrInvalid, reservedMemory, topo.TotalMemory)
	}

	return &Zone{
		topo:         topo,
		allCPUs:      topo.CPUDetails.CPUs(),
		reservedCPUs: h.ReservedCPUs.Clone(),
		memory:       topo.TotalMemory - reservedMemory,
		vms:          map[int]record{},
	}, nil
}

// Track records a VM that already runs in the zone, replacing any earlier record.
// It never fails on budget. The node-capacity guest is never recorded.
func (z *Zone) Track(vm VM) error {
	if vm.ID <= 0 {
		return fmt.Errorf("%w: a VM ID is required", ErrInvalid)
	}

	c, cpus, err := vm.classify()
	if err != nil {
		return err
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	if c == classIgnored {
		delete(z.vms, vm.ID)

		return nil
	}

	z.vms[vm.ID] = record{cpus: vm.CPUs, cpuSet: cpus, memory: vm.Memory, numaMemory: maps.Clone(vm.NUMAMemory)}

	return nil
}

// Release forgets VM id. Releasing a VM without a record changes nothing.
func (z *Zone) Release(id int) {
	z.mu.Lock()
	defer z.mu.Unlock()

	delete(z.vms, id)
}

// Reserve admits a new VM if the zone has room for it under p, and returns where it goes.
// A failure for lack of room is ErrNoRoom.
func (z *Zone) Reserve(p Policy, vm VM) (Grant, error) {
	if vm.ID <= 0 {
		return Grant{}, fmt.Errorf("%w: %s", ErrInvalid, errRequired)
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	rec, numa, err := z.admit(p, vm)
	if err != nil {
		return Grant{}, err
	}

	z.vms[vm.ID] = rec

	if rec.cpuSet.IsEmpty() {
		return Grant{}, nil
	}

	return Grant{
		CPUSet:            rec.cpuSet.Clone(),
		NUMA:              numa,
		DescriptionTokens: []string{affinityToken + rec.cpuSet.String()},
	}, nil
}

// Fit is what reserving vm under p would leave the zone with, without reserving it.
// It fails exactly when Reserve would, except that vm may have no ID yet.
func (z *Zone) Fit(p Policy, vm VM) (Fit, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	rec, numa, err := z.admit(p, vm)
	if err != nil {
		return Fit{}, err
	}

	ratio, err := z.ratio(p)
	if err != nil {
		return Fit{}, err
	}

	left := z.available(z.usageWith(vm.ID, rec), ratio)

	return Fit{
		NUMANodes: len(numa),
		Leftover:  scarcerShare(left.CPUs, z.sharedCapacity(usage{}, ratio), left.Memory, z.memory),
	}, nil
}

// Available is what a new shared VM could still get under p, clamped at zero.
// A bad CPU overcommit ratio offers nothing.
func (z *Zone) Available(p Policy) Capacity {
	ratio, err := z.ratio(p)
	if err != nil {
		return Capacity{}
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	return z.available(z.usage(), ratio)
}

// ratio is how many shared vCPUs the zone offers per unpinned host CPU under p.
func (z *Zone) ratio(p Policy) (float64, error) {
	ratio := p.CPUOvercommitRatio
	if p.Pinned || ratio == 0 {
		return 1, nil
	}

	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 1 {
		return 0, fmt.Errorf("%w: CPU overcommit ratio %v must be a finite number greater than or equal to 1", ErrInvalid, ratio)
	}

	if float64(z.topo.NumCPUs)*ratio >= float64(math.MaxInt) {
		return 0, fmt.Errorf("%w: CPU overcommit ratio %v exceeds the supported CPU budget", ErrInvalid, ratio)
	}

	return ratio, nil
}

// admit places vm under p and checks the zone has room for it, without recording it.
func (z *Zone) admit(p Policy, vm VM) (record, map[int]NUMABinding, error) {
	// A zero ID is a VM that Fit is asked about before it has one; Reserve rejects it.
	if vm.ID < 0 || vm.CPUs <= 0 || vm.Memory == 0 {
		return record{}, nil, fmt.Errorf("%w: %s", ErrInvalid, errRequired)
	}

	ratio, err := z.ratio(p)
	if err != nil {
		return record{}, nil, err
	}

	if _, ok := z.vms[vm.ID]; ok {
		return record{}, nil, fmt.Errorf("resources for VM %d are already reserved: %w", vm.ID, ErrExists)
	}

	rec := record{cpus: vm.CPUs, cpuSet: cpuset.New(), memory: vm.Memory}

	var numa map[int]NUMABinding

	if p.Pinned {
		rec.cpuSet, numa, err = placePinned(z.free(), vm.CPUs, vm.Memory)
		if err != nil {
			return record{}, nil, noRoomError{err}
		}

		rec.numaMemory = Grant{NUMA: numa}.NUMAMemory()
	}

	u := z.usageWith(vm.ID, rec)
	if u.memory > z.memory {
		return record{}, nil, noRoomError{fmt.Errorf("not enough memory available: requested=%d, available=%d", vm.Memory, z.available(z.usage(), ratio).Memory)}
	}

	for i := range rec.numaMemory {
		if u.numaMemory[i] > z.topo.NUMANodes[i] {
			return record{}, nil, noRoomError{fmt.Errorf("not enough memory available on NUMA node %d", i)}
		}
	}

	if u.sharedCPUs > z.sharedCapacity(u, ratio) {
		return record{}, nil, noRoomError{fmt.Errorf("not enough CPUs available: requested=%d, available=%d", vm.CPUs, z.available(z.usage(), ratio).CPUs)}
	}

	return rec, numa, nil
}

func (z *Zone) available(u usage, ratio float64) Capacity {
	return Capacity{
		CPUs:   max(0, z.sharedCapacity(u, ratio)-u.sharedCPUs),
		Memory: z.freeMemory(u),
	}
}

// freeMemory is the zone memory u leaves free, clamped at zero.
func (z *Zone) freeMemory(u usage) uint64 {
	if u.memory < z.memory {
		return z.memory - u.memory
	}

	return 0
}

// sharedCapacity is how many shared vCPUs the unpinned, unreserved CPUs offer.
func (z *Zone) sharedCapacity(u usage, ratio float64) int {
	unpinned := z.allCPUs.Difference(z.reservedCPUs).Difference(u.pinnedCPUs).Size()

	return int(math.Floor(float64(unpinned) * ratio))
}

// free is what the zone has left for a new VM.
func (z *Zone) free() freeResources {
	u := z.usage()

	zoneFree := z.freeMemory(u)

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

func (z *Zone) usage() usage {
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
func (z *Zone) usageWith(id int, rec record) usage {
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

// scarcerShare is the smaller of the free CPU share and the free memory share.
func scarcerShare(freeCPUs, cpus int, freeMemory, memory uint64) float64 {
	if cpus <= 0 || memory == 0 {
		return 0
	}

	return min(float64(freeCPUs)/float64(cpus), float64(freeMemory)/float64(memory))
}
