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
* `capacity`: The resource capacity of the instance type.
  * `cpu`: The number of virtual CPUs.
  * `ephemeral-storage`: The boot disk size.
  * `memory`: The amount of memory.
  * `pods`: The maximum number of pods.
* `overhead`: The resource overhead for the instance type, applied in the kubelet configuration file.
  * `KubeReserved`: The resources reserved for Kubernetes system components.
    * `cpu`: The amount of CPU reserved for Kubernetes.
    * `memory`: The amount of memory reserved for Kubernetes.
  * `SystemReserved`: The resources reserved for the host system.
    * `cpu`: The amount of CPU reserved for the host system.
    * `memory`: The amount of memory reserved for the host system.
  * `EvictionThreshold`: The eviction threshold for the instance type.
    * `memory`: The minimum amount of free memory required before the eviction process is triggered.
