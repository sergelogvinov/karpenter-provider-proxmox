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
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

// renderCreateConfig renders the template config used to create a new VM
// template: the spec's options plus the boot disk imported from imageID on
// storageImage, and the TPM/EFI disks on storageTemplate.
func renderCreateConfig(templateClass *v1alpha1.ProxmoxTemplate, imageID, storageImage, storageTemplate string) *qemu.Config {
	cfg := renderTemplateConfig(templateClass)

	cfg.SCSI = map[int]qemu.Drive{
		0: {
			File:       fmt.Sprintf("%s:0", storageTemplate),
			Format:     "raw",
			ImportFrom: fmt.Sprintf("%s:%s/%s", storageImage, importContent, imageID),
			IOThread:   new(true),
		},
	}

	if templateClass.Spec.TPM != nil {
		cfg.TPMState = &qemu.TPMState{
			File:    fmt.Sprintf("%s:4", storageTemplate),
			Version: templateClass.Spec.TPM.Version,
		}
	}

	if templateClass.Spec.Bios == "ovmf" {
		cfg.EFIDisk = &qemu.EFIDisk{
			File:    fmt.Sprintf("%s:0", storageTemplate),
			EFIType: "4m",
		}
	}

	return cfg
}

// managedOptions are the config key families the template config owns. An
// in-place update deletes every key of these families that the template has
// but the spec no longer renders. Keys outside them (disks, efidisk, tpmstate,
// cicustom, ...) are never touched.
var managedOptions = []string{
	"agent",
	"bios",
	"cpu",
	"description",
	"hostpci",
	"ipconfig",
	"machine",
	"nameserver",
	"net",
	"onboot",
	"serial",
	"tags",
	"vga",
}

// renderUpdateConfig renders the template config used to update the VM
// template whose config is current in place: the spec's options, plus a
// delete of every managed option current has that the spec does not render.
func renderUpdateConfig(templateClass *v1alpha1.ProxmoxTemplate, current *qemu.Config) (*qemu.Config, error) {
	cfg := renderTemplateConfig(templateClass)

	currentOptions, err := qemu.EncodeConfig(current)
	if err != nil {
		return nil, fmt.Errorf("failed to encode current config: %w", err)
	}

	desiredOptions, err := qemu.EncodeConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode template config: %w", err)
	}

	for key := range currentOptions {
		if _, ok := desiredOptions[key]; !ok && isManagedOption(key) {
			cfg.Delete = append(cfg.Delete, key)
		}
	}

	slices.Sort(cfg.Delete)

	return cfg, nil
}

// isManagedOption reports whether key (e.g. "net3", "vga") belongs to one of
// the managedOptions families.
func isManagedOption(key string) bool {
	return slices.Contains(managedOptions, strings.TrimRight(key, "0123456789"))
}

// templateHashMarker precedes the template hash that renderTemplateConfig
// writes into a VM template's description.
const templateHashMarker = "Hash: "

var templateHashRe = regexp.MustCompile(`Hash:\s*(\d+)`)

// templateHash returns the template hash of the VM template vmid: the hash
// written into its description, or for templates Karpenter did not create,
// one derived from vmid and the template's meta.
func templateHash(vmid int, cfg *qemu.Config) string {
	if matches := templateHashRe.FindStringSubmatch(cfg.Description); len(matches) > 1 {
		return matches[1]
	}

	return fmt.Sprintf("%d-%d", vmid, lo.Must(hashstructure.Hash(cfg.Meta, hashstructure.FormatV2, nil)))
}

// renderTemplateConfig renders the options shared by the create and update
// forms: the defaults overlaid with the spec.
func renderTemplateConfig(templateClass *v1alpha1.ProxmoxTemplate) *qemu.Config {
	cfg := defaultVirtualMachineTemplate()
	cfg.Name = templateClass.Name

	cfg.Description = "The virtual machine managed by Karpenter, do not delete it. " + templateHashMarker + templateClass.Hash()
	if templateClass.Spec.Description != "" {
		cfg.Description = templateClass.Spec.Description + " " + templateHashMarker + templateClass.Hash()
	}

	if templateClass.Spec.Machine != "" {
		cfg.Machine = &qemu.Machine{Type: templateClass.Spec.Machine}
	}

	if templateClass.Spec.Bios != "" {
		cfg.BIOS = new(templateClass.Spec.Bios)
	}

	if templateClass.Spec.QemuGuestAgent != nil {
		cfg.Agent = &qemu.Agent{
			Enabled:           new(templateClass.Spec.QemuGuestAgent.Enabled),
			FreezeFs:          templateClass.Spec.QemuGuestAgent.FsFreezeOnBackup,
			FsTrimClonedDisks: templateClass.Spec.QemuGuestAgent.FsTrimClonedDisks,
		}
	}

	if templateClass.Spec.CPU != nil {
		cfg.CPU = &qemu.CPU{
			Type:  templateClass.Spec.CPU.Type,
			Flags: templateClass.Spec.CPU.Flags,
		}
	}

	if templateClass.Spec.VGA != nil {
		cfg.VGA = &qemu.VGA{
			Type:   templateClass.Spec.VGA.Type,
			Memory: templateClass.Spec.VGA.Memory,
		}

		if templateClass.Spec.VGA.Type == "serial0" {
			cfg.Serial = map[int]string{0: "socket"}
		}
	}

	applyNetworkConfig(templateClass, cfg)
	applyPCIDevicesConfig(templateClass, cfg)

	if len(templateClass.Spec.Tags) > 0 {
		tags := lo.Uniq(templateClass.Spec.Tags)
		slices.Sort(tags)

		vmTags := qemu.Tags(tags)
		cfg.Tags = &vmTags
	}

	if templateClass.Spec.OnBoot != nil {
		cfg.OnBoot = templateClass.Spec.OnBoot
	}

	return cfg
}

func applyNetworkConfig(templateClass *v1alpha1.ProxmoxTemplate, cfg *qemu.Config) {
	if templateClass.Spec.Network == nil {
		return
	}

	cfg.Net = make(map[int]qemu.Net, len(templateClass.Spec.Network))
	cfg.IPConfig = make(map[int]qemu.IPConfig, len(templateClass.Spec.Network))

	dnsservers := []string{}

	for i, iface := range templateClass.Spec.Network {
		idx := networkInterfaceIndex(iface.Name, i)

		model := "virtio"
		if iface.Model != nil {
			model = *iface.Model
		}

		net := qemu.Net{
			Model:  model,
			Bridge: iface.Bridge,
		}

		if iface.MTU != nil {
			net.MTU = new(int(*iface.MTU))
		}

		if iface.VLAN != nil {
			net.Tag = new(int(*iface.VLAN))
		}

		if iface.Firewall != nil {
			net.Firewall = iface.Firewall
		}

		cfg.Net[idx] = net

		cfg.IPConfig[idx] = qemu.IPConfig{
			IPv4:        iface.IPConfig.Address4,
			IPv6:        iface.IPConfig.Address6,
			GatewayIPv4: iface.IPConfig.Gateway4,
			GatewayIPv6: iface.IPConfig.Gateway6,
		}

		if iface.IPConfig.Address4 == "" && iface.IPConfig.Address6 == "" &&
			iface.IPConfig.Gateway4 == "" && iface.IPConfig.Gateway6 == "" {
			cfg.IPConfig[idx] = qemu.IPConfig{IPv4: "dhcp", IPv6: "auto"}
		}

		if iface.DNSServers != nil {
			dnsservers = append(dnsservers, iface.DNSServers...)
		}
	}

	if len(dnsservers) > 0 {
		cfg.Nameserver = strings.Join(dnsservers, " ")
	}
}

// networkInterfaceIndex derives the Proxmox network/ipconfig slot (netN/ipconfigN)
// from the interface name (e.g. "net1" -> 1), falling back to the slice position
// when the name is unset or does not encode a valid index.
func networkInterfaceIndex(name string, fallback int) int {
	if name == "" {
		return fallback
	}

	if idx, err := strconv.Atoi(strings.TrimPrefix(name, "net")); err == nil {
		return idx
	}

	return fallback
}

func applyPCIDevicesConfig(templateClass *v1alpha1.ProxmoxTemplate, cfg *qemu.Config) {
	if len(templateClass.Spec.PCIDevices) == 0 {
		return
	}

	cfg.HostPCI = make(map[int]qemu.HostPCI, len(templateClass.Spec.PCIDevices))

	for i, dev := range templateClass.Spec.PCIDevices {
		cfg.HostPCI[i] = qemu.HostPCI{
			Mapping: dev.Mapping,
			MDev:    dev.MDev,
			PCIe:    dev.PCIe,
			XVGA:    dev.XVga,
		}
	}
}

func defaultVirtualMachineTemplate() *qemu.Config {
	return &qemu.Config{
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
	}
}
