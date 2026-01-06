# Install Proxmox Karpenter Provider

## Proxmox configuration

Proxmox Karpenter Provider requires the correct privileges in order to create the VMs.

Create `Karpenter` role in Proxmox:

```shell
pveum role add Karpenter -privs "Datastore.Allocate Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Mapping.Audit Mapping.Use Sys.Audit Sys.AccessNetwork SDN.Audit SDN.Use VM.Audit VM.Allocate VM.Clone VM.Config.CDROM VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.HWType VM.Config.Cloudinit VM.Config.Options VM.PowerMgmt"
```

If you want to update VM pool membership in ProxmoxNodeClass resource, add also the following privileges to the role:

```shell
pveum role modify Karpenter --append --privs "Pool.Audit Pool.Allocate"
```

Next create a user `kubernetes@pve` for the Karpenter plugin and grant it the above role

```shell
pveum user add kubernetes@pve
pveum aclmod / -user kubernetes@pve -role Karpenter
pveum user token add kubernetes@pve karpenter -privsep 0 --comment "Kubernetes Karpenter"
```

Or more restricted way, which allows to use the token with special privileges only for the plugin:

```shell
pveum user add kubernetes@pve
pveum aclmod / -user kubernetes@pve -role Karpenter
pveum user token add kubernetes@pve karpenter -privsep 1 --comment "Kubernetes Karpenter"
pveum aclmod / --token 'kubernetes@pve!karpenter' -role Karpenter
```

## Install Karpenter Provider

All examples below assume that plugin controller runs on control-plane. Change the `nodeSelector` to match your environment if needed.

```yaml
nodeSelector:
  node-role.kubernetes.io/control-plane: ""
tolerations:
  - key: node-role.kubernetes.io/control-plane
    effect: NoSchedule
```

### Install by using kubectl

Create a Proxmox cloud config to connect to your cluster with the Proxmox user you just created.
More information about the configuration can be found in [plugin configuration file](config.md).

```yaml
# config.yaml
clusters:
  # List of Proxmox clusters
  - url: https://cluster-api-1.exmple.com:8006/api2/json
    # Skip the certificate verification, if needed
    insecure: false
    # Proxmox api token
    token_id: "kubernetes@pve!karpenter"
    token_secret: "secret"
    # Region name, which is cluster name
    region: Region-1
```

Upload the configuration to the Kubernetes as a secret

```shell
kubectl -n kube-system create secret generic karpenter-provider-proxmox --from-file=config.yaml
```

Install latest release version

```shell
kubectl apply -f https://raw.githubusercontent.com/sergelogvinov/karpenter-provider-proxmox/main/docs/deploy/karpenter-provider-proxmox.yml
```

Or install latest stable version (edge)

```shell
kubectl apply -f https://raw.githubusercontent.com/sergelogvinov/karpenter-provider-proxmox/main/docs/deploy/karpenter-provider-proxmox-edge.yml
```

### Install by using Helm

Create the helm values file, for more information see [values.yaml](/charts/karpenter-provider-proxmox/values.yaml)

```yaml
# karpenter-proxmox.yaml
config:
  clusters:
    - url: https://cluster-api-1.exmple.com:8006/api2/json
      insecure: false
      token_id: "kubernetes@pve!karpenter"
      token_secret: "secret"
      region: Region-1
```

Deploy the Proxmox Karpenter Provider using Helm:

```shell
helm upgrade -i -n kube-system -f karpenter-proxmox.yaml karpenter-proxmox oci://ghcr.io/sergelogvinov/charts/karpenter-proxmox
```
