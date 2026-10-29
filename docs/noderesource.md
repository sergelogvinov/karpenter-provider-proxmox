# Node resource allocation and optimization

The Karpenter Proxmox provider supports three resource allocation modes: `simple` (default), `static`, and `simple-with-cpu-overcommit`.

To change allocation mode, set flag `-node-policy` or env `NODE_POLICY` to `simple`, `static`, or `simple-with-cpu-overcommit`.

## Simple allocation mode

In this mode, the plugin periodically observes the Proxmox cluster resources and the currently running VMs. It calculates the available resources on each Proxmox node by subtracting the resources requested by running VMs from the total resources of the node.

When scheduling new VMs, the plugin checks the available resources on each node to ensure that the requested resources can be allocated.

If two or more VMs are using the same vCPUs on a Proxmox node (CPU affinity), the plugin merges their vCPUs to calculate the total vCPUs on that node.
By default, a new VM is scheduled only if the total vCPUs (including the new VM) does not exceed the available logical CPU capacity of the node.

Memory overcommitment is not supported.

Example:

A Proxmox node has:
* 16 vCPUs
* 64 GB of memory

Two running VMs are using:
* vCPUs `0–3` and `2–4` respectively (via CPU affinity)
* 16 GB and 16 GB of memory

The available resources are calculated as:
* vCPUs `0–4` are used (merged), so 11 vCPUs are available
* 32 GB of memory is used, so 32 GB is available

## Simple allocation with CPU overcommit

Select the separate `simple-with-cpu-overcommit` policy with
`--node-policy=simple-with-cpu-overcommit` or `NODE_POLICY=simple-with-cpu-overcommit`.
The existing `simple` policy remains unchanged. Configure the CPU allocation ratio via
`--cpu-overcommit-ratio` or `CPU_OVERCOMMIT_RATIO`; its default is `1`.
The value must be finite and at least `1`; fractional ratios such as `1.5` are supported.
A ratio greater than `1` is rejected with the `simple` and `static` policies.

Helm configuration:

```yaml
extraArgs:
  - --node-policy=simple-with-cpu-overcommit
settings:
  cpuOvercommitRatio: 2
```

The shared vCPU budget is `floor(remaining logical CPUs * ratio)`, where remaining
logical CPUs exclude reserved CPUs and the union of affinity-assigned CPUs. Existing
VMs without CPU affinity and new shared allocations consume this same budget.
Existing usage is tracked by VM ID even when CPU or memory usage exceeds the current
budget. Reducing the ratio while keeping this policy blocks further CPU admission
until usage fits again, without dropping existing VM memory from accounting.
Affinity CPU IDs remain unavailable until the last VM using them is released.
Hyperthreading is already included in the discovered logical CPU count; the ratio is
an additional allocation multiplier, not a change to CPU topology or VM CPU IDs.

For example, a host with 32 logical CPUs and 48 allocated shared vCPUs has no remaining
CPU allocation capacity at ratio `1`. With ratio `2` and no reservations or affinity
assignments, it has a budget of 64 vCPUs and can admit another 16 vCPUs.

This changes allocation accounting only. It does not create physical CPU capacity or
limit actual VM CPU use. Choose the ratio based on sustained workload demand and monitor
host contention. Memory accounting and reservations remain unchanged, and all visible
running VMs continue to be included in resource discovery. NodePool limits still apply.

## Static allocation mode

In this mode, the plugin does everything that `simple` mode does, but additionally sets `CPU pinning` (CPU affinity) and `NUMA node affinity` when creating a new VM.

The CPU and NUMA placement is calculated based on the available resources on the Proxmox node.
This ensures that the VM is pinned to specific CPU cores and NUMA nodes that have sufficient capacity, which improves performance and resource utilization.

If a VM requires more than 1 vCPU, the plugin tries to:
* Allocate vCPUs from the same physical core (using hyper-thread siblings) first.
* Then allocate from the same NUMA node if more cores are needed.

When several NUMA nodes can hold the whole VM (its vCPUs and its memory), the VM goes to the one it leaves fullest,
judged by whichever of CPU or memory has less left, so that the emptier NUMA nodes stay whole for larger VMs.
Only when no single NUMA node can hold the VM is it spread across NUMA nodes, with its memory split in proportion to its vCPUs on each.

Across zones, a zone where the VM stays on one NUMA node is always preferred over a zone where it would span several,
whatever the node class's `placementStrategy` (see [NodeClass](nodeclass.md)).

Within a Proxmox node, the VM also keeps off free NUMA slots (see [NUMA types](#numa-types)) when it can:
a NUMA node already in use is tried before one whose whole slot is free, and the CPUs outside free slots are taken first,
then the CPUs that break the fewest and smallest free slots.
This is a preference only: a VM still takes slot CPUs, or spans NUMA nodes, when that is the only fit.
It never makes a VM span more NUMA nodes than it would without it.

## NUMA types

With the `static` node policy, Karpenter also offers NUMA types: a VM that takes a whole NUMA node, an aligned half, or an aligned quarter of it
(see [Instance types](instancetypes.md#numa-types)).
Karpenter cuts each NUMA node into NUMA slots from its usable CPUs and memory:

* **CPUs.** The CPUs of the NUMA node minus the `reservedcpus` that belong to it.
  A reserved CPU reduces only the NUMA node that holds it, so NUMA node 0 is usually smaller than the others.
  A slot has whole physical cores only: the CPUs left over by rounding stay usable by regular VMs, but no slot covers them.
* **Memory.** The memory of the NUMA node minus its share of `reservedmemory` (see [NUMA reservations](#numa-reservations)),
  or the NUMA node's hugepage pool when the Proxmox node has one.

A slot is free, and its NUMA type available, while none of its CPUs is pinned, its NUMA node has the slot's memory free,
and the Proxmox node keeps room for its CPU share groups and its VMs without CPU affinity next to it.
A NUMA VM gets the CPUs of the lowest free slot of its shape (lowest NUMA node, then lowest slot),
pinned one vCPU per CPU, and one guest NUMA node bound to the slot's NUMA node with all of its memory:

```
affinity: 1-15,33-47
numa0: cpus=0-29,hostnodes=0,memory=129024,policy=bind
```

If the slot's CPUs are used by a CPU share group, the [Proxmox Scheduler](scheduler.md) moves the group on its next rebalance.

Example:

A Proxmox node has 2 NUMA nodes, each with 16 cores / 32 threads and 128 GiB of memory,
with `reservedcpus` `[0,32]` (one core of NUMA node 0) and `reservedmemory` 4 GiB:

| NUMA node | Usable CPUs | Usable memory | `n1` | `n2` | `n4` |
|---|---|---|---|---|---|
| 0 | 30 | 126 GiB | `n1.30VCPU-126GB` | `n2.14VCPU-63GB` | `n4.6VCPU-31GB` |
| 1 | 32 | 126 GiB | `n1.32VCPU-126GB` | `n2.16VCPU-63GB` | `n4.8VCPU-31GB` |

NUMA node 0 has 15 usable cores: its halves have 7 cores each, and the core left over is used by regular VMs only.

## CPU share types

A VM of a CPU share type (see [Instance types](instancetypes.md#cpu-share-types)) does not get CPUs of its own: it takes a seat in a CPU share group.
Karpenter never assigns CPUs to a CPU share VM, it only counts how many host CPUs the CPU share groups of a node need.
The [Proxmox Scheduler](scheduler.md) builds the CPU share groups on host CPUs, and puts a new group on the NUMA node with the most pinned CPUs first,
so that groups rarely sit on free NUMA slots (see [NUMA types](#numa-types)).

For each size class, the number of seats N and the vCPU count, with `count` VMs on the node:

```
groups = ceil(count / N)
CPUs   = groups × vCPU
```

The CPUs of all size classes add up. Sharing is never assumed across size classes.
So a single `e2.2VCPU-4GB` VM costs 2 CPUs, and a second one on the same node costs no more CPUs: it fills the open seat.

* `simple` mode: the CPU share groups' CPUs are subtracted from the available vCPUs before the other VMs without CPU affinity are counted.
* `static` mode: a new pinned VM fits only if the CPUs not pinned by other VMs, minus the CPU share groups' CPUs, are enough for it.
  CPU share VMs get no CPU affinity and no NUMA node affinity.
* `simple-with-cpu-overcommit` mode: CPU share types are not offered.
  A VM created by hand with a `cpushare` token is counted as a regular VM without CPU affinity.

Memory is accounted as for any other VM, at the node level.

Karpenter writes the `cpushare=N` token into the VM description, and reads it back when it observes the running VMs,
so a VM created by hand with the token is accounted the same way:

```
Karpenter managed instance, class=default, capacity-type=on-demand, cpushare=2
```

A VM with both CPU affinity and a `cpushare` token is accounted as a pinned VM, and the token is ignored.

Example, in `simple` or `static` mode:

A Proxmox node has 16 CPUs. Running VMs are:
* three `e2.2VCPU-4GB` VMs: 2 groups, 4 CPUs
* one `e4.8VCPU-16GB` VM: 1 group with three free seats, 8 CPUs

The CPU share groups take 12 CPUs, so 4 CPUs are left for other VMs.
A fourth `e2.2VCPU-4GB` VM costs no more CPUs, only its 4 GB of memory.

## Limitations and notes

`static` mode requires root privileges (`root@pam`) to set CPU pinning for VMs.

Proxmox does not expose CPU and NUMA topology via its API.
As a result, the plugin tries to predict the CPU and NUMA topology based on information gathered from the Proxmox node.
To improve the accuracy of this prediction, you can provide a custom node topology configuration file, as described below.

## Customize node topology

You can customize the node topology by providing a JSON configuration file.
The controller includes the flag `-node-setting-file` or env `NODE_SETTING_FILE`, which lets you specify the path to this custom node resources file.

The file structure looks like this:

```json
{
  "region-1": {
    "node1": {
      "sockets": 1,
      "threads": 2,
      "uncorecaches": 1,
      "nodes": {
        "0": {
          "cpus": "0-15",
          "memsize": 4294967296
        },
        "1": {
          "cpus": "16-31",
          "memsize": 4294967296
        }
      },

      "reservedcpus": [0,4],
      "reservedmemory": 1073741824
    },
    "node2": {
      "reservedcpus": [1,5],
      "reservedmemory": 4294967296
    },
    "*": {
      "reservedcpus": [1,5],
      "reservedmemory": 4294967296
    }
  },
  "region-2": {
    "node3": {
      "reservedcpus": [0,1],
      "reservedmemory": 1073741824
    }
  }
}
```

- The top-level keys are region names (as defined in the Proxmox cluster configuration).
- The second-level keys are node names (as shown in the Proxmox VE dashboard). You can use `*` as a wildcard to apply settings to all nodes in the region.
    * `reservedcpus`: An array of CPU core indices to reserve for system use on the node.
    * `reservedmemory`: The amount of memory (in bytes) to reserve for system use on the node, split evenly across its NUMA nodes (see [NUMA reservations](#numa-reservations)).
    * `hugepagesize`: (Optional) The page size (in bytes) of the node's hugepage pool (see [Hugepage pool](#hugepage-pool)).

- NUMA topology settings (optional):
    * `sockets`: (Optional) The number of CPU sockets on the node.
    * `threads`: (Optional) The number of threads per core on the node.
    * `uncorecaches`: (Optional) The number of uncore cache levels on the node.
    * `nodes`: (Optional) An object defining NUMA nodes on the system.
        - The keys are NUMA node IDs.
        - Each value is an object with:
            * `cpus`: A string defining the CPU cores associated with the NUMA node (e.g., "0-15" for cores 0 to 15).
            * `memsize`: The amount of memory (in bytes) associated with the NUMA node, or its hugepage pool (see [Hugepage pool](#hugepage-pool)).

This configuration allows you to optimize resource allocation on your Proxmox nodes by reserving specific CPU cores and memory for system use, as well as defining the NUMA topology for better VM performance.

`sockets` field defines how many physical CPU sockets are present on the node.

`threads` field defines how many threads per core are available SMT/Hyper-Threading-wise.

`uncorecaches` field defines how many uncore cache levels are present on the node.
Use the `lscpu -e` command on the Proxmox node to gather this information (output column: L3 - get maximum number +1).

`cpus` field supports both individual CPU indices and ranges (e.g., "0,2,4-6" for cores 0, 2, 4, 5, and 6).
It can be gathered from the Proxmox VE dashboard or by using the `lscpu | grep NUMA` command on the Proxmox node.

`memsize` values are specified in bytes. To define memory on each NUMA node, you can use `numactl --hardware` command on the Proxmox node to get the memory distribution across NUMA nodes.

## NUMA reservations

With the `static` node policy, reservations are accounted per NUMA node, not only for the whole Proxmox node:

* `reservedcpus`: each reserved CPU is taken from the NUMA node it belongs to.
* `reservedmemory`: split evenly across the NUMA nodes. Each NUMA node starts at its memory minus `reservedmemory / <number of NUMA nodes>`.
  A Proxmox node with 2 NUMA nodes of 128 GiB and `reservedmemory` 4 GiB has 126 GiB usable on each NUMA node.

Per-NUMA overrides of the reservations are not supported.

### Hugepage pool

A Proxmox node can hold its VM memory as preallocated hugepages of one size, a hugepage pool.
The node-capacity VM of the [Proxmox Scheduler](scheduler.md) declares it with `hugepages` (the page size in MiB, `2` or `1024`),
and its `numa[n]` memory is then the pool on each NUMA node, not ordinary memory:

```yaml
hugepages: 1024
numa0: cpus=0-15,hostnodes=0,memory=102400
numa1: cpus=16-31,hostnodes=1,memory=102400
```

The node settings file can declare it too, with `hugepagesize` (the page size in bytes) and the pool of each NUMA node as `memsize`:

```json
{
  "region-1": {
    "node1": {
      "hugepagesize": 1073741824,
      "nodes": {
        "0": { "cpus": "0-15,32-47", "memsize": 107374182400 },
        "1": { "cpus": "16-31,48-63", "memsize": 107374182400 }
      }
    }
  }
}
```

With a hugepage pool:

* Each NUMA node's usable memory is its pool, and `reservedmemory` is ignored: the host's own memory lies outside the pool.
* NUMA types are sized on the pool, e.g. `n1.32VCPU-100GB` and `n2.16VCPU-50GB` for a pool of 100 GiB.
* NUMA VMs are created with `hugepages` set to the pool's page size.
