---
status: proposed
---

# A hugepage zone has two memory budgets: the pool and system memory

Partly supersedes ADR 0003: its rejection of "give every VM in a hugepage zone hugepages", and its split that offers no non-NUMA type in a hugepage zone of two or more NUMA nodes. Both rested on one gap: a hugepage zone's only memory budget was its pool, so system memory beside the pool was invisible to zone capacity and a VM drawing from it could not be accounted. Zone capacity now tracks two budgets in a hugepage zone, the hugepage pool per NUMA node and system memory (total minus the pool minus reserved memory) for the whole zone, and each virtual machine is charged to its memory source.

The memory source follows the launch decision. As root it is the pool first, wherever the type may use it: a NUMA type and a type with a hugepage request use only the pool; a bound VM without a request, and an unbound VM without a request in a zone of one NUMA node, fall back to system memory when the pool is full; an unbound VM in a zone of two or more NUMA nodes uses only system memory, since its pages would land on NUMA nodes the pool's per-node view does not track. Without root the template decides: a hugepage template's clones use the pool, a plain template's use system memory, so a hugepage template still drops unbound types in a zone of two or more NUMA nodes, and a plain template drops NUMA types and types with a hugepage request in a hugepage zone. A running virtual machine's memory source is read from its own config: `hugepages` set is the pool, anything else is system memory.

A bound VM on system memory keeps one guest NUMA node per host NUMA node its CPUs are on, with `hostnodes` and `policy=preferred` rather than `policy=bind`, because system memory is known only for the whole zone and binding against a per-node estimate can fail at start.

## Considered Options

- The node-capacity guest reports free system memory per NUMA node: rejected for now, it changes the guest's encoding and the scheduler for a budget that only bound VMs on system memory would use per node, and they do not need a hard bind.
- Split the zone's system memory evenly across NUMA nodes and keep `policy=bind`: rejected, the estimate can be wrong and the VM then fails at start, the failure #321 had for hugepages.
- Bound VMs never use system memory: rejected, pinned dedicated types are meant to run from either source.
- System memory first for types without a hugepage request: rejected in favour of the pool first, so a hugepage zone keeps launching as it did where both fit.
- Keep the root rule that offers no non-NUMA type in a pool zone of two or more NUMA nodes: rejected, as root the controller deletes `hugepages` and the VM is charged to system memory.

## Consequences

The root rule in ADR 0003 that offers no non-NUMA type in a hugepage zone of two or more NUMA nodes goes away; without root its split narrows from non-NUMA types to unbound types. `ReservedMemory` is no longer ignored in a hugepage zone. NUMA slots are still carved from the pool only. `policy=preferred` with one host node per guest NUMA node must be verified on Proxmox before this is accepted.
