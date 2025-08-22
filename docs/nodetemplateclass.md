# Karpenter CRD for Proxmox Virtual Machine templates

The project supports two types of template definitions:
* `ProxmoxTemplate` - the plugin is responsible for creating and maintaining the template.
* `ProxmoxUnmanagedTemplate` - the user is responsible for creating and managing the template.

Changing templates can affect Karpenter [drift detection](https://karpenter.sh/docs/concepts/disruption/#drift). You can automatically upgrade the instances by changing the source image URL (for example, using FluxCD).

## ProxmoxTemplate resource

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxTemplate
metadata:
  name: default
spec:
  # Source image parameters
  # Proxmox will download it and store in the import directory.
  sourceImage:
    # Http(s) url of the image, qcow2/raw images are supported,
    # Required value.
    url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
    # The name of destination image, karpenter will add checksum to the name later.
    # Required value.
    imageName: ubuntu-amd64.qcow2
    # After downloading, Proxmox can verify the image integrity by checking its checksum. (Optional)
    checksum: <checksum>
    # The checksum type (Optional)
    # Can be one of md5, sha1, sha224, sha256, sha384, sha512
    checksumType: md5

  # Proxmox storage IDs where the downloaded image and template will be created.
  # You can provide a list of storages, and the plugin will automatically select the most suitable one.
  # The selected storages must support import and image content types, either separately or combined.
  # Required value.
  storageIDs:
    - local
    - system

  #
  # Proxmox common Virtual Machine parameters.
  #

  # Type of virtual machine (Optional)
  # Can be one of pc, q35
  machine: q35

  # CPU configuration.
  cpu:
    # CPU type (Optional)
    # Can be one of host kvm64, x86-64-v2, x86-64-v2-AES, x86-64-v3, x86-64-v4
    # Default value is x86-64-v2-AES
    type: x86-64-v2-AES
    # CPU flags (Optional)
    # See official documentation https://pve.proxmox.com/wiki/Manual:_qm.conf
    flags: +pdpe1gb

  # VGA configuration.
  vga:
    # Type of the VGA device (Optional)
    # Can be one of none, serial0, std, vmware
    type: serial0
    # Memory of the device (Optional)
    memory: 16

  # List of network interfaces (Required)
  # At least one network interface must be defined.
  network:
    - # Name of Proxmox interface netX, e.g. net0 (Optional)
      name: net0
      # Model of network hardware (Optional)
      # Can be e1000, e1000e, rtl8139, virtio, vmxnet3
      # Default is virtio
      model: virtio
      # Bridge to attach the network device to (Required)
      bridge: vmbr0
      # MTU of the interface (Optional)
      mtu: 1500
      # VLAN tag to apply to packets on this interface (Optional)
      vlan: 15
      # Interface should be protected by the firewall (Optional)
      firewall: true

  # Tags to apply to the template (Optional)
  tags:
    - talos-k8s-proxmox
    - karpenter
```

After applying the resource, Karpenter for Proxmox downloads the image from `spec.sourceImage.url` and stores it in the Proxmox `import` storage type.
It then creates a virtual machine template on each node where this is supported.

Proxmox has limitations when importing images. Not all image formats are supported.
It is recommended to use `qcow2` or `raw` images.

Image compression is not yet supported (Proxmox 8.4)

## ProxmoxUnmanagedTemplate resource

```yaml
apiVersion: karpenter.proxmox.sinextra.dev/v1alpha1
kind: ProxmoxUnmanagedTemplate
metadata:
  name: default
spec:
  # The name of the Proxmox template (Required)
  templateName: talos
```

The Karpenter plugin searches all Proxmox nodes for the specified template name.
It then uses the matching templates to create VMs on nodes that support them.

You can define as many different templates as needed, as long as they share the same name.

### Example: manual

The Proxmox Virtual Machine template:

```yaml
acpi: 1
agent: enabled=0,fstrim_cloned_disks=0,type=virtio
arch: x86_64
bios: seabios
boot: order=scsi0
cicustom: user=local:snippets/common.yaml
cores: 1
cpu: cputype=host
cpuunits: 1024
ide2: local:1002/vm-1002-cloudinit.qcow2,media=cdrom
ipconfig0: ip=dhcp,ip6=auto
keyboard: en-us
machine: q35
memory: 512
meta: creation-qemu=9.2.0,ctime=1754811620
name: talos
nameserver: 1.1.1.1 2001:4860:4860::8888
net0: virtio=BC:24:11:7D:F3:15,bridge=vmbr0,firewall=1,mtu=1500
numa: 1
onboot: 0
ostype: l26
protection: 0
scsi0: local:1002/vm-1002-disk-0.raw,aio=io_uring,backup=1,cache=none,discard=ignore,iothread=1,replicate=1,size=3G
scsihw: virtio-scsi-single
serial0: socket
smbios1: uuid=e536baad-cf78-4543-9034-60295eed2b22
sockets: 1
tablet: 0
template: 1
vga: memory=16,type=serial0
vmgenid: 0f832f6f-eb15-4e94-a44f-03698adee947
```

Do not forget to prepare cloud-init config `snippets/common.yaml` and virtual machine image `1002/vm-1002-disk-0.raw`

### Example: terraform


```hcl
terraform {
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = "0.66.3"
    }
  }
}

provider "proxmox" {
  endpoint = "https://${var.proxmox_host}:8006/"
  insecure = true
}

variable "release" {
  type        = string
  description = "The version of the Talos image"
  default     = "1.10.5"
}

resource "proxmox_virtual_environment_download_file" "talos" {
  for_each     = { for inx, zone in local.zones : zone => inx }
  node_name    = each.key
  content_type = "iso"
  datastore_id = "local"
  file_name    = "talos.raw.xz.img"
  overwrite    = false

  decompression_algorithm = "zst"
  url                     = "https://factory.talos.dev/image/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba/v${var.release}/nocloud-amd64.raw.xz"
}

resource "proxmox_virtual_environment_vm" "template" {
  for_each    = { for inx, zone in local.zones : zone => inx }
  name        = "talos"
  node_name   = each.key
  vm_id       = each.value + 1000
  on_boot     = false
  template    = true
  description = "Talos ${var.release} template"

  machine = "q35"
  cpu {
    architecture = "x86_64"
    cores        = 1
    sockets      = 1
    numa         = true
    type         = "host"
  }

  scsi_hardware = "virtio-scsi-single"
  disk {
    file_id      = proxmox_virtual_environment_download_file.talos[each.key].id
    datastore_id = "local"
    interface    = "scsi0"
    size         = 3
    file_format  = "raw"
  }

  network_device {
    bridge   = "vmbr0"
    mtu      = 1500
    firewall = true
  }

  operating_system {
    type = "l26"
  }

  initialization {
    dns {
      servers = ["1.1.1.1", "2001:4860:4860::8888"]
    }
    ip_config {
      ipv4 {
        address = "dhcp"
      }
      ipv6 {
        address = "auto"
      }
    }

    datastore_id = "local"
    # user_data_file_id = proxmox_virtual_environment_file.machineconfig[each.key].id
  }

  serial_device {}
  vga {
    type = "serial0"
  }
}
```

# Reference

* [Proxmox Virtual Machine Configuration](https://pve.proxmox.com/wiki/Manual:_qm.conf)
* [Karpenter Documentation](https://karpenter.sh/docs/)
