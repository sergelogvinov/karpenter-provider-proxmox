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
    size: 50Gi
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

  # SecurityGroups to apply to the VMs
  # Optional: if not set, no security groups will be applied to the VMs
  securityGroups:
    - name: kubernetes
      # Interface to apply the security group
      interface: net0
```

### Parameters:
