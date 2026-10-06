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
          "memory": 4294967296
        },
        "1": {
          "cpus": "16-31",
          "memory": 4294967296
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
    * `reservedmemory`: The amount of memory (in bytes) to reserve for system use on the node.

- NUMA topology settings (optional):
    * `sockets`: (Optional) The number of CPU sockets on the node.
    * `threads`: (Optional) The number of threads per core on the node.
    * `uncorecaches`: (Optional) The number of uncore cache levels on the node.
    * `nodes`: (Optional) An object defining NUMA nodes on the system.
        - The keys are NUMA node IDs.
        - Each value is an object with:
            * `cpus`: A string defining the CPU cores associated with the NUMA node (e.g., "0-15" for cores 0 to 15).
            * `memory`: The amount of memory (in bytes) associated with the NUMA node.

This configuration allows you to optimize resource allocation on your Proxmox nodes by reserving specific CPU cores and memory for system use, as well as defining the NUMA topology for better VM performance.

`sockets` field defines how many physical CPU sockets are present on the node.

`threads` field defines how many threads per core are available SMT/Hyper-Threading-wise.

`uncorecaches` field defines how many uncore cache levels are present on the node.
Use the `lscpu -e` command on the Proxmox node to gather this information (output column: L3 - get maximum number +1).

`cpus` field supports both individual CPU indices and ranges (e.g., "0,2,4-6" for cores 0, 2, 4, 5, and 6).
It can be gathered from the Proxmox VE dashboard or by using the `lscpu | grep NUMA` command on the Proxmox node.

`memory` values are specified in bytes. To define memory on each NUMA node, you can use `numactl --hardware` command on the Proxmox node to get the memory distribution across NUMA nodes.
