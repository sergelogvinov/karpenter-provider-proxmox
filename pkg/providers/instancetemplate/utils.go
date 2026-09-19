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
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samber/lo"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	pxstorage "github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/tasks"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// taskTimeout bounds how long a create/download/delete background task is
// waited on. These are cold-path, low-frequency operations (template
// lifecycle), so a generous timeout is preferred over a tight one.
const taskTimeout = 5 * time.Minute

func (p *DefaultProvider) downloadImage(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	storageInfo *cloudcapacity.NodeStorageCapacityInfo,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.downloadImage").WithValues("region", region, "zone", zone, "storage", storageInfo.Name)

	imageID := templateClass.GetImageID()
	log = log.WithValues("image", imageID)

	cl, err := p.pool.Get(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	volID := fmt.Sprintf("%s:%s/%s", storageInfo.Name, importContent, filepath.Base(imageID))

	content, err := cl.Nodes(zone).Storage().Content(storageInfo.Name).List(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to get storage content for storage %s: %w", storageInfo.Name, err)
	}

	if _, found := lo.Find(content, func(c pxstorage.Volume) bool {
		return c.VolID == volID
	}); found {
		return nil
	}

	opts := &pxstorage.DownloadURLOptions{
		Content:           importContent,
		URL:               templateClass.Spec.SourceImage.URL,
		Filename:          imageID,
		Checksum:          templateClass.Spec.SourceImage.Checksum,
		ChecksumAlgorithm: pxstorage.ChecksumAlgorithm(templateClass.Spec.SourceImage.ChecksumType),
	}

	upid, err := cl.Nodes(zone).Storage().DownloadURL(ctx, storageInfo.Name, opts)
	if err != nil {
		log.Error(err, "Failed to download image")

		return fmt.Errorf("unable to download image: %w", err)
	}

	if err := cl.Nodes(zone).Tasks().Wait(ctx, upid, &tasks.WaitOptions{Timeout: taskTimeout}); err != nil {
		return fmt.Errorf("unable to download image: %w", err)
	}

	return nil
}

func (p *DefaultProvider) deleteImage(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	storageInfo *cloudcapacity.NodeStorageCapacityInfo,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.deleteImage").WithValues("region", region, "zone", zone, "storage", storageInfo.Name)

	imageID := templateClass.Status.ImageID
	log = log.WithValues("image", imageID)

	cl, err := p.pool.Get(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	volID := fmt.Sprintf("%s:%s/%s", storageInfo.Name, importContent, filepath.Base(imageID))

	content, err := cl.Nodes(zone).Storage().Content(storageInfo.Name).List(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to get storage content for storage %s: %w", storageInfo.Name, err)
	}

	if _, found := lo.Find(content, func(c pxstorage.Volume) bool {
		return c.VolID == volID
	}); !found {
		return nil
	}

	log.V(1).Info("Delete image")

	upid, err := cl.Nodes(zone).Storage().Content(storageInfo.Name).Delete(ctx, volID, 0)
	if err != nil {
		log.Error(err, "Failed to delete storage content")

		return fmt.Errorf("unable to delete storage content: %w", err)
	}

	if upid == "" {
		return nil
	}

	if err := cl.Nodes(zone).Tasks().Wait(ctx, upid, &tasks.WaitOptions{Timeout: taskTimeout}); err != nil {
		return fmt.Errorf("unable to delete storage content: %w", err)
	}

	return nil
}

func (p *DefaultProvider) createTemplate(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	storageImage *cloudcapacity.NodeStorageCapacityInfo,
	storageTemplate *cloudcapacity.NodeStorageCapacityInfo,
) (int, error) {
	log := log.FromContext(ctx).WithName("instancetemplate.createTemplate").WithValues("region", region, "zone", zone, "storage", storageTemplate.Name)

	imageID := templateClass.GetImageID()
	log = log.WithValues("image", imageID, "hash", templateClass.Hash())

	vmid := 0

	templates := p.ListWithFilter(ctx, func(info *InstanceTemplateInfo) bool {
		return info.Region == region && info.Zone == zone && info.Name == templateClass.Name
	})
	for _, t := range templates {
		if t.TemplateHash == templateClass.Hash() {
			return int(t.TemplateID), nil
		}

		log.V(1).Info("Deleting outdated template", "templateID", t.TemplateID, "templateHash", t.TemplateHash)

		if err := p.deleteTemplate(ctx, region, zone, int(t.TemplateID)); err != nil {
			return 0, fmt.Errorf("failed to delete outdated template: %w", err)
		}

		vmid = int(t.TemplateID)
	}

	log.V(1).Info("Creating template")

	cl, err := p.pool.Get(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster", "region", region)

		return 0, err
	}

	if vmid == 0 {
		vmid, err = p.pool.Cluster(region).GetNextID(ctx, 1000)
		if err != nil {
			return 0, fmt.Errorf("failed to get next id: %v", err)
		}
	}

	cfg := defaultVirtualMachineTemplate()
	cfg.Name = templateClass.Name

	applyVirtualMachineTemplateConfig(templateClass, cfg)

	disk := fmt.Sprintf("%s:0", storageTemplate.Name)
	cfg.SCSI = map[int]qemu.Drive{
		0: {
			File:       disk,
			Format:     "raw",
			ImportFrom: fmt.Sprintf("%s:%s/%s", storageImage.Name, importContent, imageID),
			IOThread:   new(true),
		},
	}

	if templateClass.Spec.TPM != nil {
		cfg.TPMState = &qemu.TPMState{
			File:    fmt.Sprintf("%s:4", storageTemplate.Name),
			Version: templateClass.Spec.TPM.Version,
		}
	}

	if templateClass.Spec.Bios == "ovmf" {
		cfg.EFIDisk = &qemu.EFIDisk{
			File:    fmt.Sprintf("%s:0", storageTemplate.Name),
			EFIType: "4m",
		}
	}

	defer func() {
		if err != nil && vmid != 0 {
			if delErr := deleteQemuVM(ctx, cl, zone, vmid); delErr != nil {
				log.Error(delErr, "failed to delete vm", "vmid", vmid, "region", region, "zone", zone)
			}
		}
	}()

	opts := &qemu.CreateOptions{
		Config: *cfg,
		VMID:   vmid,
		Pool:   templateClass.Spec.ResourcePool,
	}

	upid, err := cl.Nodes(zone).Qemu().Create(ctx, opts)
	if err != nil {
		log.Error(err, "Failed to create virtual machine")

		return 0, fmt.Errorf("unable to create virtual machine: %w", err)
	}

	if err = cl.Nodes(zone).Tasks().Wait(ctx, upid, &tasks.WaitOptions{Timeout: taskTimeout}); err != nil {
		return 0, fmt.Errorf("unable to create virtual machine: %w", err)
	}

	return vmid, nil
}

func (p *DefaultProvider) deleteTemplate(
	ctx context.Context,
	region string,
	zone string,
	vmID int,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.deleteTemplate").WithValues("region", region, "zone", zone, "vmID", vmID)
	log.V(1).Info("Delete template")

	if vmID == 0 {
		return nil
	}

	templates := p.ListWithFilter(ctx, func(info *InstanceTemplateInfo) bool {
		return info.Region == region && info.Zone == zone && info.TemplateID == uint64(vmID)
	})
	if len(templates) == 0 {
		return nil
	}

	cl, err := p.pool.Get(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	return deleteQemuVM(ctx, cl, zone, vmID)
}

func (p *DefaultProvider) updateTemplate(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	vmid int,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.updateTemplate").WithValues("region", region, "zone", zone)

	templates := p.ListWithFilter(ctx, func(info *InstanceTemplateInfo) bool {
		return info.Region == region && info.Zone == zone && info.TemplateID == uint64(vmid)
	})
	if len(templates) == 0 {
		log.Info("Failed to get template", "region", region)

		return fmt.Errorf("template not found for update")
	}

	cl, err := p.pool.Get(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster", "region", region)

		return err
	}

	if _, err := cl.Nodes(zone).Qemu().Config(ctx, vmid, nil); err != nil {
		if proxmoxrest.IsNotFound(err) {
			log.V(1).Info("Virtual machine not found, skipping update", "vmid", vmid)

			return nil
		}

		return fmt.Errorf("unable to get virtual machine: %w", err)
	}

	cfg := defaultVirtualMachineTemplate()
	cfg.Name = templateClass.Name

	applyVirtualMachineTemplateConfig(templateClass, cfg)

	removeOptions := []string{}

	if cfg.Agent == nil {
		removeOptions = append(removeOptions, "agent")
	}

	if cfg.CPU == nil {
		removeOptions = append(removeOptions, "cpu")
	}

	if cfg.VGA == nil {
		removeOptions = append(removeOptions, "vga")
	}

	if cfg.Tags == nil {
		removeOptions = append(removeOptions, "tags")
	}

	if cfg.OnBoot == nil {
		removeOptions = append(removeOptions, "onboot")
	}

	for i := range 6 {
		if _, ok := cfg.HostPCI[i]; !ok {
			removeOptions = append(removeOptions, fmt.Sprintf("hostpci%d", i))
		}
	}

	for i := range 6 {
		if _, ok := cfg.Net[i]; !ok {
			removeOptions = append(removeOptions, fmt.Sprintf("net%d", i))
		}

		if _, ok := cfg.IPConfig[i]; !ok {
			removeOptions = append(removeOptions, fmt.Sprintf("ipconfig%d", i))
		}
	}

	if len(removeOptions) > 0 {
		cfg.Delete = removeOptions
	}

	log.V(4).Info("Update virtual machine", "options", cfg)

	if err := cl.Nodes(zone).Qemu().UpdateConfig(ctx, vmid, cfg); err != nil {
		log.Error(err, "Failed to update virtual machine")

		return fmt.Errorf("unable to update virtual machine: %w", err)
	}

	return nil
}

// deleteQemuVM deletes a guest and waits for the delete task to finish, if
// Proxmox returns one (an already-stopped, disk-less guest may delete
// synchronously with no task).
func deleteQemuVM(ctx context.Context, cl *proxmoxrest.Client, zone string, vmid int) error {
	upid, err := cl.Nodes(zone).Qemu().Delete(ctx, vmid, nil)
	if err != nil {
		return err
	}

	if upid == "" {
		return nil
	}

	return cl.Nodes(zone).Tasks().Wait(ctx, upid, &tasks.WaitOptions{Timeout: taskTimeout})
}

func applyVirtualMachineTemplateConfig(templateClass *v1alpha1.ProxmoxTemplate, cfg *qemu.Config) {
	cfg.Description = "The virtual machine managed by Karpenter, do not delete it. Hash: " + templateClass.Hash()
	if templateClass.Spec.Description != "" {
		cfg.Description = templateClass.Spec.Description + " Hash: " + templateClass.Hash()
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
