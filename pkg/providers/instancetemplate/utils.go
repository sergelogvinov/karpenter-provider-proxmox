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
	"time"

	"github.com/samber/lo"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	pxstorage "github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/go-proxmox-rest/task"
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

	t, err := cl.Nodes(zone).Storage().DownloadURL(ctx, storageInfo.Name, opts)
	if err != nil {
		log.Error(err, "Failed to download image")

		return fmt.Errorf("unable to download image: %w", err)
	}

	if err := t.Wait(ctx, &task.WaitOptions{Timeout: taskTimeout}); err != nil {
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

	t, err := cl.Nodes(zone).Storage().Content(storageInfo.Name).Delete(ctx, volID, 0)
	if err != nil {
		log.Error(err, "Failed to delete storage content")

		return fmt.Errorf("unable to delete storage content: %w", err)
	}

	if err := t.Wait(ctx, &task.WaitOptions{Timeout: taskTimeout}); err != nil {
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

	cfg := renderCreateConfig(templateClass, imageID, storageImage.Name, storageTemplate.Name)

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

	t, err := cl.Nodes(zone).Qemu().Create(ctx, opts)
	if err != nil {
		log.Error(err, "Failed to create virtual machine")

		return 0, fmt.Errorf("unable to create virtual machine: %w", err)
	}

	if err = t.Wait(ctx, &task.WaitOptions{Timeout: taskTimeout}); err != nil {
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

// deletePlacement deletes the template at placement and its image from every
// import storage of the zone, holding the zone lock throughout.
func (p *DefaultProvider) deletePlacement(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate, placement v1alpha1.TemplatePlacement) error {
	log := log.FromContext(ctx).WithName("instancetemplate.Delete()")

	region := placement.Region
	zone := placement.Zone
	vmid := int(placement.TemplateID)

	p.zoneLocks.Lock(zone)
	defer p.zoneLocks.Unlock(zone)

	if err := p.deleteTemplate(ctx, region, zone, vmid); err != nil {
		log.Error(err, "Failed to delete template", "region", region, "zone", zone, "vmid", vmid)

		return fmt.Errorf("failed to delete template %d: %w", vmid, err)
	}

	for _, storageID := range templateClass.Spec.StorageIDs {
		storage := p.cloudCapacityProvider.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
			return slices.Contains(info.Capabilities, importContent) && slices.Contains(info.Zones, zone)
		})
		if storage == nil {
			continue
		}

		if err := p.deleteImage(ctx, templateClass, region, zone, storage); err != nil {
			return fmt.Errorf("failed to delete image: %w", err)
		}
	}

	return nil
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

	current, err := cl.Nodes(zone).Qemu().Config(ctx, vmid, nil)
	if err != nil {
		if proxmoxrest.IsNotFound(err) {
			log.V(1).Info("Virtual machine not found, skipping update", "vmid", vmid)

			return nil
		}

		return fmt.Errorf("unable to get virtual machine: %w", err)
	}

	cfg, err := renderUpdateConfig(templateClass, current)
	if err != nil {
		return fmt.Errorf("unable to render template config: %w", err)
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
	t, err := cl.Nodes(zone).Qemu().Delete(ctx, vmid, nil)
	if err != nil {
		return err
	}

	return t.Wait(ctx, &task.WaitOptions{Timeout: taskTimeout})
}
