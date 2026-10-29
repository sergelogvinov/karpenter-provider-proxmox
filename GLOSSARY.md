# Karpenter Provider Proxmox

Provisions Kubernetes nodes as Proxmox virtual machines on demand.

## Node IPAM

**IP pool**:
The set of addresses in one subnet that can be handed to virtual machines, plus a record of which are taken.
_Avoid_: subnet (when the tracking state is meant), IPAM

**Reserved address**:
An address in an IP pool that is never handed to a virtual machine and cannot be released: the network address, the subnet's gateway, and for IPv4 also the first host address and the broadcast address.
_Avoid_: occupied (for these), excluded

**Occupied address**:
An address in an IP pool that a node or virtual machine currently holds; it returns to the pool when released.
_Avoid_: used, allocated (for externally-assigned addresses)

**Allocate**:
To pick the next free address in an IP pool for a new virtual machine and mark it occupied.
_Avoid_: occupy (for this), next

**Allocation hint**:
An address already owned by something else (typically the Proxmox node's own IP) after which allocation starts searching.
_Avoid_: start IP, offset

## Instance templates

**Template config**:
The Proxmox VM configuration Karpenter renders from a ProxmoxTemplate spec. It has a create form (adds the boot disk, TPM and EFI disks) and an update form (adds deletes for managed options the spec no longer renders).
_Avoid_: VM options, template options

**Managed option**:
A template config key family (`net`, `ipconfig`, `serial`, `nameserver`, ...) that Karpenter owns outright; an in-place update deletes any key of the family that the spec does not render, including ones added by hand.
_Avoid_: removable option

**Template hash**:
The hash of a ProxmoxTemplate's rebuild fields, written into the VM template's description after `Hash: `; a template whose hash differs from the spec is replaced, not updated.
_Avoid_: image hash, template version

**Rebuild field**:
A ProxmoxTemplate spec field covered by the template hash (source image, storage IDs, BIOS, TPM); changing one replaces the VM template.
_Avoid_: immutable field

**In-place field**:
A ProxmoxTemplate spec field outside the template hash; changing one updates the existing VM template's config.
_Avoid_: mutable field

**Template lifecycle**:
The stages a ProxmoxTemplate's VM templates go through: created on each zone, replaced when a rebuild field changes, updated in place when only in-place fields change, removed from zones the spec no longer targets, and deleted with the ProxmoxTemplate. When one change touches both kinds of field, the replacement already carries the in-place fields.
_Avoid_: template reconcile, template sync

**Template placement**:
One VM template on one zone of one region, stored in status fields as `region/zone/vmid` (`ProxmoxTemplate.status.zones`, `ProxmoxUnmanagedTemplate.status.zones`, `ProxmoxNodeClass.status.selectedZones`). Region and zone are required and vmid is positive; entries that don't parse are skipped.
_Avoid_: zone (for the whole triple), template location

## Cloud capacity

**Zone**:
One Proxmox node, as Karpenter sees it for placement.
_Avoid_: host (for this)

**Zone capacity**:
The CPU and memory one zone can give to virtual machines, plus a record for each virtual machine of what it holds there.
_Avoid_: resource manager, ledger, node capacity

**Reserve**:
To admit a new virtual machine into a zone's capacity, but only if the zone has enough room left for it.
_Avoid_: allocate (for this)

**Track**:
To record a virtual machine that already runs in a zone, even if the zone is then over its limits.
_Avoid_: allocate-or-update, observe, adopt

**Pending reservation**:
A reservation whose virtual machine has not yet been seen running in its zone. It keeps holding capacity across capacity syncs, including syncs that find its zone missing, until the virtual machine is seen running and tracked, is released, or never appears in the cluster within a grace period.
_Avoid_: in-flight reservation, carry-over

**Release**:
To forget a virtual machine's record and return what it held to the zone's capacity. Releasing a virtual machine that has no record changes nothing.

**Shared placement**:
A virtual machine's vCPUs run on any of the zone's ungrouped CPUs, together with other virtual machines.
_Avoid_: simple policy

**Pinned placement**:
A virtual machine gets its own set of host CPUs aligned to the NUMA layout, and its memory is split across the NUMA nodes those CPUs belong to.
_Avoid_: static policy

**Constrained VM**:
A virtual machine whose affinity has a different number of host CPUs than it has vCPUs; its threads are masked to the affinity instead of pinned one per CPU. Its CPUs count as pinned.
_Avoid_: masked VM

**Unpinned CPUs**:
A zone's CPUs that are neither reserved for the host nor held by a pinned or constrained virtual machine; where the CPU share groups and the virtual machines under shared placement run.
_Avoid_: shared pool, shared CPUs, free CPUs

**Partition**:
The scheduler's division of the ungrouped CPUs among the virtual machines under shared placement, one cpuset each (`--shared-policy=partition`). Cpusets are disjoint while the ungrouped CPUs suffice; when oversubscribed they overlap, but only within one NUMA node.
_Avoid_: shared pool, slice

**Plan**:
Where a zone's running virtual machines' CPUs go, worked out from the zone's records and the cpusets the scheduler has applied: the virtual machines whose cpuset must change and how their threads are placed, plus the CPUs left busy or idle.
_Avoid_: rebalance (for the result), schedule

**Applied cpuset**:
The cpuset the scheduler last set on a virtual machine's threads, recorded only after `taskset` succeeded; a new process starts with none.
_Avoid_: current affinity, assigned set

**NUMA slot**:
An aligned whole, half or quarter of one NUMA node's usable CPUs and memory; smaller slots nest inside larger ones.
_Avoid_: partition, chunk

**Slot fraction**:
The number a NUMA node is divided by to form its NUMA slots: 1, 2 or 4.
_Avoid_: numa size, split

**Slot shape**:
The CPU and memory size of a NUMA slot on a given NUMA node.
_Avoid_: size class

**NUMA template**:
An instance types file entry with a slot fraction (`numa`); its name is the family of the NUMA types generated from it, which take their CPUs and memory from the slot and the rest from the template.
_Avoid_: numa instance type, slot template

**NUMA type**:
An instance type generated from a NUMA template for one slot shape, named `<family>.<vCPU>VCPU-<memory>GB` and sized exactly as the slot.
_Avoid_: slot type, NUMA instance

**Kept NUMA type**:
A NUMA type that stays listed, with unavailable offerings, in a zone or capacity type it is no longer generated for, because a NodeClaim there still uses it; when its slot shape is gone from every zone it is rebuilt from its name and its family's NUMA template. It is dropped with its last NodeClaim.
_Avoid_: stale type, orphan type

**Slot offer**:
A zone's slot shape together with how many free NUMA slots of that shape it has; a NUMA type is available in a zone when its slot offer has at least one.
_Avoid_: slot availability, free slots (for the pair)

**Strict slot allocation**:
Reserving a NUMA type's virtual machine in the lowest free NUMA slot of its slot shape (lowest NUMA node, then lowest slot), pinned 1:1 to the slot's CPUs with one guest NUMA node bound to the slot's NUMA node, and with hugepages when the zone has a hugepage pool. Without a free slot the reservation fails; it never falls back to CPUs outside a slot.
_Avoid_: slot reserve, aligned placement

**CPU share group**:
A set of host CPUs with a fixed number of seats, used only by the virtual machines seated in it; every member has the same vCPU count and sees all of the group's CPUs.
_Avoid_: shared pool, shared placement, share group

**Ungrouped CPUs**:
A zone's unpinned CPUs that no CPU share group holds; where virtual machines under shared placement run.
_Avoid_: shared pool, shared CPUs, floating CPUs

**CPU share VM**:
A virtual machine that sits in a CPU share group instead of holding CPUs of its own.
_Avoid_: shared VM, fractional VM

**CPU overcommit ratio**:
How many shared vCPUs a zone offers for each unpinned host CPU; 1.0 means no overcommit.
_Avoid_: oversubscription factor

**Hugepage pool**:
The memory a zone holds as preallocated hugepages of one size, per NUMA node; when a zone has one, the node-capacity guest's NUMA memory reports the pool, not ordinary memory.
_Avoid_: hugepages memory, reserved memory

**Fit**:
What placing a virtual machine in a zone would leave the zone with, worked out without reserving: how many NUMA nodes the virtual machine would span, and what share of the zone's scarcer resource, CPU or memory, would stay free. Spanning fewer NUMA nodes is always the better fit.
_Avoid_: score, load

**Placement strategy**:
A node class's rule for ordering zones of equal NUMA span by fit: best fit puts the virtual machine where the least is left over, balanced where the most is left over, and availability first in random order.

**Zone choice**:
Picking, among a launch's candidate zones, the one whose fit best suits the placement strategy, and reserving the virtual machine there in the same step.
_Avoid_: zone selection, zone sorting, best zone

## Instances

**Launch**:
Creating one virtual machine for a NodeClaim in one zone, from reserving zone capacity to starting the virtual machine. Creating a node tries launches zone by zone until one succeeds.
_Avoid_: provision, instance create

**Launch rollback**:
Undoing every side effect of a failed launch (the zone capacity reservation, the virtual machine, occupied addresses, the cloud-init ISO), newest first, even when the launch was cancelled. The bootstrap token is not undone; it expires on its own.
_Avoid_: cleanup, revert
