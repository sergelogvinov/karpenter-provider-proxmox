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

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| replicaCount | int | `1` |  |
| image.repository | string | `"ghcr.io/sergelogvinov/karpenter-provider-proxmox"` | Proxmox Karpenter image. |
| image.pullPolicy | string | `"IfNotPresent"` | Always or IfNotPresent |
| image.tag | string | `""` | Overrides the image tag whose default is the chart appVersion. |
| imagePullSecrets | list | `[]` |  |
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
| priorityClassName | string | `"system-cluster-critical"` | Controller pods priorityClassName. |
| serviceAccount | object | `{"annotations":{},"create":true,"name":""}` | Pods Service Account. ref: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/ |
| logVerbosityLevel | string | `"info"` | Log verbosity level. Can be one of 'debug', 'info', or 'error' |
| existingConfigSecret | string | `nil` | Proxmox cluster config stored in secrets. |
| existingConfigSecretKey | string | `"config.yaml"` | Proxmox cluster config stored in secrets key. |
| configFile | string | `"/etc/proxmox/config.yaml"` | Proxmox cluster config path. |
| config | object | `{"clusters":[]}` | Proxmox cluster config. ref: https://github.com/sergelogvinov/karpenter-provider-proxmox/blob/main/docs/install.md |
| initContainers | list | `[]` | Add additional init containers for the CSI controller pods. ref: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/ |
| hostAliases | list | `[]` | hostAliases Deployment pod host aliases ref: https://kubernetes.io/docs/tasks/network/customize-hosts-file-for-pods/ |
| podAnnotations | object | `{}` | Annotations for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/ |
| podLabels | object | `{}` | Labels for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/ |
| podSecurityContext | object | `{"fsGroup":65532,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532}` | Controller Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Controller Container Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":1},"type":"RollingUpdate"}` | Controller deployment update strategy type. ref: https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#updating-a-deployment |
| metrics | object | `{"enabled":false,"port":8080,"type":"annotation"}` | Prometheus metrics |
| metrics.enabled | bool | `false` | Enable Prometheus metrics. |
| metrics.port | int | `8080` | Prometheus metrics port. |
| nodeSelector | object | `{}` | Node labels for controller assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| tolerations | list | `[{"effect":"NoSchedule","key":"node-role.kubernetes.io/control-plane","operator":"Exists"},{"effect":"NoSchedule","key":"node.cloudprovider.kubernetes.io/uninitialized","operator":"Exists"}]` | Tolerations for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| affinity | object | `{}` | Affinity for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity |
| extraEnvs | list | `[]` | Any extra environments for talos-cloud-controller-manager |
| extraArgs | list | `[]` | Any extra arguments for talos-cloud-controller-manager |
| extraVolumes | list | `[]` | Additional volumes for Pods |
| extraVolumeMounts | list | `[]` | Additional volume mounts for Pods |
