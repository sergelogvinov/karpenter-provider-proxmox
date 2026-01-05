# Karpenter Provider for Proxmox

## Overview

__Motivation__: Most of the time, my bare-metal servers have more capacity than I actually need.
However, there are two situations where I require additional capacity:

1. During maintenance – I need to migrate workloads from one server to another. In this case, I temporarily rent a virtual machine from a cloud provider (hybrid Kubernetes setup) and release it after maintenance is complete.
2. When demand grows quickly – if a service suddenly becomes more popular, I add cloud VMs for rapid scale-up. Later, I replace them with a larger bare-metal server, which often costs the same or even less than renting VMs.

When I rent a bare-metal server, I maximize its utilization by running as many VMs as the hardware allows, distributing them according to the NUMA architecture. This setup works very well. However, sometimes Kubernetes spreads pods across different VMs, which can reduce network performance. Of course, we can use pod affinity to prevent this, but it requires additional management and fine-tuning.

This brings me to an idea: implementing a node autoscaler that can automatically create VMs on my Proxmox Cluster(s) as needed. This would eliminate the need to manually run terraform to add/delete nodes, which is not the fastest process.

Also, having free resources can provide more advantages than we might initially expect:
* `Power efficiency` – Idle CPU cores can be switched to power-saving mode, reducing overall energy usage and lowering system temperature.
* `Better CPU frequency boosting` – With unused capacity, the system has additional power headroom to boost active core frequencies when needed.
* `Automated VM recreation` – Using Karpenter framework, VMs can be automatically recreated if they become unhealthy or require upgrades (drift management).
* `CPU pinning` – VMs can automatically have pinned vCPUs to specific host cores, improving cache locality and reducing latency.
* `NUMA node affinity` – VMs can be scheduled on the same NUMA node to improve performance by minimizing cross-node communication. Achieving this with Kubernetes requires identical resource requests and limits for pods, which is not always possible.

## In Scope

* [x] Dynamic VM creation and termination
* [x] Cloud-init metadata delivery via CD-ROM
* [x] Cloud-init Go-template metadata support (dynamic metadata based on instance type and node location)
* [x] Firewall security group support
* [x] Kubelet short-lived join tokens
* [x] Kubelet configuration tuning
* [x] Predefined VM template selection
* [x] Prepare VM templates via CRD
* [x] Simple IPAM (IP Address Management) for VM network interfaceses
* [x] VM optimization: Placement across zones
* [x] VM optimization: Network performance
* [ ] VM optimization: CPU pinning
* [ ] VM optimization: NUMA node affinity

## Requirements

- Kubernetes 1.30+
- Proxmox VE 8+
- Proxmox CCM plugin

## Installation

Create a configuration file with credentials for your Proxmox cluster.
Apply the configuration as a secret to the `kube-system` namespace.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: karpenter-provider-proxmox
  namespace: kube-system
stringData:
  config.yaml: |
    clusters:
      - url: https://cluster-api-1.exmple.com:8006/api2/json
        token_id: "kubernetes@pve!karpenter"
        token_secret: "secret"
        region: Region-1
```

Deploy the Karpenter Proxmox Helm chart.

```shell
helm upgrade -i -n kube-system karpenter-proxmox oci://ghcr.io/sergelogvinov/charts/karpenter-proxmox
```

For more details, see [Installation instruction](docs/install.md).

## Configuration

### Karpenter Proxmox Node Class configuration:

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxNodeClass
metadata:
  name: default
spec:
  # Tags to apply to the VM on Proxmox Dashboard (optional)
  tags:
    - k8s
    - karpenter

  # The Proxmox virtual machine template reference (required)
  instanceTemplateRef:
    # CRD resource kind: ProxmoxTemplate or ProxmoxUnmanagedTemplate
    kind: ProxmoxTemplate
    # The name of the resource
    name: default

  metadataOptions:
    # How delivery the metadata to the VM, options: none or cdrom (required)
    type: none|cdrom
    # templatesRef is used if the type is `cdrom`, that contains cloud-init metadata templates
    templatesRef:
      name: ubuntu-k8s
      namespace: kube-system

  # Firewall Security Groups to apply to the VM (optional)
  securityGroups:
    - name: kubernetes
      interface: net0
```

For more details, see:
- [Karpenter Proxmox NodeClass](docs/nodeclass.md).
- [Proxmox Virtual Machine template configuration](docs/nodetemplateclass.md)

### Karpenter Node Pool configuration:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  limits:
    cpu: "64"
    memory: 512Gi

  template:
    spec:
      nodeClassRef:
        group: karpenter.proxmox.sinextra.dev
        kind: ProxmoxNodeClass
        name: default
      requirements:
        - key: "kubernetes.io/arch"
          operator: In
          values: ["amd64"]
```

The requirements labels (key) can be:

- `kubernetes.io/arch`: The CPU architecture of the node, e.g. [`amd64`, `arm64`].
- `kubernetes.io/os`: The operating system of the node, e.g. [`linux`, `windows`].
- `topology.kubernetes.io/zone`: The zone where the node is located, e.g. [`us-west-1a`, `us-east-1b`].
- `topology.kubernetes.io/region`: The region where the node is located, e.g. [`us-west-1`, `us-east-1`].
- `node.kubernetes.io/instance-type`: The instance type of the node, e.g. [`t1.2VCPU-6GB`, `m1.2VCPU-16GB`].

Karpenter specific labels:

- `karpenter.sh/capacity-type`: The capacity type of the node [`on-demand`, `spot`, `reserved`].
- `karpenter.sh/nodepool`: The node pool to which the node belongs.
- `karpenter.proxmox.sinextra.dev/instance-family`: The instance family of the node [`t1`,`s1`, `m1`]
- `karpenter.proxmox.sinextra.dev/proxmoxnodeclass`: The Proxmox Node Class name of the node.

For more details, see:
- [Karpenter Instance Types](docs/instancetypes.md) for information about instance types.
- [Karpenter Node Pool](https://karpenter.sh/docs/concepts/nodepools/) for information about node pools spec.

## Contributing

Contributions are welcomed and appreciated!
See [Contributing](CONTRIBUTING.md) for our guidelines.

If this project is useful to you, please consider starring the [repository](https://github.com/sergelogvinov/karpenter-provider-proxmox).

## Privacy Policy

This project does not collect or send any metrics or telemetry data.
You can build the images yourself and store them in your private registry, see the [Makefile](Makefile) for details.

To provide feedback or report an issue, please use the [GitHub Issues](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues).

## Community, discussion and support

If you have any questions or want to get the latest project news, you can connect with us in the following ways:
- __Using and Deploying Karpenter?__ Reach out in the [#karpenter](https://kubernetes.slack.com/archives/C02SFFZSA2K) channel in the [Kubernetes slack](https://slack.k8s.io/) to ask questions about configuring or troubleshooting Karpenter.

## Code of Conduct

This [Code of Conduct](CODE_OF_CONDUCT.md) is adapted from the Contributor Covenant, version 1.4.

## References

- [Karpenter](https://karpenter.sh/)
- [Proxmox VE](https://www.proxmox.com/en/proxmox-ve)
- [Proxmox CCM](https://github.com/sergelogvinov/proxmox-cloud-controller-manager)

## License

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

[http://www.apache.org/licenses/LICENSE-2.0](http://www.apache.org/licenses/LICENSE-2.0)

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

---

`Proxmox®` is a registered trademark of [Proxmox Server Solutions GmbH](https://www.proxmox.com/en/about/company).
