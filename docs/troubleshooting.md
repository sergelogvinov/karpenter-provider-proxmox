# Troubleshooting

## Karpenter Proxmox Controller logs

Ensure that the Karpenter Proxmox controller is running and has the necessary permissions to interact with the Proxmox API.
Check the logs of the Karpenter Proxmox controller for any error messages or warnings that might indicate the cause of the issue.

## Verify Custom Resource Definitions (CRDs) and Resources

Check the status of the Custom Resource Definitions (CRDs) used by Karpenter to ensure they are have `Ready` status.

```shell
kubectl get ProxmoxTemplate -owide
kubectl get ProxmoxUnmanagedTemplate -owide
```

Output example:

```shell
NAME      ZONES   MACHINE   CPU             VGA       READY   AGE
default   3       q35       x86-64-v2-AES   serial0   True    2d5h

NAME      ZONES   NAME    READY   AGE
default   3       k8s     True    4d6h
```

* `ZONES` indicates the availability zones where the template can be used.
* `READY` indicates whether the template is ready for use.

If status is not `Ready`, describe the resource to see detailed information about the issue.

```shell
kubectl describe ProxmoxTemplate default
kubectl describe ProxmoxUnmanagedTemplate default
```

```yaml
...
Status:
  Conditions:
    Last Transition Time:  2025-09-13T03:14:44Z
    Message:
    Observed Generation:   1
    Reason:                ProxmoxVirtualMachineTemplateReady
    Status:                True
    Type:                  ProxmoxVirtualMachineTemplateReady
    Last Transition Time:  2025-09-13T03:14:44Z
    Message:
    Observed Generation:   1
    Reason:                Ready
    Status:                True
    Type:                  Ready
  Image ID:                ubuntu-amd64-9606822589293882907.qcow2
  Resources:
    Zones:  3
  Zones:
    region-1/rnd-1/1004
    region-1/rnd-3/1000
    region-1/rnd-2/1005
```

Check the conditions for any errors or warnings.
In this example, the template is ready and can be used in zones `region-1/rnd-1/1004`, `region-1/rnd-3/1000`, and `region-1/rnd-2/1005`.

Where:
* `region-1` is the Proxmox datacenter
* `rnd-1`, `rnd-2`, and `rnd-3` are the Proxmox clusters
* `1000`, `1004`, and `1005` are the Virtual Machine IDs.

## Verify ProxmoxNodeClass and NodePool configurations

Verify that the ProxmoxNodeClass and NodePool configurations are correct and match the available resources in your Proxmox environment.

```shell
kubectl get ProxmoxNodeClass -owide
```

Output example:

```shell
NAME      ZONES   BALANCE    TEMPLATE   METADATA   DISK   READY   AGE
default   2       Balanced   default    cdrom      30Gi   True    15d
```

* `ZONES` indicates the availability zones where the node class can be used.
* `READY` indicates whether the node class is ready for use.

If status is not `Ready`, describe the resource to see detailed information about the issue.

```shell
kubectl describe ProxmoxNodeClass default
```

Output example:

```yaml
...
Status:
  Conditions:
    Last Transition Time:  2025-09-13T03:28:47Z
    Message:
    Observed Generation:   49
    Reason:                InstanceMetadataOptionsReady
    Status:                True
    Type:                  InstanceMetadataOptionsReady
    Last Transition Time:  2025-09-15T05:12:05Z
    Message:
    Observed Generation:   49
    Reason:                InstanceTemplateReady
    Status:                True
    Type:                  InstanceTemplateReady
    Last Transition Time:  2025-09-15T07:52:05Z
    Message:
    Observed Generation:   49
    Reason:                Ready
    Status:                True
    Type:                  Ready
  Resources:
    Zones:  2
  Selected Zones:
    region-1/rnd-2/1005
    region-1/rnd-1/1004
```

Check the conditions for any errors or warnings.
In this example, the node class is ready and can be used in zones `region-1/rnd-1/1004` and `region-1/rnd-2/1005`.

```shell
kubectl get NodePool -owide
```

Output example:

```shell
NAME      NODECLASS   NODES   READY   AGE
default   default     1       True    15d
```

## Verify NodeClaims configurations

Check the status of the NodeClaims created by Karpenter to see if they are being provisioned correctly.


```shell
kubectl get NodeClaims -owide
```

Output example:

```shell
NAME            TYPE           CAPACITY    ZONE    NODE            READY   AGE   IMAGEID                     ID                         NODEPOOL   NODECLASS   DRIFTED
default-w6nxl   m1.1VCPU-8GB   on-demand   rnd-2   default-w6nxl   True    36m   1005-11881035043233139611   proxmox://region-1/20000   default    default
```

# References

* [Kubernetes distribution examples](/examples/README.md)
