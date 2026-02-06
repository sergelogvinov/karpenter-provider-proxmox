# Proxmox Scheduler

The Proxmox Scheduler is a server component that monitors the Proxmox VE environment and makes scheduling decisions for virtual machines based on CPU and memory affinity rules defined in each VM configuration.

## Features

- Pins VM vCPUs to specific physical CPU cores.
- Adjusts CPU governor settings to improve performance.
- Assigns IRQ or SR-IOV devices to the same CPU cores used by the VM.
- Optionally provides node topology information for Karpenter.

I hope some of these tasks may eventually be handled directly by Proxmox itself.

## Feature Flags

The Proxmox Scheduler can be configured using feature flags passed as environment variables or configured in the `/etc/default/proxmox-scheduler` file.

- `PROXMOX_FEATURE_FLAGS` - A comma-separated list of feature flags to enable or disable specific features. Available flags:
  - `karpenter` - Enables topology discovery for Karpenter integration.


### Flag: karpenter

When the karpenter feature flag is enabled, the Proxmox Scheduler creates a virtual machine that represents the full available capacity of the host.

The `affinity` and `numa[n]` parameters are used to expose the node topology (CPU and memory layout) to Karpenter.
This allows the Karpenter scheduler to understand the physical characteristics of the node.

VM configuration example:

```yaml
#Karpenter discovery service
affinity: 0-7,32-39,8-15,40-47,16-23,48-55,24-31,56-63
cores: 64
cpu: host
memory: 507904
name: node-capacity
numa: 1
numa0: cpus=0-15,hostnodes=0,memory=126976
numa1: cpus=16-31,hostnodes=1,memory=126976
numa2: cpus=32-47,hostnodes=2,memory=126976
numa3: cpus=48-63,hostnodes=3,memory=126976
tags: karpenter
```

The host has 4 NUMA nodes.
- Each NUMA node has 16 CPU cores and 126,976 KB of memory.
- The first NUMA node uses CPU cores 0–7 and 32–39, where:
- 0–7 are physical cores.
- 32–39 are their corresponding hyperthreads.

If your environment does not allow running the scheduler daemon directly on the host, you can instead create a virtual machine with this configuration to expose the topology information.

## Installation

```shell
sudo dpkg -i proxmox-scheduler_<version>_amd64.deb
```
