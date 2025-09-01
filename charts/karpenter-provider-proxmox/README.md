# karpenter-provider-proxmox

![Version: 0.2.2](https://img.shields.io/badge/Version-0.2.2-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.4.0](https://img.shields.io/badge/AppVersion-v0.4.0-informational?style=flat-square)

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
pveum role add Karpenter -privs "Datastore.Allocate Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Sys.Audit Sys.AccessNetwork SDN.Audit SDN.Use VM.Audit VM.Allocate VM.Clone VM.Config.CDROM VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.HWType VM.Config.Cloudinit VM.Config.Options VM.PowerMgmt"

# Create user and grant permissions
pveum user add kubernetes@pve
pveum aclmod / -user kubernetes@pve -role Karpenter
pveum user token add kubernetes@pve Karpenter -privsep 0 --comment "Kubernetes Karpenter"
```

## Helm values example

```yaml
# karpenter-provider-proxmox.yaml

config:
  clusters:
    - url: https://cluster-api-1.exmple.com:8006/api2/json
      insecure: false
      token_id: "kubernetes@pve!karpenter"
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
| settings.batchMaxDuration | string | `"10s"` | The maximum length of a batch window. The longer this is, the more pods we can consider for provisioning at one time which usually results in fewer but larger nodes. |
| settings.batchIdleDuration | string | `"1s"` | The maximum amount of time with no new ending pods that if exceeded ends the current batching window. If pods arrive faster than this time, the batching window will be extended up to the maxDuration. If they arrive slower, the pods will be batched separately. |
| settings.preferencePolicy | string | `"Respect"` | How the Karpenter scheduler should treat preferences. Preferences include preferredDuringSchedulingIgnoreDuringExecution node and pod affinities/anti-affinities and ScheduleAnyways topologySpreadConstraints. Can be one of 'Ignore' and 'Respect' |
| settings.minValuesPolicy | string | `"Strict"` | How the Karpenter scheduler treats min values. Options include 'Strict' (fails scheduling when min values can't be met) and 'BestEffort' (relaxes min values when they can't be met). |
| settings.vmMemoryOverheadPercent | float | `0.075` | The VM memory overhead as a percent that will be subtracted from the total memory for all instance types. The value of `0.075` equals to 7.5%. |
| settings.featureGates | object | `{"nodeRepair":false,"reservedCapacity":true,"spotToSpotConsolidation":false}` | Feature Gate configuration values. Feature Gates will follow the same graduation process and requirements as feature gates in Kubernetes. More information here https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/#feature-gates-for-alpha-or-beta-features. |
| settings.featureGates.nodeRepair | bool | `false` | nodeRepair is ALPHA and is disabled by default. Setting this to true will enable node repair. |
| settings.featureGates.reservedCapacity | bool | `true` | reservedCapacity is BETA and is enabled by default. Setting this will enable native on-demand capacity reservation support. |
| settings.featureGates.spotToSpotConsolidation | bool | `false` | spotToSpotConsolidation is ALPHA and is disabled by default. Setting this to true will enable spot replacement consolidation for both single and multi-node consolidation. |
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
| extraEnvs | list | `[]` | Any extra environments for Karpenter |
| extraArgs | list | `[]` | Any extra arguments for Karpenter |
| extraVolumes | list | `[]` | Additional volumes for Pods |
| extraVolumeMounts | list | `[]` | Additional volume mounts for Pods |
| customResources.nodeClass.default.enabled | bool | `false` | Create Proxmox Node Class 'default' |
| customResources.nodeClass.default.spec | object | `{"bootDevice":{"size":"30Gi"},"instanceTemplateRef":{"kind":"ProxmoxUnmanagedTemplate","name":"default"},"securityGroups":[{"interface":"net0","name":"kubernetes"}],"tags":["karpenter"]}` | Raw spec of ProxmoxNodeClass refs: https://github.com/sergelogvinov/karpenter-provider-proxmox/blob/main/docs/nodeclass.md |
| customResources.nodePools.default.enabled | bool | `false` | Create Karpenter Node Pool 'default' |
| customResources.nodePools.default.spec | object | `{"limits":{"cpu":"64","memory":"512Gi"},"template":{"spec":{"nodeClassRef":{"group":"karpenter.proxmox.sinextra.dev","kind":"ProxmoxNodeClass","name":"default"}}}}` | Raw spec of Node Pool refs: https://karpenter.sh/docs/concepts/nodepools |
| customResources.nodeTemplate.default.enabled | bool | `false` | Create Proxmox Virtual Machine Template 'default' |
| customResources.nodeTemplate.default.kind | string | `"ProxmoxUnmanagedTemplate"` | Kind of template can be: ProxmoxUnmanagedTemplate or ProxmoxTemplate |
| customResources.nodeTemplate.default.spec | object | `{"templateName":"talos"}` | Raw spec of Template refs: https://github.com/sergelogvinov/karpenter-provider-proxmox/blob/main/docs/nodetemplateclass.md |
