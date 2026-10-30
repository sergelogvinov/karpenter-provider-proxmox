# Hugepage templates without root

NUMA VMs in a zone with a [hugepage pool](noderesource.md#hugepage-pool) run on hugepages of the pool's page size.
As root the controller sets `hugepages` on each launch. Without root (any API token, or a user other than `root@pam`, see [Permissions](permission.md)) it may not.
The clone then inherits `hugepages` from the VM template, so the template decides which instance types a NodeClass can launch in such a zone ([ADR 0003](adr/0003-unprivileged-controller.md)):

| Template `hugepages`                 | NUMA types in a hugepage zone | Non-NUMA types in a hugepage zone |
|--------------------------------------|-------------------------------|-----------------------------------|
| unset                                | dropped                       | offered                           |
| the pool's page size (`2` or `1024`) | offered                       | dropped                           |
| another page size, or `any`          | dropped                       | dropped                           |
| not known (see below)                | dropped                       | dropped                           |

Non-NUMA types are every instance type outside a NUMA slot: static types and CPU share types.
A non-NUMA VM with `hugepages` would draw from the pool, which zone capacity does not track for it.

Zones without a hugepage pool, and regions where the controller is root, are not affected: their offerings are not filtered by the template.

So in hugepage zones without root, NUMA NodePools and non-NUMA NodePools need separate NodeClasses, each with its own template.

A dropped offering is only left out of what the NodeClass offers for new nodes.
NodeClaims already running on an instance type that is now dropped, for example after the template's `hugepages` changed, are not drifted and keep running as they are.

## Set up the hugepage template

1. Clone your VM template in Proxmox and, as `root@pam` (only root may set this option), give the clone the page size of the zone's hugepage pool, in MiB:

   ```shell
   qm set 1003 --hugepages 1024
   qm template 1003
   ```

   Set the page size explicitly. `hugepages: any` lets Proxmox pick the page size at VM start, so it is not guaranteed to be the pool's page size, and the controller treats it as a mismatch.
   Do not set `affinity` on the template.

   Repeat on every Proxmox node with that pool, or use shared storage. Give every copy the same `hugepages`: a template whose copies disagree is not known (below).

2. Reference it from a `ProxmoxUnmanagedTemplate`. A tag is the simplest way to keep it apart from the template without `hugepages`:

   ```yaml
   apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
   kind: ProxmoxUnmanagedTemplate
   metadata:
     name: talos-hugepages
   spec:
     tags:
       - karpenter
       - hugepages-1g
   ```

   Once reconciled, its status records what its VM templates share:

   ```yaml
   status:
     template:
       hugepages: "1024"
       affinity: false
     conditions:
       - type: ProxmoxVirtualMachineTemplateConsistent
         status: "True"
   ```

3. Give it its own NodeClass:

   ```yaml
   apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
   kind: ProxmoxNodeClass
   metadata:
     name: numa
   spec:
     instanceTemplateRef:
       kind: ProxmoxUnmanagedTemplate
       name: talos-hugepages
     # bootDevice, metadataOptions, ... as in the non-NUMA NodeClass
   ```

4. Pair a NUMA NodePool with it, restricted to the NUMA families of the [instance types file](scheduler-numa.md#3-configuration):

   ```yaml
   apiVersion: karpenter.sh/v1
   kind: NodePool
   metadata:
     name: numa
   spec:
     template:
       spec:
         nodeClassRef:
           group: karpenter.proxmox.sinextra.dev
           kind: ProxmoxNodeClass
           name: numa
         requirements:
           - key: karpenter.proxmox.sinextra.dev/instance-family
             operator: In
             values: ["n1", "n2"]
   ```

   The non-NUMA NodePool keeps the non-NUMA NodeClass, whose template has no `hugepages`, and should exclude the NUMA families.

## The TemplateCompatible condition

Each NodeClass has a `TemplateCompatible` condition.
It is `False` only for a problem of the template, and `True` otherwise.
Dropping NUMA types from a NodeClass whose template has no `hugepages`, or non-NUMA types from a NodeClass with a hugepage template, is the intended split above and leaves it `True`, so a correctly split pair of NodeClasses shows `True` on both.
Whenever offerings are dropped, its message names each zone, the kind of instance type dropped there, and why:

```yaml
- type: TemplateCompatible
  status: "True"
  reason: TemplateHugepagesSet
  message: "region-1/pve-1: non-NUMA types dropped, the template has hugepages=1024 and a non-NUMA VM would draw from the zone's hugepage pool"
```

| Status  | Reason                      | Dropped                 | Cause                                                                    |
|---------|-----------------------------|-------------------------|--------------------------------------------------------------------------|
| `True`  | `TemplateCompatible`        | nothing                 | every kind is offered in every zone                                      |
| `True`  | `TemplateHugepagesSet`      | non-NUMA types          | the template has the pool's page size: a NodeClass for NUMA types        |
| `True`  | `TemplateHugepagesUnset`    | NUMA types              | the template has no `hugepages`: a NodeClass for non-NUMA types          |
| `True`  | `OfferingsDropped`          | as in the message       | intended splits of more than one reason                                  |
| `False` | `TemplateHugepagesMismatch` | both                    | the template's `hugepages` is another page size, or `any`                |
| `False` | `TemplateStatusUnknown`     | both                    | the template's `status.template` is unset, or the template cannot be read |
| `False` | `TemplateOffersNothing`     | both                    | the NodeClass offers nothing in a zone                                   |
| `False` | `TemplateIncompatible`      | as in the message       | problems of more than one reason                                         |

The condition is not part of `Ready`.
With a `True` condition, read the message to check that the dropped kind is the one the NodeClass's NodePool does not use.
The controller rechecks every 15 seconds, so the condition follows changes to the template.
While a hugepage zone is out of the controller's view, for example when its Proxmox node is unreachable, the check keeps the page size of its pool as last seen, so the condition does not flip; the zone launches nothing until it is back.

A template whose `status.template` is unset is not known, and launches nothing in hugepage zones, since a clone's `hugepages` cannot be predicted.
This happens when its VM templates disagree on `hugepages` or `affinity` (`ProxmoxVirtualMachineTemplateConsistent` is `False`, reason `VMTemplatesDisagree`), or while one of them is not read yet (`Unknown`, reason `VMTemplatesUnknown`).
The message repeats that condition's message.
A template that cannot be read at all is treated the same.
