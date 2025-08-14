# Karpenter Provider for Proxmox

__On active development__

## Overview

__Motivation__: I usually have more capacity on bare metal than I actually need. The only time I require extra capacity is during maintenance when I need to migrate workloads to another server. If a service becomes more popular, I simply rent additional servers. In cases where workloads are highly dynamic for short periods, I scale up by provisioning additional nodes from a cloud provider—following a hybrid cloud approach. This strategy is highly cost-effective since bare metal servers are generally cheaper than cloud instances.

When I rent a server, I maximize its utilization by running as many virtual machines (VMs) as the hardware allows, distributing them according to NUMA architecture. This setup works perfect. However, sometimes Kubernetes spreads pods across different VMs, which can negatively impact network performance. Of course, we can use pod affinity to prevent this, but it requires additional management and fine-tuning.

This brings me to an idea: implementing a node autoscaler that can automatically create VMs on my Proxmox cluster as needed. This would eliminate the need to manually run terraform to add nodes, which is not the fastest process.

The benefits of not fully utilizing all bare metal resources include:
* `Power efficiency` – Free CPU cores and unused RAM can be switched to power-saving mode.
* `Better CPU frequency boosting` – The system has extra power available to boost core frequencies when needed.
* `Automated VM recreation` – VMs can be recreated on a schedule for improved manageability or to apply updates.
* `CPU pinning` – VMs CPUs pinned to specific CPU cores on the host.
* `NUMA node affinity` – VMs can be placed on the same NUMA node for better performance.

## In Scope

* [x] Dynamic creation/termination
* [x] VM template selection to create kubernetes node
* [x] The best placement strategy for VMs across zones
* [x] Firewall security groups
* [ ] Cloud-init metadata delivery by cdrom
* [ ] VM optimization: CPU pinning and NUMA node affinity
* [ ] VM optimization: Network and storage performance

## Requirements

- Kubernetes 1.30+
- Proxmox VE 8+
- Proxmox CCM plugin

## Installation

For details about how to install and deploy the Karpenter, see [Installation instruction](docs/install.md).

## Configuration

Karpenter Node Class configuration:

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

  metadataOptions:
    # How delivery the metadata to the VM, options: none, cdrom or http endpoint (required)
    type: none|cdrom|http

  # Firewall Security Groups to apply to the VM (optional)
  securityGroups:
    - name: kubernetes
      interface: net0
```

For more information, see [Karpenter Proxmox NodeClass](docs/nodeclass.md).

Karpenter Node Pool configuration:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  # Node pool name, it uses to create the node name for new VMs
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

For more information, see [Karpenter Node Pool](https://karpenter.sh/docs/concepts/nodepools/)

## Contributing

Contributions are welcomed and appreciated!
See [Contributing](CONTRIBUTING.md) for our guidelines.

## Code of Conduct

This [Code of Conduct](CODE_OF_CONDUCT.md) is adapted from the Contributor Covenant, version 1.4.

## References

* [Karpenter](https://karpenter.sh/)
* [Proxmox VE](https://www.proxmox.com/en/proxmox-ve)
* [Proxmox CCM](https://github.com/sergelogvinov/proxmox-cloud-controller-manager)

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
