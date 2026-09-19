/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package instancetemplate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

func TestDefaultVirtualMachineTemplate(t *testing.T) {
	t.Parallel()

	cfg := defaultVirtualMachineTemplate()

	assert.Equal(t, &qemu.Config{
		Template:    new(true),
		ACPI:        new(true),
		Cores:       new(1),
		Sockets:     new(1),
		NUMAEnabled: new(true),
		Memory:      &qemu.Memory{Current: new(1024)},
		Balloon:     new(0),
		Machine:     &qemu.Machine{Type: "pc"},
		BIOS:        new("seabios"),
		OSType:      new("l26"),
		SCSIHW:      "virtio-scsi-single",
		Boot:        new("order=scsi0"),
		Tablet:      new(false),
	}, cfg)
}

func TestApplyVirtualMachineTemplateConfig(t *testing.T) {
	t.Parallel()

	templateClass := &v1alpha1.ProxmoxTemplate{
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Machine: "q35",
			Bios:    "ovmf",
			QemuGuestAgent: &v1alpha1.QemuGuestAgent{
				Enabled:           true,
				FsFreezeOnBackup:  new(true),
				FsTrimClonedDisks: new(false),
			},
			CPU: &v1alpha1.CPU{
				Type:  "host",
				Flags: []string{"+aes", "-pcid"},
			},
			VGA: &v1alpha1.VGA{
				Type:   "serial0",
				Memory: new(32),
			},
			Network: []v1alpha1.Network{
				{
					Bridge:   "vmbr0",
					VLAN:     new(uint16(100)),
					Firewall: new(true),
					Address4: "10.0.0.5/24",
					Gateway4: "10.0.0.1",
				},
				{
					Name:   "net1",
					Bridge: "vmbr1",
					Model:  new("e1000"),
				},
			},
			PCIDevices: []v1alpha1.PCIDevice{
				{Mapping: "gpu0", PCIe: new(true), XVga: new(true)},
			},
			Tags:   []string{"b", "a"},
			OnBoot: new(true),
		},
	}

	cfg := defaultVirtualMachineTemplate()
	applyVirtualMachineTemplateConfig(templateClass, cfg)

	assert.Equal(t, &qemu.Machine{Type: "q35"}, cfg.Machine)
	assert.Equal(t, "ovmf", *cfg.BIOS)
	assert.Equal(t, &qemu.Agent{Enabled: new(true), FreezeFs: new(true), FsTrimClonedDisks: new(false)}, cfg.Agent)
	assert.Equal(t, &qemu.CPU{Type: "host", Flags: []string{"+aes", "-pcid"}}, cfg.CPU)
	assert.Equal(t, &qemu.VGA{Type: "serial0", Memory: new(32)}, cfg.VGA)
	assert.Equal(t, "socket", cfg.Serial[0])
	assert.Equal(t, qemu.Net{Model: "virtio", Bridge: "vmbr0", Tag: new(100), Firewall: new(true)}, cfg.Net[0])
	assert.Equal(t, qemu.IPConfig{IPv4: "10.0.0.5/24", GatewayIPv4: "10.0.0.1"}, cfg.IPConfig[0])
	assert.Equal(t, qemu.Net{Model: "e1000", Bridge: "vmbr1"}, cfg.Net[1])
	assert.Equal(t, qemu.IPConfig{IPv4: "dhcp", IPv6: "auto"}, cfg.IPConfig[1])
	assert.Equal(t, qemu.HostPCI{Mapping: "gpu0", PCIe: new(true), XVGA: new(true)}, cfg.HostPCI[0])
	assert.Equal(t, &qemu.Tags{"a", "b"}, cfg.Tags)
	assert.True(t, *cfg.OnBoot)
	assert.Contains(t, cfg.Description, "Hash: ")
}

func TestApplyNetworkConfigNameIndex(t *testing.T) {
	t.Parallel()

	// Interfaces are declared out of slice order: "net1" comes first, "net0"
	// comes second. The resulting cfg.Net/cfg.IPConfig index must follow the
	// declared name, not the slice position, so users can target a specific
	// netN/ipconfigN slot regardless of list order.
	templateClass := &v1alpha1.ProxmoxTemplate{
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Network: []v1alpha1.Network{
				{
					Name:   "net1",
					Bridge: "vmbr1",
				},
				{
					Name:     "net0",
					Bridge:   "vmbr0",
					Address4: "10.0.0.5/24",
				},
			},
		},
	}

	cfg := defaultVirtualMachineTemplate()
	applyVirtualMachineTemplateConfig(templateClass, cfg)

	assert.Equal(t, qemu.Net{Model: "virtio", Bridge: "vmbr1"}, cfg.Net[1])
	assert.Equal(t, qemu.IPConfig{IPv4: "dhcp", IPv6: "auto"}, cfg.IPConfig[1])
	assert.Equal(t, qemu.Net{Model: "virtio", Bridge: "vmbr0"}, cfg.Net[0])
	assert.Equal(t, qemu.IPConfig{IPv4: "10.0.0.5/24"}, cfg.IPConfig[0])
}

func TestNetworkInterfaceIndex(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, networkInterfaceIndex("", 0))
	assert.Equal(t, 3, networkInterfaceIndex("", 3))
	assert.Equal(t, 1, networkInterfaceIndex("net1", 0))
	assert.Equal(t, 0, networkInterfaceIndex("net0", 5))
	assert.Equal(t, 2, networkInterfaceIndex("eth2", 2), "falls back to slice position when the name has no numeric netN suffix")
}

func TestApplyVirtualMachineTemplateConfigDefaults(t *testing.T) {
	t.Parallel()

	templateClass := &v1alpha1.ProxmoxTemplate{
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Description: "my template",
		},
	}

	cfg := defaultVirtualMachineTemplate()
	applyVirtualMachineTemplateConfig(templateClass, cfg)

	assert.Contains(t, cfg.Description, "my template Hash: ")
	assert.Equal(t, &qemu.Machine{Type: "pc"}, cfg.Machine)
	assert.Equal(t, "seabios", *cfg.BIOS)
	assert.Nil(t, cfg.Agent)
	assert.Nil(t, cfg.CPU)
	assert.Nil(t, cfg.VGA)
	assert.Nil(t, cfg.Tags)
	assert.Nil(t, cfg.OnBoot)
}

func TestMergeDisks(t *testing.T) {
	t.Parallel()

	cfg := &qemu.Config{
		SCSI: map[int]qemu.Drive{
			0: {File: "local-lvm:vm-100-disk-0"},
		},
		IDE: map[int]qemu.Drive{
			2: {File: "none"},
		},
		SATA: map[int]qemu.Drive{
			0: {File: "local:100/vm-100-disk-1.raw"},
		},
	}

	disks := mergeDisks(cfg)

	assert.ElementsMatch(t, []string{"local-lvm:vm-100-disk-0", "local:100/vm-100-disk-1.raw"}, disks)
}
