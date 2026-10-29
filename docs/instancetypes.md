# Karpenter instance types

Karpenter uses instance types to make intelligent decisions about where to allocate new nodes.
Each instance type defines a specific combination of CPU, memory, and other resources, often tied to a particular availability zone or hardware family.
By evaluating these options, Karpenter selects the most efficient instance type that meets workload requirements while minimizing unused capacity.
This approach ensures workloads are scheduled cost-effectively, helping to maximize savings without sacrificing performance.

## Instance family

To make workload management with CPU and Memory easier, Karpenter for Proxmox comes with a predefined list of instance types.

By default, vCPU options are 1, 2, 4, 8, and 16 cores.
The amount of memory is determined by the vCPU count, multiplied by a family-specific ratio of 2, 3, 4, or 8.

Instance families are defined as follows:
* `c1` family: with dedicated vCPU and Memory with 1:2 rate (e.g., c1.2VCPU-4GB, c1.4VCPU-8GB), calls as compute instances.
* `t1` family: with dedicated vCPU and Memory with 1:3 rate (e.g., t1.2VCPU-6GB, t1.4VCPU-12GB).
* `s1` family: with dedicated vCPU and Memory with 1:4 rate (e.g., s1.2VCPU-8GB, s1.4VCPU-16GB), calls as standard instances.
* `m1` family: with dedicated vCPU and Memory with 1:8 rate (e.g., m1.2VCPU-16GB, m1.4VCPU-32GB), calls as memory optimized instances.
* `x1` family: with dedicated vCPU and Memory with 1:16 rate (e.g., x1.2VCPU-32GB, x1.4VCPU-64GB), calls as in-memory application optimized instances.

Instance types are named using the following convention: `<family>.<vCPU>VCPU-<memory>GB`.
For example, `c1.4VCPU-8GB` represents an instance type from the `c1` family with `4` virtual CPUs and `8` GB of memory.

The predefined list also has the `n1` and `n2` [NUMA templates](#numa-templates),
so hosts with the `static` node policy offer whole and half NUMA node types (see [NUMA types](#numa-types)).

## CPU share types

A CPU share type gives a VM CPUs that it shares with up to N−1 other VMs of the same size, instead of CPUs of its own.
The VMs that share the CPUs form a CPU share group: a set of host CPUs with N seats, one per VM.
Memory is never shared, each VM has its own.

CPU share types come in three families:
* `e2` family: a CPU share group of 2 seats, each VM is accounted as 1/2 of its vCPUs (e.g., e2.2VCPU-4GB, e2.4VCPU-16GB).
* `e3` family: a CPU share group of 3 seats, each VM is accounted as 1/3 of its vCPUs (e.g., e3.2VCPU-4GB, e3.4VCPU-16GB).
* `e4` family: a CPU share group of 4 seats, each VM is accounted as 1/4 of its vCPUs (e.g., e4.2VCPU-4GB, e4.8VCPU-32GB).

The digit after `e` is the number of seats, not a generation as in `c1` or `m1`.

* **Capacity.** The guest sees the full vCPU count, and the node's `cpu` capacity is the full vCPU count too.
  The type is burstable: a VM may use all of its vCPUs while its neighbors are idle, but no minimum of vCPU/N of them is enforced.
  Kubernetes may place up to vCPU cores of CPU requests on such a node, so choose CPU share types for workloads where that is acceptable.
* **Pricing.** A CPU share type is priced as vCPU/N CPUs plus its full memory, so `e2.2VCPU-4GB` costs as much CPU as a 1 vCPU type.
  Karpenter therefore prefers CPU share types for any NodePool that allows them.
* **Node policy.** CPU share types are offered with the `simple` and `static` node policies, and never with `simple-with-cpu-overcommit`
  (see [Node resource allocation](noderesource.md#cpu-share-types)).
* **Opt-in.** CPU share types are not part of the predefined list. They exist only when the instance types file defines them (see below).
  Once they do, every NodePool that names no instance family accepts them; a NodePool excludes them with a `NotIn` requirement (see below).

Even vCPU counts (2, 4, 8) are recommended, so that a CPU share group covers whole physical cores.

The [Proxmox Scheduler](scheduler.md) builds the CPU share groups, so that the VMs of a group share the same host CPUs,
see [CPU share instance types design](scheduler-cpushare.md).

### Select CPU share types in a NodePool

NodePools select the CPU share families with the `instance-family` label:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: burstable
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.proxmox.sinextra.dev
        kind: ProxmoxNodeClass
        name: default
      requirements:
        - key: karpenter.proxmox.sinextra.dev/instance-family
          operator: In
          values: ["e2"]
```

A NodePool that does not name a family accepts the CPU share types of the instance types file too, and Karpenter prefers them because they cost less.
A NodePool excludes the CPU share families with `NotIn`:

```yaml
      requirements:
        - key: karpenter.proxmox.sinextra.dev/instance-family
          operator: NotIn
          values: ["e2", "e3", "e4"]
```

The `NotIn` requirement matches family names only: the family is the part of the name before the dot, whatever its `cpushare`.
A CPU share type with another family name, such as `custom.2VCPU-4GB` with `cpushare: 2`, stays eligible unless its family is excluded too.
Give CPU share types and dedicated types distinct family names, so that this filter tells them apart.

## NUMA types

A NUMA type gives a VM a whole NUMA node of a Proxmox host, an aligned half of it, or an aligned quarter of it.
Its size is not fixed in the instance types file: it is derived from the NUMA topology of the hosts,
so every host offers its real usable size, after its reservations (see [Node resource allocation](noderesource.md#numa-reservations)).

Each NUMA node is cut into aligned NUMA slots, and smaller slots nest inside larger ones:

```
n1  whole NUMA node  [ ............................ ]
n2  halves           [ ............. ][ ............. ]
n4  quarters         [ ...... ][ ...... ][ ...... ][ ...... ]
```

NUMA types come in three families, one per slot fraction:
* `n1` family: a whole NUMA node (e.g., n1.30VCPU-126GB).
* `n2` family: an aligned half of a NUMA node (e.g., n2.16VCPU-63GB).
* `n4` family: an aligned quarter of a NUMA node (e.g., n4.8VCPU-31GB).

The digit after `n` is the slot fraction, the number a NUMA node is divided by, not a generation as in `c1` or `m1`.

* **Naming.** NUMA types use the usual convention `<family>.<vCPU>VCPU-<memory>GB`, with the CPUs and memory of the slot.
  A slot has the usable CPUs of the node divided by the fraction, rounded down to whole physical cores,
  and the node's memory divided by the fraction, rounded down to whole GiB.
  Hosts with the same slot shape share one instance type, so a homogeneous cluster gets one type per family.
  Reserved CPUs make NUMA node 0 smaller than the others, so it usually has types of its own,
  e.g. `n1.30VCPU-126GB` for node 0 and `n1.32VCPU-126GB` for node 1.
* **Placement.** A NUMA VM is pinned 1:1 to the CPUs of one free slot and its memory is bound to the slot's NUMA node.
  It never spans NUMA nodes: when no slot of its shape is free, the launch fails and Karpenter tries elsewhere.
  When the zone has a hugepage pool, the VM is created with hugepages.
* **Availability.** A NUMA type is offered in the zones that have its slot shape, and is available while one such slot is free:
  none of its CPUs is pinned, its NUMA node has the slot's memory free,
  and the node keeps room for its CPU share groups and its VMs without CPU affinity.
  Regular VMs keep off free slots when they can: they go to NUMA nodes already in use first, and to CPUs outside free slots first.
  The [Proxmox Scheduler](scheduler.md) puts new CPU share groups on the NUMA node with the most pinned CPUs for the same reason.
* **Node policy.** NUMA types are offered with the `static` node policy only.
* **Pricing.** A NUMA type is priced by its CPUs and memory, as any other type.
* **Defaults.** The predefined list has the `n1` and `n2` NUMA templates.
  An instance types file replaces the predefined list, so it has NUMA types only if it defines NUMA templates (see below).

### NUMA templates

An entry of the instance types file with a `numa` field is a NUMA template, not an instance type.
Its name is the family of the NUMA types generated from it:

```json
[
  {
    "name": "n1",
    "numa": 1,
    "capacity": {
      "ephemeral-storage": "30Gi",
      "pods": "110"
    }
  },
  {
    "name": "n2",
    "numa": 2,
    "capacity": {
      "ephemeral-storage": "30Gi",
      "pods": "110"
    }
  },
  {
    "name": "n4",
    "numa": 4,
    "capacity": {
      "ephemeral-storage": "30Gi",
      "pods": "110"
    }
  }
]
```

* `name`: The family. The name is free-form: the `numa` field, not the name, makes an entry a NUMA template.
* `numa`: The slot fraction: `1`, `2` or `4`. The controller rejects the file if it has any other value.
* `capacity`: `ephemeral-storage` and `pods` are taken from the template. `cpu` and `memory` are ignored, they come from the slot.
* `overhead` (optional): When omitted, it is computed from the CPUs and memory of each generated type.

NUMA templates and other entries can be mixed in one file.
If a static entry has the same name as a generated NUMA type, the static entry is kept and the NUMA type is not offered.

### Select NUMA types in a NodePool

NodePools select the NUMA families with the `instance-family` label, without matching on the generated names:

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
        name: default
      requirements:
        - key: karpenter.proxmox.sinextra.dev/instance-family
          operator: In
          values: ["n1", "n2"]
```

A NodePool that does not name a family accepts NUMA types too.
A NodePool excludes them with `NotIn`:

```yaml
      requirements:
        - key: karpenter.proxmox.sinextra.dev/instance-family
          operator: NotIn
          values: ["n1", "n2", "n4"]
```

### Kept NUMA types

When the slot shape of a host changes (new reservations, a hugepage pool, new hardware), its NUMA types get new names.
Nodes of the old names are not replaced: a NUMA type stays listed, with unavailable offerings, as long as a NodeClaim still uses it.
Nothing new launches on a kept NUMA type, and it is dropped with its last NodeClaim, so its nodes age out normally.
To roll them anyway, delete the NodeClaims.

Removing a NUMA template from the file drops its types, kept or not, so their nodes drift and are replaced.

## Customize instance types

You can redefine the instance family and the list of instance types by providing a JSON configuration file.
The controller includes the flag `-instance-types-file` or env `INSTANCE_TYPES_FILE`, which lets you specify the path to this custom instance types file.

The file structure looks like this:

```json
[
  {
    "name": "c1.1VCPU-2GB",
    "capacity": {
      "cpu": "1",
      "ephemeral-storage": "30Gi",
      "memory": "2Gi",
      "pods": "64"
    },
    "overhead": {
      "KubeReserved": {
        "cpu": "20m",
        "memory": "384Mi"
      },
      "SystemReserved": {
        "cpu": "10m",
        "memory": "64Mi"
      },
      "EvictionThreshold": {
        "memory": "100Mi"
      }
    }
  }
]
```

* `name`: The name of the instance type, following the convention `<family>.<size-of-instance>`.
* `cpushare` (optional): The number of seats in the CPU share group of a CPU share type: `2`, `3` or `4`.
  Omit it, or set it to `0`, for dedicated CPUs. The controller rejects the file if it has any other value.
  This field, not the name, makes an instance type a CPU share type, so the family can be called anything.
* `numa` (optional): The slot fraction of a NUMA template: `1`, `2` or `4` (see [NUMA templates](#numa-templates)).
* `capacity`: The resource capacity of the instance type.
  * `cpu`: The number of virtual CPUs.
  * `ephemeral-storage`: The boot disk size.
  * `memory`: The amount of memory.
  * `pods`: The maximum number of pods.
  * `hugepages-1Gi` (optional): The amount of hugepages with 1Gi size.
  * `hugepages-2Mi` (optional): The amount of hugepages with 2Mi size.
* `overhead`: The resource overhead for the instance type, applied in the kubelet configuration file.
  * `KubeReserved`: The resources reserved for Kubernetes system components.
    * `cpu`: The amount of CPU reserved for Kubernetes.
    * `memory`: The amount of memory reserved for Kubernetes.
  * `SystemReserved`: The resources reserved for the host system.
    * `cpu`: The amount of CPU reserved for the host system.
    * `memory`: The amount of memory reserved for the host system.
  * `EvictionThreshold`: The eviction threshold for the instance type.
    * `memory`: The minimum amount of free memory required before the eviction process is triggered.

A CPU share type is an entry with the `cpushare` field:

```json
[
  {
    "name": "e2.2VCPU-4GB",
    "cpushare": 2,
    "capacity": {
      "cpu": "2",
      "ephemeral-storage": "30Gi",
      "memory": "4Gi",
      "pods": "64"
    }
  }
]
```

`capacity.cpu` is the full vCPU count the guest sees, not the vCPU/N share it is accounted as.

## Generate instance types

The `instancetypes` command-line utility generates an instance types file.
`make build-instancetypes` builds it as `bin/instancetypes-$(ARCH)`:

```shell
bin/instancetypes-amd64 generate --cpus 2,4,8 --memfactor 2,4 --kubelet --system --eviction > instancetypes.json
```

* `--cpus`: Comma-separated list of vCPU counts (default `1,2,4`).
* `--memfactor`: Comma-separated list of memory multipliers per vCPU, each `2`, `3`, `4`, `8` or `16` (default `2,3,4`).
  The multiplier picks the family: `c1`, `t1`, `s1`, `m1` or `x1`; any other value is rejected.
* `--storage`: Boot disk size in GiB, omitted if zero (default `30`).
* `--cpushare`: Comma-separated list of CPU share group seats, each `2`, `3` or `4`.
  For each value N it adds the `eN` family of the same sizes, with `cpushare` set.
* `--numa`: Comma-separated list of NUMA slot fractions, each `1`, `2` or `4`.
  For each value N it adds the `nN` [NUMA template](#numa-templates), with `numa` set,
  the `--storage` size and no `cpu`, `memory` or `overhead`: those come from each slot.
* `--kubelet`, `--system`, `--eviction`: Add the `overhead` for the kubelet, the OS system daemons and the eviction threshold.

For example, `--cpus 2,4,8 --memfactor 2,4 --cpushare 2,3,4` generates the `c1` and `s1` families and the
`e2`, `e3` and `e4` families with 2, 4 and 8 vCPUs and 1:2 and 1:4 memory ratios, e.g. `e3.4VCPU-16GB`.
Adding `--numa 1,2,4` also adds the `n1`, `n2` and `n4` NUMA templates; their NUMA types, e.g. `n2.16VCPU-63GB`,
are named by the controller from the NUMA topology of the hosts.
