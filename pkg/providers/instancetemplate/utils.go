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
	"strings"

	"github.com/luthermonson/go-proxmox"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	goproxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmox"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (p *DefaultProvider) downloadImage(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	storage *cloudcapacity.NodeStorageCapacityInfo,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.downloadImage").WithValues("region", region, "zone", zone, "storage", storage.Name)

	imageID := templateClass.GetImageID()
	log = log.WithValues("image", imageID)

	cl, err := p.pool.GetProxmoxCluster(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	node, err := cl.Node(ctx, zone)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", zone, err)
	}

	st, err := node.Storage(ctx, storage.Name)
	if err != nil {
		return fmt.Errorf("unable to find storage with name %s: %w", storage.Name, err)
	}

	content, err := st.GetContent(ctx)
	if err != nil {
		return fmt.Errorf("unable to get storage content for storage %s: %w", storage.Name, err)
	}

	if _, found := lo.Find(content, func(c *proxmox.StorageContent) bool {
		return c.Volid == fmt.Sprintf("%s:%s/%s", storage.Name, importContent, filepath.Base(imageID))
	}); !found {
		log.V(1).Info("Download image")

		options := &proxmox.StorageDownloadURLOptions{
			Node:              node.Name,
			Content:           importContent,
			Storage:           storage.Name,
			URL:               templateClass.Spec.SourceImage.URL,
			Filename:          imageID,
			Checksum:          templateClass.Spec.SourceImage.Checksum,
			ChecksumAlgorithm: templateClass.Spec.SourceImage.ChecksumType,
		}

		upid, err := node.StorageDownloadURL(ctx, options)
		if err != nil {
			log.Error(err, "Failed to download URL")

			return fmt.Errorf("unable to download URL: %w", err)
		}

		if err := proxmox.NewTask(proxmox.UPID(upid), cl.Client).WaitFor(ctx, 5*60); err != nil {
			return fmt.Errorf("unable to download URL: %w", err)
		}
	}

	return nil
}

func (p *DefaultProvider) deleteImage(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	region string,
	zone string,
	storage *cloudcapacity.NodeStorageCapacityInfo,
) error {
	log := log.FromContext(ctx).WithName("instancetemplate.deleteImage").WithValues("region", region, "zone", zone, "storage", storage.Name)

	imageID := templateClass.Status.ImageID
	log = log.WithValues("image", imageID)

	cl, err := p.pool.GetProxmoxCluster(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	node, err := cl.Node(ctx, zone)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", zone, err)
	}

	st, err := node.Storage(ctx, storage.Name)
	if err != nil {
		return fmt.Errorf("unable to find storage with name %s: %w", storage.Name, err)
	}

	content, err := st.GetContent(ctx)
	if err != nil {
		return fmt.Errorf("unable to get storage content for storage %s: %w", storage.Name, err)
	}

	if _, found := lo.Find(content, func(c *proxmox.StorageContent) bool {
		return c.Volid == fmt.Sprintf("%s:%s/%s", storage.Name, importContent, filepath.Base(imageID))
	}); found {
		log.V(1).Info("Delete image")

		var upid string

		v := fmt.Sprintf("%s:%s/%s", storage.Name, importContent, filepath.Base(imageID))

		err := cl.Delete(ctx, fmt.Sprintf("/nodes/%s/storage/%s/content/%s", zone, storage.Name, v), &upid)
		if err != nil {
			log.Error(err, "Failed to delete storage content")

			return fmt.Errorf("unable to delete storage content: %w", err)
		}

		if err := proxmox.NewTask(proxmox.UPID(upid), cl.Client).WaitFor(ctx, 5*60); err != nil {
			return fmt.Errorf("unable to delete storage content: %w", err)
		}
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
	log = log.WithValues("image", imageID)

	templates := p.ListWithFilter(ctx, func(info *InstanceTemplateInfo) bool {
		return info.Region == region && info.Zone == zone && info.Name == templateClass.Name
	})
	if len(templates) > 0 {
		return int(templates[0].TemplateID), nil
	}

	cl, err := p.pool.GetProxmoxCluster(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster", "region", region)

		return 0, err
	}

	vmid, err := cl.GetNextID(ctx, 1000)
	if err != nil {
		return 0, fmt.Errorf("failed to get next id: %v", err)
	}

	vm := defaultVirtualMachineTemplate()
	vm["node"] = zone
	vm["vmid"] = vmid
	vm["name"] = templateClass.Name
	vm["description"] = "The virtual machine managed by Karpenter, do not delete it"

	applyVirtualMachineTemplateConfig(templateClass, vm)

	disk := fmt.Sprintf("%s:0", storageTemplate.Name)
	vm["scsi0"] = fmt.Sprintf("file=%s,format=raw,import-from=%s:%s/%s,iothread=on", disk, storageImage.Name, importContent, imageID)

	err = cl.CreateVM(ctx, zone, vm)
	if err != nil {
		log.Error(err, "Failed to create virtual machine")

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

	cl, err := p.pool.GetProxmoxCluster(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster")

		return err
	}

	return cl.DeleteVMByID(ctx, zone, vmID)
}

func applyVirtualMachineTemplateConfig(templateClass *v1alpha1.ProxmoxTemplate, vm map[string]interface{}) {
	if templateClass.Spec.Description != "" {
		vm["description"] = templateClass.Spec.Description
	}

	if templateClass.Spec.Machine != "" {
		vm["machine"] = templateClass.Spec.Machine
	}

	if templateClass.Spec.CPU != nil {
		vm["cpu"] = fmt.Sprintf("cputype=%s", templateClass.Spec.CPU.Type)

		if len(templateClass.Spec.CPU.Flags) > 0 {
			flags := strings.Join(templateClass.Spec.CPU.Flags, ",")
			vm["cpu"] = fmt.Sprintf("%s,flags=%s", vm["cpu"], flags)
		}
	}

	if templateClass.Spec.VGA != nil {
		vm["vga"] = fmt.Sprintf("type=%s", templateClass.Spec.VGA.Type)

		if templateClass.Spec.VGA.Memory != nil {
			mem := templateClass.Spec.VGA.Memory
			vm["vga"] = fmt.Sprintf("%s,memory=%d", vm["vga"], mem)
		}

		if templateClass.Spec.VGA.Type == "serial0" {
			vm["serial0"] = "socket"
		}
	}

	if templateClass.Spec.Network != nil {
		for i, iface := range templateClass.Spec.Network {
			network := goproxmox.VMNetworkDevice{
				Model:  "virtio",
				Bridge: iface.Bridge,
			}

			if iface.Model != nil {
				network.Model = *iface.Model
			}

			if iface.MTU != nil {
				mtu := int(*iface.MTU)
				network.MTU = &mtu
			}

			if iface.VLAN != nil {
				vlan := int(*iface.VLAN)
				network.Tag = &vlan
			}

			if iface.Firewall != nil {
				network.Firewall = goproxmox.NewIntOrBool(*iface.Firewall)
			}

			name := fmt.Sprintf("net%d", i)
			if iface.Name != "" {
				name = iface.Name
			}

			value, _ := network.ToString()
			vm[name] = value

			inx := strings.TrimPrefix(name, "net")
			vm[fmt.Sprintf("ipconfig%s", inx)] = "ip=dhcp,ip6=auto"
		}
	}

	if len(templateClass.Spec.Tags) > 0 {
		vm["tags"] = strings.Join(templateClass.Spec.Tags, ";")
	}
}

func defaultVirtualMachineTemplate() map[string]interface{} {
	return map[string]interface{}{
		"template": 1,
		"cores":    1,
		"sockets":  1,
		"numa":     0,
		"memory":   1024,
		"machine":  "pc",
		"bios":     "seabios",
		"ostype":   "l26",
		"scsihw":   "virtio-scsi-single",
		"boot":     "order=scsi0",
		"tablet":   0,
	}
}
