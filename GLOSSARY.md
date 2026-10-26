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
