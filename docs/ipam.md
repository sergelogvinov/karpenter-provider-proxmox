# IP Address Management (IPAM)

Karpenter Proxmox supports IP Address Management (IPAM) for VM network interfaceses.
It allows you to define a pool of IP addresses that can be automatically assigned to VMs when they are created.

## Simple IPAM mode

In simple IPAM mode, you can define a pool of IP addresses directly in the virtual machine template (ProxmoxTemplate, ProxmoxUnmanagedTemplate).
In the cloud-init section of the template, specify the `IP Config` fields with the desired IP address pool (ProxmoxUnmanagedTemplate case).

* If the ip addresses is given subnet definition format (e.g., `192.168.0.0/24`), Karpenter Proxmox will use that subnet to allocate IP addresses.
* Before assigning an address, it checks the existing IPs already in use on Proxmox and Kubernetes nodes to avoid conflicts.
* If no gateway is specified, it defaults to using the IP address of the Proxmox node’s bridge as the gateway.
* For IPv6, it uses SLAAC mechanism to assign addresses from the specified IPv6 subnet, the subnet can be less than /64.

Example proxmox virtual machine configuration:

```conf
ipconfig0: ip=192.168.0.0/24,gw=192.168.0.1,ip6=fd00:1::0/64,gw6=fd00:1::1
```

Here:
* `ip=192.168.0.0/24` the IPv4 address pool. Warning: `192.168.0.1/24` is not valid, because it specifies ip address with subnet mask.
* `gw=192.168.0.1` specifies the IPv4 gateway.
* `ip6=fd00:1::0/64` specifies the IPv6 address pool.
* `gw6=fd00:1::1` specifies the IPv6 gateway.
