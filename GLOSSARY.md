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
A virtual machine's vCPUs run on any of the zone's unpinned CPUs, together with other virtual machines.
_Avoid_: simple policy

**Pinned placement**:
A virtual machine gets its own set of host CPUs aligned to the NUMA layout, and its memory is split across the NUMA nodes those CPUs belong to.
_Avoid_: static policy

**CPU overcommit ratio**:
How many shared vCPUs a zone offers for each unpinned host CPU; 1.0 means no overcommit.
_Avoid_: oversubscription factor
