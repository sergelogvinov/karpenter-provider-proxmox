# Provider configuration file

This file configures the Proxmox Karpenter Provider and defines how it connects to Proxmox VE cluster.

```yaml
clusters:
  # List of Proxmox clusters
  - url: https://cluster-api-1.exmple.com:8006/api2/json

    # Skip the certificate verification, if needed
    insecure: false

    # Proxmox api credentials
    ## Username and password (not recommended, use api tokens instead)
    username: "root@pam"
    password: "password"
    ## Proxmox api token (recommended)
    token_id: "kubernetes@pve!karpenter"
    token_secret: "secret"
    ## Proxmox api token via files, it can be used both with token_id and token_secret
    ## token_id and token_secret have priority over files
    token_id_file: "/path/to/token_id_file"
    token_secret_file: "/path/to/token_secret_file"

    # Region name, which is cluster name and `topology.kubernetes.io/region` label
    region: Region-1

  # Add more clusters if needed
  - url: https://cluster-api-2.exmple.com:8006/api2/json
    insecure: false
    token_id: "kubernetes@pve!karpenter"
    token_secret: "secret"
    region: Region-2
```

## Cluster credentials

You can define multiple clusters in the `clusters` section.

* `url` - The URL of the Proxmox cluster API.
* `insecure` - Set to `true` to skip TLS certificate verification.
* `username` - The Proxmox username (not recommended, use API tokens instead).
* `password` - The Proxmox password (not recommended, use API tokens instead).
* `token_id` - The Proxmox API token ID.
* `token_id_file` - The path to a file containing the Proxmox API token ID.
* `token_secret` - The Proxmox API token.
* `token_secret_file` - The path to a file containing the Proxmox API token secret.
* `region` - The name of the region, which is also used as `topology.kubernetes.io/region` label.
