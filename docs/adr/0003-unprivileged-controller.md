# Without root, the scheduler pins and the template carries hugepages

Proxmox lets only `root@pam` set some VM options; the controller writes two of them on launch, `affinity` and `hugepages`. The controller decides per region whether it is privileged: logging in as `root@pam` is root, any API token or other user is not. As root nothing changes. Without root the controller never writes `affinity` or `hugepages`: a pinned VM's host CPUs travel only in the `affinity=` description token, and the proxmox-scheduler, which already reads that token, pins the VM's threads at runtime. Hugepages come from the template the VM is cloned from. The controller still writes `numaN` with `hostnodes` and `policy=bind`, which needs no root, so a strict slot allocation still binds the VM's memory to its NUMA node and one template per page size serves every NUMA node.

A NUMA NodePool therefore uses its own NodeClass whose template, a `ProxmoxUnmanagedTemplate`, has `hugepages` set. Without root, `GetInstanceTypes` checks the NodeClass's template against each zone and drops what it cannot launch correctly, setting a status condition on the NodeClass; as root the controller sets or clears these options itself, so no check applies:

- in a zone with a hugepage pool, NUMA types need a template whose `hugepages` matches the pool's page size, and regular types need a template without `hugepages`;
- without root, a template with `affinity` set offers no types, since every clone would inherit CPUs the controller can neither replace nor clear.

## Considered Options

- Detect privilege by trying to set `affinity` and falling back on a permission error: rejected, every failed attempt leaves a half-configured VM to roll back, and the answer is known from the credentials at startup.
- The scheduler writes `affinity` and `hugepages` as root before the VM starts: rejected, it turns a host daemon into a config writer, and the controller would have to create the VM stopped and wait for it; the scheduler stays passive and only applies the description token.
- The controller waits for the scheduler to pin a VM before reporting the launch: rejected, a few seconds unpinned during boot is harmless.
- Give every VM in a hugepage zone hugepages, so one template serves all types: rejected, regular VMs would draw from the hugepage pool, which zone capacity does not track for them.
- One template per host NUMA node: not needed while `numaN` `hostnodes` stays settable without root.
- A `hugepages` field on the managed `ProxmoxTemplate`: deferred, a non-root controller could not create such a template either; it can be added later without breaking anything.
- Launch NUMA types without hugepages when the template has none, or fail at Create: rejected, the first breaks slot memory accounting (a slot's memory is the pool), the second loops through launch, failure and backoff.

## Consequences

Without root, the proxmox-scheduler is required for pinned placement: the `affinity` option on a VM is no longer the source of truth, the description token is, and nothing pins a VM on a host where the scheduler is not running. The controller logs a warning for a pinned zone without a node-capacity guest, but cannot prove the scheduler is alive.

The template becomes part of the placement contract: a NodeClass is valid only for the instance types its template fits, so operators keep separate NodeClasses for NUMA and regular NodePools in hugepage zones.

Two Proxmox behaviours are assumed and must hold on supported PVE versions: `numaN` `hostnodes` needs no root, and an API token of `root@pam` does not pass the root-only check. If root tokens do pass, privilege becomes "the user is `root@pam`, with or without a token"; if `hostnodes` turns out to need root, NUMA slots need one template per NUMA node and this decision is reopened.

"Hugepage template" here is a Proxmox VM template; it is unrelated to the **NUMA template**, an instance types file entry.
