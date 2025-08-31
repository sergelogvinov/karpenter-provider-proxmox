# Talos Kubernetes distribution

This example demonstrates how to deploy a Talos cluster in a Proxmox environment.
It assumes that you have already generated the Talos machine configurations and successfully deployed the control plane.

```shell
talosctl gen config --output-dir _cfgs --with-docs=false --with-examples=false proxmox https://api.example.com:6443
```

## Cloud-init template

The following example focuses on the worker node only.
Secrets have been omitted for clarity.

```yaml
version: v1alpha1
debug: false
persist: true
machine:
    type: worker
    token: ...
    ca:
        crt: ...
    kubelet:
        image: ghcr.io/siderolabs/kubelet:v1.33.4
        defaultRuntimeSeccompProfileEnabled: true
        disableManifestsDirectory: true
    network: {}
    install:
        disk: /dev/sda
        image: ghcr.io/siderolabs/installer:v1.10.7
        wipe: false
    features:
        rbac: true
        stableHostname: true
        apidCheckExtKeyUsage: true
        diskQuotaSupport: true
cluster:
    id: ...
    secret: ...
    controlPlane:
        endpoint: https://api.example.com:6443
    clusterName: proxmox
    network:
        dnsDomain: cluster.local
        podSubnets:
            - 10.244.0.0/16
        serviceSubnets:
            - 10.96.0.0/12
    token: ...
    ca:
        crt: ...
```

We will convert this machine configuration into a template and use it to create a new worker node.
The following is a Go template file that includes both system-defined and user-defined values.

```yaml
version: v1alpha1
debug: false
persist: true
machine:
  type: worker
  token: {{ .Values.machineToken }}
  ca:
    crt: {{ .Values.machineCA }}
  kubelet:
    image: ghcr.io/siderolabs/kubelet:{{ .Values.kubeletVersion }}
    defaultRuntimeSeccompProfileEnabled: true
    extraArgs:
      register-with-taints: "karpenter.sh/unregistered=:NoExecute"
    extraConfig:
      {{- .Kubernetes.KubeletConfiguration | toYamlPretty | nindent 6 }}
  install:
    wipe: true
  features:
    rbac: true
    stableHostname: true
    apidCheckExtKeyUsage: true
    diskQuotaSupport: true
cluster:
  id: {{ .Values.clusterID }}
  secret: {{ .Values.clusterSecret }}
  controlPlane:
    endpoint: {{ .Values.clusterEndpoint }}
  clusterName: {{ .Values.clusterName}}
  discovery:
    enabled: false
  network:
    dnsDomain: cluster.local
    podSubnets:
      - 10.244.0.0/16
    serviceSubnets:
      - 10.96.0.0/12
  token: {{ .Values.clusterToken }}
  ca:
    crt: {{ .Kubernetes.RootCA | b64enc | quote }}
```

Original file [examples/talos/worker.yaml](/examples/talos/worker.yaml)

Next, we define the user-specific values in a separate file [examples/talos/user-data-values.yaml](/examples/talos/user-data-values.yaml).

The values file can be managed using FluxCD GitOps.
While the values may differ between clusters, the machine template itself stays consistent.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: karpenter-template-values
  namespace: kube-system
stringData:
  machineCA: ...
  machineToken: ...
  clusterID: ...
  clusterSecret: ...
  clusterEndpoint: "https://api.example.com:6443"
  clusterName: proxmox
  clusterToken: ...
  clusterCA: ...
  kubeletVersion: v1.33.4
```

Please replace `...` with the real secrets from talos machine config file.

Upload the machine template and the user-specific values to your Kubernetes cluster:

```shell
kubectl -n kube-system create secret generic karpenter-template --from-file=user-data=examples/talos/worker.yaml
kubectl -n kube-system apply -f examples/talos/user-data-values.yaml
```

Note: Updating these secrets will not cause Karpenter to initiate drift events.

## Deploy Karpenter Provider

Next, we will deploy Karpenter using the helm.
Create a secret for the Karpenter provider configuration:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: karpenter-provider-proxmox
  namespace: kube-system
stringData:
  config.yaml: |
    clusters:
      - url: https://cluster-api-1.exmple.com:8006/api2/json
        token_id: "kubernetes@pve!karpenter"
        token_secret: "secret"
        region: Region-1
```

Deploy the Karpenter provider:

```shell
helm upgrade -i -n kube-system karpenter-proxmox oci://ghcr.io/sergelogvinov/charts/karpenter-proxmox
```

For more details, see [Karpenter Installation instruction](/docs/install.md).

## Define Karpenter resources

Next, we will configure Karpenter NodeClass resource:

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxNodeClass
metadata:
  name: default
spec:
  instanceTemplateRef:
    kind: ProxmoxUnmanagedTemplate
    name: talos
  metadataOptions:
    type: cdrom
    templatesRef:
      name: karpenter-template
      namespace: kube-system
    valuesRef:
      name: karpenter-template-values
      namespace: kube-system
  bootDevice:
    size: 30Gi
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
      requirements:
        - key: "kubernetes.io/arch"
          operator: In
          values: ["amd64"]
```

Or apply already existing resources:

```shell
kubectl apply -f examples/talos/karpenter-unmanaged.yaml
```

Note: ProxmoxUnmanagedTemplate requires that the Proxmox Virtual Machine template is already created and available in the Proxmox environment.

For more details, see [Proxmox Template Class](/docs/nodetemplateclass.md).

After we can check status with:

```shell
kubectl get ProxmoxUnmanagedTemplate -owide -w
```

Result:

```shell
NAME      ZONES   NAME    READY   AGE
default   2       talos   True    5d11h
```

The status should show that the template is ready and available for use.
If the template is not ready, you may need to check conditions and logs for more information.

## Scale up the cluster

Now, we can deploy a test workload that will trigger Karpenter to create a new Talos worker node.

```shell
kubectl apply -f examples/workloads/test-statefulset.yaml
```

When the workload is applied, Karpenter will automatically provision a new Talos worker node to accommodate the workload's resource requirements. You can monitor the Karpenter node resources using the following command:

```shell
kubectl get NodeClaims -owide -w
```

Result:

```shell
NAME            TYPE           CAPACITY    ZONE    NODE            READY     AGE   IMAGEID                   ID                         NODEPOOL   NODECLASS   DRIFTED
default-6ggrl   m1.1VCPU-8GB   on-demand   rnd-2   default-6ggrl   True      87s   1002-151445218505738649   proxmox://region-1/20000   default    default
```
