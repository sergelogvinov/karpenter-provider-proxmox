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
	"strconv"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/internal/cpumanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

var (
	// ErrNoRoom means the zone lacks the CPUs or memory for a VM. The error is wrapped with the reason.
	ErrNoRoom = errors.New("no room in zone")
	// ErrNotOffered means the policy offers no VM of this kind, such as a CPU share VM under
	// CPU overcommit. It holds for as long as the policy does.
	ErrNotOffered = errors.New("not offered under the node policy")
	// ErrTokenIgnored means Track recorded a VM but ignored a `cpushare` token next to its affinity:
	// the VM holds its affinity. It is a warning, not a failure.
	ErrTokenIgnored = errors.New("cpushare token ignored next to an affinity")
	// ErrExists means Reserve was asked for a VM ID that already has a record.
	ErrExists = errors.New("VM already has a record")
	// ErrInvalid means a request or a VM config the zone cannot use: no ID, no CPUs or memory,
	// an unparseable affinity or `cpushare` token, or a bad CPU overcommit ratio.
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

// PolicyKind is one of the node policies.
type PolicyKind int

const (
	// PolicySimple is shared placement, one shared vCPU per unpinned host CPU.
	PolicySimple PolicyKind = iota
	// PolicyStatic is pinned placement: each VM gets its own host CPUs.
	PolicyStatic
	// PolicyOvercommit is shared placement at the CPU overcommit ratio. It offers no CPU share VM,
	// and one created by hand gets shared placement instead of a seat in a CPU share group.
	PolicyOvercommit
)

// Policy is the node policy.
type Policy struct {
	Kind PolicyKind
	// CPUOvercommitRatio is how many shared vCPUs the zone offers per unpinned host CPU
	// under PolicyOvercommit; 0 means 1. The other policies ignore it.
	CPUOvercommitRatio float64
}

// cpuShareGroups reports whether CPU share VMs sit in CPU share groups under p.
func (p Policy) cpuShareGroups() bool {
	return p.Kind != PolicyOvercommit
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
	// CPUSet is the VM's host CPUs; empty for a shared or CPU share VM.
	CPUSet cpuset.CPUSet
	// NUMA maps host NUMA nodes to the guest vCPUs and memory bound to them.
	NUMA map[int]NUMABinding
	// HugePageSize > 0 means the VM is launched with hugepages of this size, in bytes:
	// a VM in a NUMA slot of a zone with a hugepage pool.
	HugePageSize uint64
	// DescriptionTokens are written into the VM description, e.g. "affinity=8-15" or "cpushare=2".
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
	// numaMemory is each NUMA node's memory left after its even share of the host reserve,
	// or its hugepage pool.
	numaMemory map[int]uint64
	// slots are the zone's NUMA slots, fixed by the topology and the host reserve.
	slots []slot
	// hugePageSize is the page size of the zone's hugepage pool; 0 without one.
	hugePageSize uint64

	vms map[int]record
}

// record is what one VM holds: shared vCPUs, host CPUs or a seat in a CPU share group,
// and its memory. It holds no policy.
type record struct {
	cpus   int
	cpuSet cpuset.CPUSet
	// share is the seats of the CPU share group the VM sits in; 0 for any other VM.
	share  int
	memory uint64
	// numaMemory is the VM's memory bound to each NUMA node, if any.
	numaMemory map[int]uint64
}

// usage is the sum of all records.
type usage struct {
	pinnedCPUs cpuset.CPUSet
	sharedCPUs int
	// cpuShareCPUs are the host CPUs the CPU share groups take.
	cpuShareCPUs int
	memory       uint64
	numaMemory   map[int]uint64
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

	// The host reserve is split evenly across the NUMA nodes.
	numaMemory := make(map[int]uint64, len(topo.NUMANodes))
	for i, mem := range topo.NUMANodes {
		share := reservedMemory / uint64(len(topo.NUMANodes))
		numaMemory[i] = mem - min(mem, share)
	}

	allCPUs := topo.CPUDetails.CPUs()

	return &Zone{
		topo:         topo,
		allCPUs:      allCPUs,
		reservedCPUs: h.ReservedCPUs.Clone(),
		memory:       topo.TotalMemory - reservedMemory,
		numaMemory:   numaMemory,
		slots:        carveSlots(topo, allCPUs.Difference(h.ReservedCPUs), numaMemory),
		hugePageSize: h.HugePageSize,
		vms:          map[int]record{},
	}, nil
}

// Track records a VM that already runs in the zone, replacing any earlier record.
// It never fails on budget. The node-capacity guest is never recorded.
func (z *Zone) Track(vm VM) error {
	if vm.ID <= 0 {
		return fmt.Errorf("%w: a VM ID is required", ErrInvalid)
	}

	c, err := vm.classify()

	z.mu.Lock()
	defer z.mu.Unlock()

	if c.class == classIgnored {
		delete(z.vms, vm.ID)

		return nil
	}

	// An unreadable affinity or token still runs: classify holds its vCPUs and memory as shared,
	// so the zone is not overcommitted. An ignored token is only a warning: the VM holds its affinity.
	z.vms[vm.ID] = record{cpus: vm.CPUs, cpuSet: c.cpus, share: c.share, memory: vm.Memory, numaMemory: maps.Clone(vm.NUMAMemory)}

	return err
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

	if rec.share > 0 {
		return Grant{DescriptionTokens: []string{cpuShareToken + strconv.Itoa(rec.share)}}, nil
	}

	if rec.cpuSet.IsEmpty() {
		return Grant{}, nil
	}

	grant := Grant{
		CPUSet:            rec.cpuSet.Clone(),
		NUMA:              numa,
		DescriptionTokens: []string{affinityToken + rec.cpuSet.String()},
	}

	if vm.Slot != nil {
		grant.HugePageSize = z.hugePageSize
	}

	return grant, nil
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

	left := z.available(z.usageWith(p, vm.ID, rec), ratio)

	return Fit{
		NUMANodes: len(numa),
		Leftover:  scarcerShare(left.CPUs, z.sharedCapacity(usage{}, ratio), left.Memory, z.memory),
	}, nil
}

// HugePageSize is hugePageSize, in bytes.
func (z *Zone) HugePageSize() uint64 {
	return z.hugePageSize
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

	return z.available(z.usage(p), ratio)
}

// CPUShareCPUs is how many host CPUs accounting charges for the zone's CPU share groups
// under p: none under CPU overcommit, which gives CPU share VMs shared placement.
func (z *Zone) CPUShareCPUs(p Policy) int {
	z.mu.Lock()
	defer z.mu.Unlock()

	return z.usage(p).cpuShareCPUs
}

// ratio is how many shared vCPUs the zone offers per unpinned host CPU under p.
func (z *Zone) ratio(p Policy) (float64, error) {
	ratio := p.CPUOvercommitRatio
	if p.Kind != PolicyOvercommit || ratio == 0 {
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

	share, err := vm.cpuShare()
	if err != nil {
		return record{}, nil, err
	}

	if share > 0 && !p.cpuShareGroups() {
		return record{}, nil, fmt.Errorf("%w: CPU share VM %d under CPU overcommit", ErrNotOffered, vm.ID)
	}

	if err := vm.checkSlot(p, share); err != nil {
		return record{}, nil, err
	}

	rec := record{cpus: vm.CPUs, cpuSet: cpuset.New(), share: share, memory: vm.Memory}

	var numa map[int]NUMABinding

	if p.Kind == PolicyStatic && share == 0 {
		if vm.Slot != nil {
			rec.cpuSet, numa, err = placeSlot(z.free(z.usage(p)), *vm.Slot)
		} else {
			rec.cpuSet, numa, err = placePinned(z.free(z.usage(p)), vm.CPUs, vm.Memory)
		}

		if err != nil {
			return record{}, nil, noRoomError{err}
		}

		rec.numaMemory = Grant{NUMA: numa}.NUMAMemory()
	}

	u := z.usageWith(p, vm.ID, rec)
	if u.memory > z.memory {
		return record{}, nil, noRoomError{fmt.Errorf("not enough memory available: requested=%d, available=%d", vm.Memory, z.available(z.usage(p), ratio).Memory)}
	}

	for i := range rec.numaMemory {
		if u.numaMemory[i] > z.numaMemory[i] {
			return record{}, nil, noRoomError{fmt.Errorf("not enough memory available on NUMA node %d", i)}
		}
	}

	if z.unpinned(u) < u.cpuShareCPUs || u.sharedCPUs > z.sharedCapacity(u, ratio) {
		return record{}, nil, noRoomError{fmt.Errorf("not enough CPUs available: requested=%d, available=%d", vm.CPUs, z.available(z.usage(p), ratio).CPUs)}
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

// sharedCapacity is how many shared vCPUs the unpinned, unreserved CPUs offer
// once the CPU share groups have taken theirs.
func (z *Zone) sharedCapacity(u usage, ratio float64) int {
	return int(math.Floor(float64(max(0, z.unpinned(u)-u.cpuShareCPUs)) * ratio))
}

// unpinned is how many unreserved CPUs no VM is pinned to.
func (z *Zone) unpinned(u usage) int {
	return z.allCPUs.Difference(z.reservedCPUs).Difference(u.pinnedCPUs).Size()
}

// free is what usage u leaves the zone for a new pinned VM.
func (z *Zone) free(u usage) freeResources {
	zoneFree := z.freeMemory(u)

	numaMemory := make(map[int]uint64, len(z.numaMemory))
	for i, mem := range z.numaMemory {
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
		slots:      z.slots,
	}
}

// usage is the sum of all records under p. Without CPU share groups, a CPU share VM gets shared placement.
func (z *Zone) usage(p Policy) usage {
	u := usage{pinnedCPUs: cpuset.New(), numaMemory: map[int]uint64{}}

	seated := map[cpumanager.CPUShareClass]int{}

	for _, rec := range z.vms {
		switch {
		case rec.share > 0 && p.cpuShareGroups():
			seated[cpumanager.CPUShareClass{Seats: rec.share, VCPUs: rec.cpus}]++
		case rec.cpuSet.IsEmpty():
			u.sharedCPUs += rec.cpus
		default:
			u.pinnedCPUs = u.pinnedCPUs.Union(rec.cpuSet)
		}

		u.memory += rec.memory

		for i, mem := range rec.numaMemory {
			u.numaMemory[i] += mem
		}
	}

	u.pinnedCPUs = u.pinnedCPUs.Intersection(z.allCPUs).Difference(z.reservedCPUs)
	u.cpuShareCPUs = cpumanager.CPUShareCPUs(seated)

	return u
}

// usageWith is the usage under p if VM id held rec instead of its current record.
func (z *Zone) usageWith(p Policy, id int, rec record) usage {
	prev, ok := z.vms[id]
	z.vms[id] = rec

	u := z.usage(p)

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
