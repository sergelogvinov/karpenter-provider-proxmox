# Proxmox permissions

The controller decides per region, from that region's credentials in the [provider configuration](config.md), whether it is root:

* `username: root@pam` with a `password` is **root**, even with `token_id_file` or `token_secret_file` also set:
  the client logs in with username and password whenever both are set, and with a token only otherwise;
* any API token, a `root@pam` token included, and any other user are **not root**.

The decision is logged once per region at startup.
Proxmox lets only `root@pam` itself set the VM options `affinity`, `hugepages`, `keephugepages` and `args`;
an API token of `root@pam` does not pass that check ([ADR 0003](adr/0003-unprivileged-controller.md)).
The controller writes neither `keephugepages` nor `args`.

## Running without root

Without root a launch never writes `affinity` or `hugepages`; everything else is the same:

* A VM under pinned placement (`--node-policy=static`) gets its host CPUs only in the `affinity=` token of its description.
  The [Proxmox Scheduler](scheduler.md) reads that token and pins the VM's threads, so it must run on every zone with pinned placement.
  Zone capacity reads the same token back, so the VM's CPUs stay counted across controller restarts.
  The controller logs a warning when a zone under pinned placement has no node-capacity guest,
  once each time the zone enters that condition: it warns again if the zone gains a guest and later loses it.
  It cannot tell whether the scheduler is actually running.
* A VM in a NUMA slot still gets `numaN` with `hostnodes` and `policy=bind`, which needs no root, so its memory stays bound to the slot's NUMA node.
  Its `hugepages` must come from the VM template it is cloned from, a `ProxmoxUnmanagedTemplate` with `hugepages` set.
  In a zone with a hugepage pool a NodeClass offers NUMA types only when its template's `hugepages` is the pool's page size,
  and non-NUMA types only when its template has no `hugepages`; the `TemplateCompatible` condition on the NodeClass names what is dropped.
  See [Hugepage templates without root](hugepages.md).
* A `ProxmoxUnmanagedTemplate` drops the template placement of each VM template with `affinity` set,
  since every clone would inherit CPUs the controller can neither replace nor clear, so NodeClasses using it lose that zone.
  The template placement comes back on the next sync once `affinity` is removed from the VM template.
  When every matching VM template is dropped, the template's Ready condition is false with reason `TemplateCPUAffinitySet`.
  When only some are dropped, Ready stays true with reason `TemplateCPUAffinityDropped` and a message naming them.
  A NodeClass left with no template placements, for example because its region lost all of them,
  has its `InstanceTemplateReady` condition false with reason `TemplateCPUAffinitySet` and the template's message.

## Privileges for a non-root user

The `Karpenter` role in [Install](install.md#proxmox-configuration) grants most of them on `/`, with `Pool.Audit` and `Pool.Allocate` appended for resource pools.
It lacks `Sys.Modify`, which managed VM templates need on `/` to download their image with `download-url` (see below);
add it to the role when the controller builds its own VM templates.

Each privilege below is tied to the API calls the controller makes.
Entries marked _(uncertain)_ follow from the Proxmox API documentation as best understood, but were not verified against a Proxmox cluster;
when a call fails with a permission error, the Proxmox task log names the missing privilege.

### Reading the cluster

| API call | Used for | Privilege |
|---|---|---|
| `GET /cluster/resources` | listing zones, VMs, templates and storages for capacity and template sync | `Sys.Audit` on nodes, `VM.Audit` on VMs, `Datastore.Audit` on storages; objects without them are silently left out |
| `GET /cluster/ha/status/manager_status` | skipping zones in HA maintenance | `Sys.Audit` on `/` |
| `GET /cluster/nextid` | choosing VM IDs | none |
| `GET /nodes/{node}/status` | zone CPU and memory topology | `Sys.Audit` on `/nodes/{node}` |
| `GET /nodes/{node}/network` | bridge addresses for node IPAM | none; the list is filtered |
| `GET /nodes/{node}/network/{iface}` | one bridge's addresses | `Sys.Audit` on `/nodes/{node}` |
| `GET /nodes/{node}/qemu`, `GET /nodes/{node}/qemu/{vmid}/config`, `GET .../status/current` | reading VM and template configs, the node-capacity guest | `VM.Audit` on `/vms/{vmid}` |
| `GET /nodes/{node}/storage`, `GET /nodes/{node}/storage/{storage}/content` | storage capacity, finding images and cloud-init ISOs | `Datastore.Audit` on `/storage/{storage}` |
| `GET /pools/{pool}` | resource pool membership | `Pool.Audit` on `/pool/{pool}` |
| `GET /nodes/{node}/qemu/{vmid}/firewall/rules` | comparing a node's security groups | `VM.Audit` on `/vms/{vmid}` |

### Launching and deleting nodes

| API call | Used for | Privilege |
|---|---|---|
| `POST /nodes/{node}/qemu/{vmid}/clone` | cloning the VM template into a new VM, with `pool` and `storage` | `VM.Clone` on the template, `VM.Allocate` on `/vms/{newid}` or the pool, `Datastore.AllocateSpace` on the target storage |
| `PUT /nodes/{node}/qemu/{vmid}/resize` | growing the boot disk | `VM.Config.Disk` on the VM, `Datastore.AllocateSpace` on its storage |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `cores`, `numa`, `numaN` | vCPUs and the NUMA binding of a slot | `VM.Config.CPU` |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `memory` | memory | `VM.Config.Memory` |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `affinity`, `hugepages` | written only as root | `root@pam` only |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `tags`, `description` | node class tags, also on tag updates | `VM.Config.Options` |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `smbios1` | instance type and node name for the guest | `VM.Config.HWType` _(uncertain: `VM.Config.Options` on some releases)_ |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `netN` (multi-queue), `ipconfigN`, `nameserver` | the node's network and address | `VM.Config.Network`, `SDN.Use` on the bridge (PVE 8+) _(uncertain: whether `ipconfigN` and `nameserver` need `VM.Config.Cloudinit` instead)_ |
| `PUT /nodes/{node}/qemu/{vmid}/cloudinit` | regenerating the cloud-init drive | `VM.Config.Cloudinit` |
| `POST /nodes/{node}/storage/{storage}/upload` | uploading the cloud-init ISO (`metadataOptions.type: cdrom`) | `Datastore.AllocateTemplate` on `/storage/{storage}` |
| `PUT /nodes/{node}/qemu/{vmid}/config`: `ide2` | attaching and detaching the cloud-init ISO | `VM.Config.CDROM`; `Datastore.Audit` on the ISO's storage _(uncertain)_ |
| `DELETE /nodes/{node}/storage/{storage}/content/{volume}` | deleting the cloud-init ISO | `Datastore.Allocate` on `/storage/{storage}` _(uncertain: `Datastore.AllocateSpace` may suffice)_ |
| `PUT /nodes/{node}/qemu/{vmid}/firewall/options`, `POST`/`DELETE .../firewall/rules` | security groups | `VM.Config.Network` on the VM |
| `PUT /pools/{pool}` | moving a node between resource pools | `Pool.Allocate` on `/pool/{pool}`, `VM.Allocate` on the VM _(uncertain)_ |
| `POST /nodes/{node}/qemu/{vmid}/status/start` | starting the node | `VM.PowerMgmt` |
| `POST /nodes/{node}/qemu/{vmid}/status/stop` | stopping a node before deleting it | `VM.PowerMgmt` |
| `DELETE /nodes/{node}/qemu/{vmid}` | deleting a node, or a failed launch | `VM.Allocate` on `/vms/{vmid}` |

### Managed VM templates (`ProxmoxTemplate`)

Only needed when the controller builds its own VM templates; `ProxmoxUnmanagedTemplate` needs the read privileges above only.

| API call | Used for | Privilege |
|---|---|---|
| `POST /nodes/{node}/storage/{storage}/download-url` | downloading the source image | `Datastore.AllocateTemplate` on `/storage/{storage}`, `Sys.Audit` and `Sys.Modify` on `/` |
| `POST /nodes/{node}/qemu` with `import-from` and `pool` | creating the VM template from the image | `VM.Allocate` on `/vms/{vmid}` or the pool, `Datastore.AllocateSpace` on the target storage, `VM.Config.Disk`, `VM.Config.CPU`, `VM.Config.Memory`, `VM.Config.Network`, `VM.Config.Cloudinit`, `VM.Config.Options`, `VM.Config.HWType`, `SDN.Use` on each bridge (PVE 8+); access to the imported image _(uncertain: `Datastore.Audit` or `Datastore.AllocateSpace` on the image storage)_ |
| `PUT /nodes/{node}/qemu/{vmid}/config` | updating a template's in-place fields | the `VM.Config.*` privilege of each option, as above |
| `hostpciN` with `mapping` | PCI devices of the template | `Mapping.Use` on `/mapping/pci/{id}` _(uncertain: also `VM.Config.HWType`)_ |
| `DELETE /nodes/{node}/qemu/{vmid}` | replacing or deleting a template | `VM.Allocate` on `/vms/{vmid}` |
| `DELETE /nodes/{node}/storage/{storage}/content/{volume}` | deleting the downloaded image | `Datastore.Allocate` on `/storage/{storage}` _(uncertain)_ |
