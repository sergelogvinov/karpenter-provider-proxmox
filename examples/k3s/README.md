# k3s Kubernetes distribution

This example demonstrates how to deploy k3s worker nodes in a Proxmox environment using Karpenter.
It assumes that you have a running k3s control plane and the server join token.

This guide also assumes that both [karpenter-provider-proxmox](/docs/install.md) and proxmox-cloud-controller-manager are configured in the cluster.

This was last verified against a k3s cluster version v1.36.1+k3s1 running in a Proxmox 9.1.11

## Cloud-init template

The following example focuses on the worker node only. This was built around a custom built Alpine Linux template but other distributions such as Ubuntu, Debian, etc should work similarly with minimal changes. The Proxmox VM template must have cloud-init support configured.

The example will embed the following cloud-init configuration. Please note this should not be considered production ready as it omits numerous configurations that are outside of the scope of this document.

```yaml
#cloud-config
hostname: worker-1
manage_etc_hosts: true

users:
  - name: alpine
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/ash
    lock_passwd: false
    ssh_authorized_keys:
      - ssh-ed25519 AAAA...

write_files:
  - path: /usr/local/bin/join-k3s.sh
    permissions: '0755'
    content: |
      #!/bin/sh
      set -e
      INSTALL_K3S_VERSION="v1.36.1+k3s1" \
      K3S_URL="https://api.example.com:6443" \
      K3S_TOKEN="..." \
      INSTALL_K3S_SKIP_DOWNLOAD=false \
      sh /usr/local/bin/k3s-install.sh

runcmd:
  - swapoff -a
  - curl -fsSL https://get.k3s.io -o /usr/local/bin/k3s-install.sh
  - chmod +x /usr/local/bin/k3s-install.sh
  - /usr/local/bin/join-k3s.sh
```

We will create a Kubernetes Secret that uses both system-defined and user-defined values to be used by the ProxmoxNodeClass. The template includes a network configuration section for interface matching and a user-data section to bootstraps the k3s agent with the appropriate kubelet arguments.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: karpenter-template
  namespace: kube-system
stringData:
  network-config: |
    network:
      version: 2
      ethernets:
    {{- range .Interfaces }}
        {{ .Name }}:
          match:
            macaddress: {{ .MacAddr | lower | quote }}
          dhcp4: true
          dhcp6: false
    {{- if ne .MTU 0 }}
          mtu: {{ .MTU }}
    {{- end }}
    {{- end }}
  user-data: |
    #cloud-config
    hostname: {{ .Metadata.Hostname }}
    manage_etc_hosts: true

    users:
      - name: alpine
        sudo: ALL=(ALL) NOPASSWD:ALL
        shell: /bin/ash
        lock_passwd: false{{- with get .Values "SSHAuthorizedKeys" }}
        ssh_authorized_keys:{{- range (split . ",") }}
          - {{ . }}{{- end }}{{- end }}

    write_files:
      - path: /usr/local/bin/join-k3s.sh
        permissions: '0755'
        content: |
          #!/bin/sh
          set -e
          INSTALL_K3S_VERSION="{{ get .Values "K3sVersion" }}" \
          K3S_URL="{{ get .Values "K3sURL" }}" \
          K3S_TOKEN="{{ get .Values "K3sToken" }}" \
          INSTALL_K3S_SKIP_DOWNLOAD=false \
          sh /usr/local/bin/k3s-install.sh \
            --kubelet-arg "provider-id={{ .Metadata.ProviderID }}" \
            --kubelet-arg "register-with-taints=karpenter.sh/unregistered:NoExecute" \
            --kubelet-arg "cloud-provider=external"

    runcmd:
      - swapoff -a
      - curl -fsSL https://get.k3s.io -o /usr/local/bin/k3s-install.sh
      - chmod +x /usr/local/bin/k3s-install.sh
      - /usr/local/bin/join-k3s.sh
      - rc-update add k3s-agent default
```

Original file [examples/k3s/worker.yaml](/examples/k3s/worker.yaml)

Next, create the values secret with your k3s cluster details:

```shell
kubectl -n kube-system create secret generic karpenter-template-values \
  --from-literal=K3sURL="https://api.example.com:6443" \
  --from-literal=K3sToken="..." \
  --from-literal=K3sVersion="v1.36.1+k3s1" \
  --from-literal=SSHAuthorizedKeys="ssh-ed25519 AAAA..."
```

See also [examples/k3s/user-data-values.yaml](/examples/k3s/user-data-values.yaml)

## Define Karpenter resources

Next, we will configure Karpenter ProxmoxNodeClass resource:

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxNodeClass
metadata:
  name: default
spec:
  instanceTemplateRef:
    kind: ProxmoxUnmanagedTemplate
    name: default
  metadataOptions:
    type: cdrom
    templatesRef:
      name: karpenter-template # template secret
      namespace: kube-system
    valuesRef:
      name: karpenter-template-values # values secret
      namespace: kube-system
  bootDevice:
    size: 30Gi
    storage: local-lvm # proxmox storage pool
```

And, Karpenter NodePool resource:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  limits:
    cpu: "64"
    memory: 512Gi
  template:
    spec:
      nodeClassRef:
        group: karpenter.proxmox.sinextra.dev
        kind: ProxmoxNodeClass
        name: default
      startupTaints:
        - key: karpenter.sh/unregistered
          effect: NoExecute
      requirements:
        - key: "kubernetes.io/arch"
          operator: In
          values: ["amd64"]
```

And, Karpenter ProxmoxUnmanagedTemplate resource:

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxUnmanagedTemplate
metadata:
  name: default
spec:
  templateName: cloud-init-template
```

Original file [examples/k3s/karpenter-unmanaged.yaml](/examples/k3s/karpenter-unmanaged.yaml)

Note: ProxmoxUnmanagedTemplate requires that a Proxmox Virtual Machine template with cloud-init support
is already created and available in the Proxmox environment. The `templateName` in the ProxmoxUnmanagedTemplate must match the name of the VM template in Proxmox.

For more details, see [Proxmox Template Class](/docs/nodetemplateclass.md).

After we can check status with:

```shell
kubectl get nodepool,ProxmoxNodeClass,ProxmoxUnmanagedTemplate default
```

Result:

```shell
NAME                            NODECLASS   NODES   READY   AGE
nodepool.karpenter.sh/default   default     0       True    5m

NAME                                                      ZONES   BALANCE    TEMPLATE   METADATA   DISK   READY   AGE
proxmoxnodeclass.karpenter.proxmox.sinextra.dev/default   1       Balanced   default    cdrom      60Gi   True    5m

NAME                                                              ZONES   NAME                  READY   AGE
proxmoxunmanagedtemplate.karpenter.proxmox.sinextra.dev/default   1       cloud-init-template   True    5m
```

The status should show that the template is ready and available for use.

## Scale up the cluster

Now, we can deploy a test workload that will trigger Karpenter to create a new k3s worker node.

```shell
kubectl apply -f examples/k3s/test-karpenter.yaml
```

When the workload is applied, Karpenter will automatically provision a new worker node to accommodate the workload's resource requirements. You can monitor the Karpenter node resources using the following command:

```shell
kubectl get NodeClaim
```

Result:

```shell
NAME             TYPE           CAPACITY    ZONE          NODE             READY   AGE
default-6ggrl    c1.2VCPU-4GB   on-demand   proxmox       default-6ggrl    True    5m
```
