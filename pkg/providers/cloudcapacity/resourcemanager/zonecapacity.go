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
	"strings"
	"sync"
	"time"

	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	"k8s.io/utils/cpuset"
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
// for pinned placement its host CPUs and NUMA split, and the description tokens to write.
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
func (z *ZoneCapacity) Track(vm zonecapacity.VM) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	if err := z.zone.Track(vm); err != nil {
		return err
	}

	z.tracked[vm.ID] = true
	delete(z.pending, vm.ID)

	return nil
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

// VMFromConfig is a running VM as the zone reads it: a field copy of its listing entry and config.
// The zone parses the affinity and the description; only the numaN memory is summed per host node,
// split evenly across a numaN entry's host nodes.
func VMFromConfig(vmr *cluster.Resource, cfg *qemu.Config) (zonecapacity.VM, error) {
	if vmr == nil || cfg == nil {
		return zonecapacity.VM{}, fmt.Errorf("virtual machine resource and config cannot be nil")
	}

	vm := zonecapacity.VM{
		ID:          vmr.VMID,
		Name:        vmr.Name,
		CPUs:        vmr.MaxCPU,
		Memory:      uint64(vmr.MaxMem),
		Affinity:    cfg.Affinity,
		Description: cfg.Description,
		Tags:        vmr.Tags,
	}

	if cfg.Name != "" {
		vm.Name = cfg.Name
	}

	if cfg.Tags != nil {
		vm.Tags = *cfg.Tags
	}

	for idx, n := range cfg.NUMA {
		if n.Memory == nil || *n.Memory <= 0 || len(n.HostNodes) == 0 {
			continue
		}

		hostNodes, err := cpuset.Parse(strings.Join(n.HostNodes, ","))
		if err != nil || hostNodes.IsEmpty() {
			return zonecapacity.VM{}, fmt.Errorf("failed to parse host nodes of NUMA node numa%d: %v", idx, n.HostNodes)
		}

		if vm.NUMAMemory == nil {
			vm.NUMAMemory = map[int]uint64{}
		}

		for _, node := range hostNodes.List() {
			vm.NUMAMemory[node] += uint64(*n.Memory) / uint64(hostNodes.Size()) * zonecapacity.MiB
		}
	}

	return vm, nil
}

// vmFromRequest is a VM request as the zone takes it.
func vmFromRequest(op resources.VMResources) zonecapacity.VM {
	return zonecapacity.VM{ID: op.ID, CPUs: op.CPUs, Memory: op.Memory}
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

// applyGrant writes grant into op: host CPUs, the NUMA split with guest vCPU ranges and MiB, and the tokens.
func applyGrant(op *resources.VMResources, grant zonecapacity.Grant) {
	op.DescriptionTokens = grant.DescriptionTokens

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
