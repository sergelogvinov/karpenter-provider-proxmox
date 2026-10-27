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

**Template placement**:
One VM template on one zone of one region, stored in status fields as `region/zone/vmid` (`ProxmoxTemplate.status.zones`, `ProxmoxUnmanagedTemplate.status.zones`, `ProxmoxNodeClass.status.selectedZones`). Region and zone are required and vmid is positive; entries that don't parse are skipped.
_Avoid_: zone (for the whole triple), template location
