# karpenter-provider-proxmox

![Version: 0.0.1](https://img.shields.io/badge/Version-0.0.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.1.0](https://img.shields.io/badge/AppVersion-v0.1.0-informational?style=flat-square)

Karpenter for Proxmox VE.

**Homepage:** <https://github.com/sergelogvinov/karpenter-provider-proxmox>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| sergelogvinov |  | <https://github.com/sergelogvinov> |

## Source Code

* <https://github.com/sergelogvinov/karpenter-provider-proxmox>

## Proxmox permissions

```shell
# Create role Karpenter
pveum role add karpenter -privs "VM.Audit VM.Config.Disk Datastore.Allocate Datastore.AllocateSpace Datastore.Audit"

# Create user and grant permissions
pveum user add kubernetes-karpenter@pve
pveum aclmod / -user kubernetes-karpenter@pve -role karpenter
pveum user token add kubernetes-karpenter@pve karpenter -privsep 0
```

## Helm values example

```yaml
# karpenter-provider-proxmox.yaml

config:
  clusters:
    - url: https://cluster-api-1.exmple.com:8006/api2/json
      insecure: false
      token_id: "kubernetes-csi@pve!csi"
      token_secret: "key"
      region: cluster-1

# Deploy controller only on control-plane nodes
nodeSelector:
  node-role.kubernetes.io/control-plane: ""
tolerations:
  - key: node-role.kubernetes.io/control-plane
    effect: NoSchedule
```

## Deploy

```shell
# Install Karpenter
helm upgrade -i --namespace=kube-system -f karpenter-provider-proxmox.yaml \
  karpenter-provider-proxmox oci://ghcr.io/sergelogvinov/charts/karpenter-provider-proxmox
```

