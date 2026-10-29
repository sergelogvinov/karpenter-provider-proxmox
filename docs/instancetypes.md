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

## CPU share types

A CPU share type gives a VM CPUs that it shares with up to N−1 other VMs of the same size, instead of CPUs of its own.
The VMs that share the CPUs form a CPU share group: a set of host CPUs with N seats, one per VM.
Memory is never shared, each VM has its own.

CPU share types come in three families:
* `e2` family: a CPU share group of 2 seats, each VM is guaranteed 1/2 of its vCPUs (e.g., e2.2VCPU-4GB, e2.4VCPU-16GB).
* `e3` family: a CPU share group of 3 seats, each VM is guaranteed 1/3 of its vCPUs (e.g., e3.2VCPU-4GB, e3.4VCPU-16GB).
* `e4` family: a CPU share group of 4 seats, each VM is guaranteed 1/4 of its vCPUs (e.g., e4.2VCPU-4GB, e4.8VCPU-32GB).

The digit after `e` is the number of seats, not a generation as in `c1` or `m1`.

* **Capacity.** The guest sees the full vCPU count, and the node's `cpu` capacity is the full vCPU count too.
  The type is burstable: a VM may use all of its vCPUs while its neighbors are idle, but it is only guaranteed vCPU/N of them.
  Kubernetes may place up to vCPU cores of CPU requests on such a node, so choose CPU share types for workloads where that is acceptable.
* **Pricing.** A CPU share type is priced as vCPU/N CPUs plus its full memory, so `e2.2VCPU-4GB` costs as much CPU as a 1 vCPU type.
  Karpenter therefore prefers CPU share types for any NodePool that allows them.
* **Node policy.** CPU share types are offered with the `simple` and `static` node policies, and never with `simple-with-cpu-overcommit`
  (see [Node resource allocation](noderesource.md#cpu-share-types)).
* **Opt-in.** CPU share types are not part of the predefined list. They exist only when the instance types file defines them (see below),
  and a NodePool gets them only when its requirements allow them.

Even vCPU counts (2, 4, 8) are recommended, so that a CPU share group covers whole physical cores.

The [Proxmox Scheduler](scheduler.md) decides at runtime which VMs share which host CPUs, see [CPU share instance types design](scheduler-cpushare.md).

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
To keep a NodePool on dedicated CPUs, exclude the CPU share families:

```yaml
      requirements:
        - key: karpenter.proxmox.sinextra.dev/instance-family
          operator: NotIn
          values: ["e2", "e3", "e4"]
```

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

`capacity.cpu` is the full vCPU count the guest sees, not the guaranteed share.

## Generate instance types

The `instancetypes` command-line utility (`make build-instancetypes`) generates an instance types file:

```shell
instancetypes generate --cpus 2,4,8 --memfactor 2,4 --kubelet --system --eviction > instancetypes.json
```

* `--cpus`: Comma-separated list of vCPU counts (default `1,2,4`).
* `--memfactor`: Comma-separated list of memory multipliers per vCPU (default `2,3,4`).
* `--storage`: Boot disk size in GiB, omitted if zero (default `30`).
* `--cpushare`: Comma-separated list of CPU share group seats, each `2`, `3` or `4`.
  For each value N it adds the `eN` family of the same sizes, with `cpushare` set.
* `--kubelet`, `--system`, `--eviction`: Add the `overhead` for the kubelet, the OS system daemons and the eviction threshold.

For example, `--cpus 2,4,8 --memfactor 2,4 --cpushare 2,3,4` generates the `c1` and `s1` families and the
`e2`, `e3` and `e4` families with 2, 4 and 8 vCPUs and 1:2 and 1:4 memory ratios, e.g. `e3.4VCPU-16GB`.
