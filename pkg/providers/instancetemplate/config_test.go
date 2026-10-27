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
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestRenderTemplateConfig(t *testing.T) {
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

	cfg := renderTemplateConfig(templateClass)

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

	cfg := renderTemplateConfig(templateClass)

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

func TestRenderTemplateConfigDefaults(t *testing.T) {
	t.Parallel()

	templateClass := &v1alpha1.ProxmoxTemplate{
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Description: "my template",
		},
	}

	cfg := renderTemplateConfig(templateClass)

	assert.Contains(t, cfg.Description, "my template Hash: ")
	assert.Equal(t, &qemu.Machine{Type: "pc"}, cfg.Machine)
	assert.Equal(t, "seabios", *cfg.BIOS)
	assert.Nil(t, cfg.Agent)
	assert.Nil(t, cfg.CPU)
	assert.Nil(t, cfg.VGA)
	assert.Nil(t, cfg.Tags)
	assert.Nil(t, cfg.OnBoot)
}

// TestRenderUpdateConfigMatchesCreate checks that updating a template rendered
// from one spec in place leaves it with the same config as creating it from
// the other spec, in both directions.
func TestRenderUpdateConfigMatchesCreate(t *testing.T) {
	t.Parallel()

	full := v1alpha1.ProxmoxTemplateSpec{
		Description:    "full",
		Machine:        "q35",
		QemuGuestAgent: &v1alpha1.QemuGuestAgent{Enabled: true},
		CPU:            &v1alpha1.CPU{Type: "host"},
		VGA:            &v1alpha1.VGA{Type: "serial0"},
		Network: []v1alpha1.Network{
			{Bridge: "vmbr0", DNSServers: []string{"1.1.1.1"}},
			{Name: "net3", Bridge: "vmbr1", Address4: "10.0.0.5/24"},
		},
		PCIDevices: []v1alpha1.PCIDevice{{Mapping: "gpu0"}, {Mapping: "gpu1"}},
		Tags:       []string{"a", "b"},
		OnBoot:     new(true),
	}

	tests := []struct {
		name string
		a, b v1alpha1.ProxmoxTemplateSpec
	}{
		{name: "empty and full", a: v1alpha1.ProxmoxTemplateSpec{}, b: full},
		{name: "serial vga and std vga", a: v1alpha1.ProxmoxTemplateSpec{VGA: &v1alpha1.VGA{Type: "serial0"}}, b: v1alpha1.ProxmoxTemplateSpec{VGA: &v1alpha1.VGA{Type: "std"}}},
		{
			name: "dns servers and none",
			a:    v1alpha1.ProxmoxTemplateSpec{Network: []v1alpha1.Network{{Bridge: "vmbr0", DNSServers: []string{"1.1.1.1"}}}},
			b:    v1alpha1.ProxmoxTemplateSpec{Network: []v1alpha1.Network{{Bridge: "vmbr0"}}},
		},
		{
			name: "two networks and one",
			a:    v1alpha1.ProxmoxTemplateSpec{Network: []v1alpha1.Network{{Bridge: "vmbr0"}, {Bridge: "vmbr1"}}},
			b:    v1alpha1.ProxmoxTemplateSpec{Network: []v1alpha1.Network{{Bridge: "vmbr0"}}},
		},
	}

	for _, tt := range tests {
		for _, dir := range []struct{ from, to v1alpha1.ProxmoxTemplateSpec }{{tt.a, tt.b}, {tt.b, tt.a}} {
			from := &v1alpha1.ProxmoxTemplate{Spec: dir.from}
			from.Name = "template"
			to := &v1alpha1.ProxmoxTemplate{Spec: dir.to}
			to.Name = "template"

			current := renderCreateConfig(from, "image.raw", "local", "local-lvm")

			update, err := renderUpdateConfig(to, current)
			require.NoError(t, err, tt.name)

			assert.Equal(t,
				encodeConfig(t, renderCreateConfig(to, "image.raw", "local", "local-lvm")),
				applyUpdateConfig(t, encodeConfig(t, current), update),
				tt.name,
			)
		}
	}
}

func TestRenderUpdateConfigDeletesExtraManagedOptions(t *testing.T) {
	t.Parallel()

	templateClass := &v1alpha1.ProxmoxTemplate{
		Spec: v1alpha1.ProxmoxTemplateSpec{Network: []v1alpha1.Network{{Bridge: "vmbr0"}}},
	}

	// net7 and cicustom were added to the template by hand.
	current := renderCreateConfig(templateClass, "image.raw", "local", "local-lvm")
	current.Net[7] = qemu.Net{Model: "virtio", Bridge: "vmbr7"}
	current.CICustom = "user=local:snippets/user.yaml"

	update, err := renderUpdateConfig(templateClass, current)
	require.NoError(t, err)

	assert.Contains(t, update.Delete, "net7")
	assert.NotContains(t, update.Delete, "cicustom")
	assert.NotContains(t, update.Delete, "scsi0")
}

func encodeConfig(t *testing.T, cfg *qemu.Config) map[string]string {
	t.Helper()

	m, err := qemu.EncodeConfig(cfg)
	require.NoError(t, err)

	return m
}

// applyUpdateConfig applies update to the encoded config current the way
// Proxmox does: delete the listed keys, then set the rest.
func applyUpdateConfig(t *testing.T, current map[string]string, update *qemu.Config) map[string]string {
	t.Helper()

	out := maps.Clone(current)
	for _, key := range update.Delete {
		delete(out, key)
	}

	sets := encodeConfig(t, update)
	delete(sets, "delete")
	maps.Copy(out, sets)

	return out
}

func TestTemplateHash(t *testing.T) {
	t.Parallel()

	templateClass := &v1alpha1.ProxmoxTemplate{Spec: v1alpha1.ProxmoxTemplateSpec{Description: "my template"}}

	assert.Equal(t, templateClass.Hash(), templateHash(100, renderTemplateConfig(templateClass)))
	assert.Regexp(t, `^100-\d+$`, templateHash(100, &qemu.Config{Description: "created by hand"}), "falls back to vmid and meta for templates Karpenter did not create")
}
