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
	"sync"
	"time"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
)

// pendingGracePeriod is how long a pending reservation outlives a VM that never appears in the cluster.
const pendingGracePeriod = 10 * time.Minute

// ZoneCapacity is one zone's capacity under the controller's node policy, plus its
// pending reservations: VMs reserved here and not yet seen running.
type ZoneCapacity struct {
	mu sync.Mutex

	zone   *zonecapacity.Zone
	policy zonecapacity.Policy

	// tracked are the VMs seen running in the zone.
	tracked map[int]bool
	// pending are the reservations whose VMs have not been seen running yet.
	pending map[int]pendingReservation

	now func() time.Time
}

// pendingReservation is what Reserve asked for and got, and when.
type pendingReservation struct {
	vm         zonecapacity.VM
	grant      zonecapacity.Grant
	reservedAt time.Time
}

// NewZoneCapacity returns an empty zone on host that places new VMs under policy.
func NewZoneCapacity(host zonecapacity.Host, policy zonecapacity.Policy) (*ZoneCapacity, error) {
	zone, err := zonecapacity.New(host)
	if err != nil {
		return nil, err
	}

	return &ZoneCapacity{
		zone:    zone,
		policy:  policy,
		tracked: map[int]bool{},
		pending: map[int]pendingReservation{},
		now:     time.Now,
	}, nil
}

// Reserve admits a new VM if the zone has room for it, and fills op with where it goes:
// for pinned placement its host CPUs and NUMA split, the hugepage size of a VM in a NUMA slot,
// and the description tokens to write (`cpushare=N` for a CPU share VM, which gets no CPUs
// and no NUMA binding).
// A failure for lack of room is zonecapacity.ErrNoRoom.
func (z *ZoneCapacity) Reserve(op *resources.VMResources) error {
	if op == nil {
		return fmt.Errorf("%w: a VM request is required", zonecapacity.ErrInvalid)
	}

	z.mu.Lock()
	defer z.mu.Unlock()

	vm := vmFromRequest(*op)

	grant, err := z.zone.Reserve(z.policy, vm)
	if err != nil {
		return err
	}

	z.pending[op.ID] = pendingReservation{vm: vm, grant: grant, reservedAt: z.now()}

	applyGrant(op, grant)

	return nil
}

// Fit is what reserving op would leave the zone with, without reserving it.
// It fails exactly when Reserve would, except that op may have no ID yet.
func (z *ZoneCapacity) Fit(op resources.VMResources) (zonecapacity.Fit, error) {
	return z.zone.Fit(z.policy, vmFromRequest(op))
}

// Track records a VM that runs in the zone, replacing any earlier record or reservation.
// An error wrapping zonecapacity.ErrTokenIgnored is a warning: the VM is tracked.
func (z *ZoneCapacity) Track(vm zonecapacity.VM) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	err := z.zone.Track(vm)
	if err != nil && !errors.Is(err, zonecapacity.ErrTokenIgnored) {
		return err
	}

	z.tracked[vm.ID] = true
	delete(z.pending, vm.ID)

	return err
}

// Release forgets VM id. Releasing a VM without a record changes nothing.
func (z *ZoneCapacity) Release(id int) {
	z.mu.Lock()
	defer z.mu.Unlock()

	z.zone.Release(id)
	delete(z.tracked, id)
	delete(z.pending, id)
}

// Available is what a new shared VM could still get, clamped at zero.
func (z *ZoneCapacity) Available() zonecapacity.Capacity {
	return z.zone.Available(z.policy)
}

// Pinned reports whether the zone places new VMs under pinned placement.
func (z *ZoneCapacity) Pinned() bool {
	return z.policy.Kind == zonecapacity.PolicyStatic
}

// HugePageSize is the zone's, see zonecapacity.Zone.
func (z *ZoneCapacity) HugePageSize() uint64 {
	return z.zone.HugePageSize()
}

// Slots is every NUMA slot shape of the zone with its free slots; nil under shared placement.
func (z *ZoneCapacity) Slots() []zonecapacity.SlotOffer {
	return z.zone.Slots(z.policy)
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

	for id, p := range prev.pending {
		if z.tracked[id] {
			continue
		}

		if _, ok := z.pending[id]; ok {
			continue
		}

		if z.now().Sub(p.reservedAt) > pendingGracePeriod && !exists(id) {
			continue
		}

		// The reservation is tracked as the VM its grant will launch, so it keeps its CPUs.
		if err := z.zone.Track(vmFromGrant(p.vm, p.grant)); err != nil {
			continue
		}

		z.pending[id] = p
	}
}

// HasPending reports whether the zone holds any reservation not yet seen running.
func (z *ZoneCapacity) HasPending() bool {
	z.mu.Lock()
	defer z.mu.Unlock()

	return len(z.pending) > 0
}

// String describes the zone's capacity for logs.
func (z *ZoneCapacity) String() string {
	z.mu.Lock()
	defer z.mu.Unlock()

	free := z.zone.Available(z.policy)

	return fmt.Sprintf("VMs: %d, Pending: %d, CPU: Free: %d, Mem: %dM",
		len(z.tracked), len(z.pending), free.CPUs, free.Memory/zonecapacity.MiB)
}

// vmFromRequest is a VM request as the zone takes it.
func vmFromRequest(op resources.VMResources) zonecapacity.VM {
	return zonecapacity.VM{ID: op.ID, CPUs: op.CPUs, Memory: op.Memory, CPUShare: op.CPUShare, Slot: op.Slot}
}

// vmFromGrant is the VM a launch writes from vm's grant, as the zone reads it back.
func vmFromGrant(vm zonecapacity.VM, grant zonecapacity.Grant) zonecapacity.VM {
	if !grant.CPUSet.IsEmpty() {
		vm.Affinity = grant.CPUSet.String()
	}

	if mem := grant.NUMAMemory(); mem != nil {
		vm.NUMAMemory = mem
	}

	return vm
}

// applyGrant writes grant into op: host CPUs, the NUMA split with guest vCPU ranges and MiB, the hugepage
// size, and the tokens.
func applyGrant(op *resources.VMResources, grant zonecapacity.Grant) {
	op.DescriptionTokens = grant.DescriptionTokens
	op.HugePageSize = grant.HugePageSize

	if grant.CPUSet.IsEmpty() {
		return
	}

	op.CPUSet = grant.CPUSet.Clone()
	op.NUMANodes = make(map[int]resources.NUMANodeState, len(grant.NUMA))

	for node, b := range grant.NUMA {
		vcpus := b.VCPUs.List()

		op.NUMANodes[node] = resources.NUMANodeState{
			CPUs:   fmt.Sprintf("%d-%d", vcpus[0], vcpus[len(vcpus)-1]),
			Memory: b.Memory / zonecapacity.MiB,
			Policy: "bind",
		}
	}
}
