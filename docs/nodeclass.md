# Karpenter CRD for Proxmox

## Proxmox NodeClass Configuration

The Proxmox NodeClass is a custom resource that defines the configuration for nodes in Karpenter.
It specifies how VMs should be created and managed on Proxmox.

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxNodeClass
metadata:
  name: node-class-name
spec:
  # Region name to use for this NodeClass
  # Optional: if not set, all regions will be used
  region: cluster-1

  # PlacementStrategy defines how VM should be placed across zones
  # Optional: if not set, Balanced strategy will be used
  placementStrategy:
    zoneBalance: Balanced|AvailabilityFirst

  # InstanceTemplate is the template of the VM to create
  # Required
  instanceTemplate:
    # Type is the type of the instance template
    # Valid values: template, crd
    type: template
    # Name is the name of the instance template
    name: k8s-node-vm-template

  # BootDevice defines the root device for the VM
  # Optional: If not set, it will use the block storage device where the template is located
  bootDevice:
    # Size of the boot device
    # Valid formats: 50G, 50Gi
    size: 50G

    # Storage is the storage where the boot device will be created
    storage: lvm

  # Tags to apply to the VMs after creation
  # Optional: if not set, no tags will be applied to the VMs
  tags:
    - karpenter

  # MetadataOptions contains parameters for specifying the cloud-init metadata
  # Optional, defaults type is `none`
  metadataOptions:
    # Type of the metadata to expose to the VMs
    # Valid values: none, cdrom
    type: none

    # SecretRef is used if the type is `cdrom`. It references a secret that contains cloud-init metadata.
    # It can include the following keys, all of which are optional:
    # - `user-data` - Userdata for cloud-init
    # - `meta-data` - Metadata for cloud-init
    # - `network-config` - Network configuration for cloud-init
    secretRef:
      name: metadata-templates
      namespace: kube-system

  # SecurityGroups to apply to the VMs
  # Optional: if not set, no security groups will be applied to the VMs
  securityGroups:
    - name: kubernetes
      # Interface to apply the security group
      interface: net0
```

### Parameters:

* `region` - The name of the region to use for this NodeClass. If not set, all regions will be used. Optional.

* `placementStrategy` - The strategy to use for placing VMs across zones. Optional.
  - `zoneBalance` - Balanced or AvailabilityFirst. Defaults to Balanced.

* `instanceTemplate` - The template to use for creating VMs.
  - `type` - The type of the instance template, either `template` or `crd`. (CRD is not implemented yet). If `type` is `template`, then `name` must be the name of an existing Proxmox VM template.
  - `name` - The name of the instance template.

* `bootDevice` - Defines the root device for the VM.
  - `size` - The size of the boot device, in formats like `50G`, `50Gi`, `1T`, `1Ti`.
  - `storage` - The Proxmox storage id where the boot device will be created.

* `tags` - A list of tags to apply to the VMs after creation. Optional.

* `metadataOptions` - Contains parameters for specifying the cloud-init metadata. Optional, defaults type is `none`.
  - `type` - The type of the metadata to expose to the VMs. Valid values: `none` or `cdrom`.
  - `secretRef` - Used if the type is `cdrom`. It references a secret that contains cloud-init metadata.
    - `name` - the secret name
    - `namespace` - the namespace of the secret

* `securityGroups` - A list of security groups to apply to the VMs. Optional.
  - `name` - The name of the security group.
  - `interface` - The interface to apply the security group.

## Cloud-Init metadata

Cloud-Init automates the configuration of Kubernetes instances by using metadata provided by the user. This metadata can include user data, network settings, and other instance-specific options. It is usually defined in a YAML file and can be stored in a Kubernetes Secret, which is then referenced through the `secretRef` field in the `metadataOptions`.

The provider supports go-templating for cloud-init metadata, allowing for dynamic configuration based on instance-specific variables.

The secret must contain the following keys, each key is optional.
- `user-data` - Userdata for cloud-init.
- `meta-data` - Metadata for cloud-init.
- `network-config` - Network configuration for cloud-init.

### User-data key

Official documentation can be found [here](https://cloudinit.readthedocs.io/en/latest/topics/examples.html#user-data).

The default file contents are:

```yaml
#cloud-config
```

Original template is located here [userdata.go](/pkg/providers/instance/cloudinit/userdata.go).

### Meta-data key

The `meta-data` key is used to provide additional information about the instance, such as its hostname, instance ID, and other relevant details.

The default values are:

```yaml
hostname: {{ .Hostname }}
local-hostname: {{ .Hostname }}
instance-id: {{ .InstanceID }}
{{- if .InstanceType }}
instance-type: {{ .InstanceType }}
{{- end }}
{{- if .ProviderID }}
provider-id: {{ .ProviderID }}
{{- end }}
region: {{ .Region }}
zone: {{ .Zone }}
availability-zone: {{ .Zone }}
```

This is go-template syntax, which allows for dynamic content generation based on the instance's metadata.

Accessible values in template:
* `Hostname` - The hostname of the Proxmox VM.
* `InstanceID` - The unique identifier for the Proxmox VM ID.
* `InstanceType` - The type of the instance.
* `ProviderID` - The provider-specific identifier `proxmox://<Region>/<VMID>`.
* `Region` - The region where the VM is located.
* `Zone` - The zone where the VM is located.

Original template is located here [metadata.go](/pkg/providers/instance/cloudinit/metadata.go).

### Network-config key

The `network-config` key is used to provide network configuration for the instance, such as IP addresses, routes, and DNS settings.

The default file contents are:

```yaml
version: 1
config:
- type: physical
  name:  eth0
  mac_address: 00:11:22:33:44:55
  subnets:
  - type: dhcp
```

Original template is located here [network.go](/pkg/providers/instance/cloudinit/network.go).
Currently the provider supports network config version 1.

## Template functions

The templates support several functions that let you customize Cloud-Init metadata based on the instance’s location or size.
You can find the full list of available functions here [functions.go](/pkg/providers/instance/cloudinit/functions.go)

Those functions was inspired by Helm [Template Functions and Pipelines](https://helm.sh/docs/chart_template_guide/functions_and_pipelines/).
