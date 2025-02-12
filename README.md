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

## Requirements

- Kubernetes 1.30+
- Proxmox VE 8+
- Proxmox CCM plugin

## Installation

## Contributing

Contributions are welcomed and appreciated!
See [Contributing](CONTRIBUTING.md) for our guidelines.

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
