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
  # Region where this NodeClass is applied
  # Optional: if not set, all regions will be used
  region: cluster-1

  # PlacementStrategy defines how VM should be placed across zones.
  # Optional: if not set, Balanced strategy will be used
  placementStrategy:
    zoneBalance: Balanced|AvailabilityFirst

  # InstanceTemplateRef is a reference to a Kubernetes Custom Resource
  # that defines the virtual machine template used for creating instances.
  # Required
  instanceTemplateRef:
    # Kind specifies the type of the instance template resource.
    # Valid values: ProxmoxTemplate, ProxmoxUnmanagedTemplate
    kind: ProxmoxUnmanagedTemplate
    # Name of resource
    name: k8s-node-vm-template

  # BootDevice defines the root device for the VM
  # Optional: If not specified, the system will use the storage device on which the template is stored.
  bootDevice:
    # Size of the boot device
    # Valid formats: 50G, 50Gi
    size: 50G

    # Storage specifies the storage device where the boot disk for the virtual machine will be created.
    storage: lvm

  # Tags to apply to the VMs after creation
  # Optional, in place update supported
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
    templatesRef:
      name: metadata-templates
      namespace: kube-system

    # valuesRef is a reference to user-defined values.
    # All keys from this reference can be accessed in templates as .Values.<key>
    # Optional
    valuesRef:
      name: user-data-values
      namespace: kube-system

  # SecurityGroups to apply to the VMs
  # Optional, in place update supported
  securityGroups:
    - name: kubernetes
      # Interface to apply the security group
      interface: net0
```

### Parameters:

* `region` - The name of the region to use for this NodeClass. If not set, all regions will be used. Optional.

* `placementStrategy` - The strategy to use for placing VMs across zones. Optional.
  - `zoneBalance` - Balanced or AvailabilityFirst. Defaults to Balanced.

* `instanceTemplateRef` - The template to use for creating VMs.
  - `kind` - The kind of the instance template, either [ProxmoxTemplate](nodetemplateclass.md) or [ProxmoxUnmanagedTemplate](nodetemplateclass.md).
  - `name` - The name of the instance template.

    If `kind` is `ProxmoxUnmanagedTemplate`, then VM template should be prepared manually on proxmox side.

* `bootDevice` - Defines the root device for the VM.
  - `size` - The size of the boot device, in formats like `50G`, `50Gi`, `1T`, `1Ti`.
  - `storage` - The Proxmox storage id where the boot device will be created.

* `tags` - A list of tags to apply to the VMs after creation. Optional.
  This option supports in-place update.

* `metadataOptions` - Contains parameters for specifying the cloud-init metadata. Optional, defaults type is `none`.
  - `type` - The type of the metadata to expose to the VMs. Valid values: `none` or `cdrom`.
  - `templatesRef` - Used if the type is `cdrom`. It references a secret that contains cloud-init metadata templates.
    - `name` - the secret name
    - `namespace` - the namespace of the secret
  - `valuesRef` - Used to reference a secret that contains user-defined values (Optional)
    All keys from this reference can be accessed in templates as `.Values.<key>`
    This is especially useful when working with FluxCD or other GitOps tools
    - `name` - the secret name
    - `namespace` - the namespace of the secret

* `securityGroups` - A list of security groups to apply to the VMs. Optional.
  This option supports in-place update.
  - `name` - The name of the security group.
  - `interface` - The interface to apply the security group.

Karpenter supports instance drift detection when an `ProxmoxNodeClass` is updated.
If a change affects a node, Karpenter may replace (drift) the instance to align with the new configuration.
However, some parameters __do not trigger__ drift.
Changes to these fields are ignored during drift evaluation:
* `tags`
* `metadataOptions`
* `securityGroups`

The `ProxmoxTemplate` and `ProxmoxUnmanagedTemplate` resource definitions see [here](nodetemplateclass.md).

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

Accessible values in template:
* `.Metadata.Hostname` - The hostname of the Proxmox VM.
* `.Metadata.InstanceID` - The unique identifier for the Proxmox VM ID.
* `.Metadata.InstanceType` - The type of the instance.
* `.Metadata.ProviderID` - The provider-specific identifier `proxmox://<Region>/<VMID>`.
* `.Metadata.Region` - The region where the VM is located.
* `.Metadata.Zone` - The zone where the VM is located.
* `.Metadata.Tags` - The tags associated with the NodeClass.
* `.Kubernetes.RootCA` - The Kubernetes cluster root CA certificate.
* `.Kubernetes.KubeletConfiguration` - The configuration for the Kubelet. Optional. See original [documentation](https://pkg.go.dev/k8s.io/kubelet/config/v1beta1#KubeletConfiguration) for more information.
  - `CPUManagerPolicy` - The CPU manager policy
  - `CPUCFSQuota` - The CFS quota
  - `CPUCFSQuotaPeriod` - The CFS quota period
  - `TopologyManagerPolicy` - The topology manager policy
  - `TopologyManagerScope` - The topology manager scope
  - `AllowedUnsafeSysctls` - The allowed unsafe sysctls list
  - `ClusterDNS` - The DNS servers for the cluster.
  - `MaxPods` - The maximum number of pods that can be run on the node.
  - and many more, see crd file [here](/pkg/apis/v1alpha1/nodeclass.go).
* `.Values.<key>` - User-defined values for the instance.

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
* `Tags` - The tags associated with the proxmox virtual machine.

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
