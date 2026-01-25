# Node resource allocation and optimization

The Karpenter Proxmox provider supports two types of resource allocation and optimization modes: `simple` (default) and `static`.

To change allocation mode, set flag `-node-policy` or env `NODE_POLICY` to either `simple` or `static`.

## Simple allocation mode

In this mode, the plugin periodically observes the Proxmox cluster resources and the currently running VMs. It calculates the available resources on each Proxmox node by subtracting the resources requested by running VMs from the total resources of the node.

When scheduling new VMs, the plugin checks the available resources on each node to ensure that the requested resources can be allocated.

If two or more VMs are using the same vCPUs on a Proxmox node (CPU affinity), the plugin merges their vCPUs to calculate the total vCPUs on that node.
A new VM is scheduled only if the total vCPUs (including the new VM) does not exceed the total vCPUs capacity of the node.

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
