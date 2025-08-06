# Provider configuration file

This file is used to configure the Proxmox Karpenter Provider.

```yaml
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

  # Add more clusters if needed
  - url: https://cluster-api-2.exmple.com:8006/api2/json
    insecure: false
    token_id: "kubernetes@pve!karpenter"
    token_secret: "secret"
    region: Region-2
```

## Cluster list

You can define multiple clusters in the `clusters` section.

* `url` - The URL of the Proxmox cluster API.
* `insecure` - Set to `true` to skip TLS certificate verification.
* `token_id` - The Proxmox API token ID.
* `token_secret` - The name of the Kubernetes Secret that contains the Proxmox API token.
* `region` - The name of the region, which is also used as `topology.kubernetes.io/region` label.
