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
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	UpdateInstanceTemplates(context.Context) error

	Create(ctx context.Context, nodeTemplateClass *v1alpha1.ProxmoxTemplate) error
	Delete(ctx context.Context, nodeTemplateClass *v1alpha1.ProxmoxTemplate) error

	ListWithFilter(ctx context.Context, filter ...func(*InstanceTemplateInfo) bool) []InstanceTemplateInfo
}

type DefaultProvider struct {
	pool                  *pxpool.ProxmoxPool
	cloudCapacityProvider cloudcapacity.Provider

	muInstanceTemplates sync.RWMutex
	instanceTemplate    map[string][]InstanceTemplateInfo

	log logr.Logger
}

const (
	InstanceTemplateStatusAvailable          = "available"
	InstanceTemplateStatusDisabled           = "disabled"
	InstanceTemplateStatusUnknown            = "unknown"
	InstanceTemplateStatusMultipleStorageIDs = "multiple_storage_ids"

	importContent = "import"
)

type InstanceTemplateInfo struct {
	// Name is the name of the template.
	Name string
	// Region is the region of the template.
	Region string
	// Zone is the zone of the template.
	Zone string
	// TemplateID is the ID of the template.
	TemplateID uint64
	// TemplateHash is the hash of the template.
	TemplateHash string
	// TemplateTags are the tags associated with the template.
	TemplateTags []string
	// TemplateStorage is the storage of boot disk for the template.
	TemplateStorageID string
	// Status of the template, e.g. "available", "disabled", etc.
	Status string
}

func NewDefaultProvider(ctx context.Context, pool *pxpool.ProxmoxPool, cloudCapacityProvider cloudcapacity.Provider) *DefaultProvider {
	log := log.FromContext(ctx).WithName("instancetemplate")

	return &DefaultProvider{
		pool:                  pool,
		log:                   log,
		cloudCapacityProvider: cloudCapacityProvider,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) error {
	log := log.FromContext(ctx).WithName("instancetemplate.Create()")

	imageID := templateClass.GetImageID()
	if templateClass.Status.ImageID != "" && templateClass.Status.ImageID != imageID {
		return fmt.Errorf("wait until old image will deleted")
	}

	regions := []string{}
	if templateClass.Spec.Region != "" {
		regions = []string{templateClass.Spec.Region}
	}

	if len(regions) == 0 {
		regions = p.pool.GetRegions()
	}

	installedZones := []string{} // templateClass.Status.InstalledZones

	for _, region := range regions {
		var (
			storageImage    *cloudcapacity.NodeStorageCapacityInfo
			storageTemplate *cloudcapacity.NodeStorageCapacityInfo
		)

		for _, storageID := range templateClass.Spec.StorageIDs {
			storageImageTmp := p.cloudCapacityProvider.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
				return slices.Contains(info.Capabilities, importContent) && len(info.Zones) != 0
			})
			if storageImage == nil && storageImageTmp != nil {
				storageImage = storageImageTmp
			}

			storageTemplateTmp := p.cloudCapacityProvider.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
				return slices.Contains(info.Capabilities, "images") && len(info.Zones) != 0
			})
			if storageTemplate == nil && storageTemplateTmp != nil {
				storageTemplate = storageTemplateTmp
			}
		}

		if storageImage == nil || storageTemplate == nil {
			log.Error(nil, "No storage found for image or template", "region", region, "storageIDs", templateClass.Spec.StorageIDs, "storageImage", storageImage, "storageTemplate", storageTemplate)

			continue
		}

		zones := lo.Intersect(storageImage.Zones, storageTemplate.Zones)
		if storageTemplate.Shared {
			zones = []string{storageTemplate.Zones[0]}
		}

		for _, zone := range zones {
			err := p.downloadImage(ctx, templateClass, region, zone, storageImage)
			if err != nil {
				continue
			}

			vmid, err := p.createTemplate(ctx, templateClass, region, zone, storageImage, storageTemplate)
			if err != nil || vmid == 0 {
				continue
			}

			p.UpdateInstanceTemplates(ctx)

			installedZones = append(installedZones, fmt.Sprintf("%s/%s/%d", region, zone, vmid))
		}
	}

	if len(installedZones) > 0 {
		templateClass.Status.ImageID = imageID
		templateClass.Status.Zones = installedZones
	}

	return nil
}

func (p *DefaultProvider) Delete(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) error {
	log := log.FromContext(ctx).WithName("instancetemplate.Delete()").WithValues("imageID", templateClass.Status.ImageID)
	log.V(1).Info("Deleting template")

	imageID := templateClass.Status.ImageID

	removedImages := []string{}

	for _, key := range templateClass.Status.Zones {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 {
			removedImages = append(removedImages, key)

			continue
		}

		region := parts[0]
		zone := parts[1]

		if len(parts) == 3 {
			vmid, _ := strconv.Atoi(parts[2])

			if err := p.deleteTemplate(ctx, region, zone, vmid); err != nil {
				log.Error(err, "Failed to delete template", "region", region, "zone", zone, "vmid", vmid)

				return fmt.Errorf("failed to delete template %d: %w", vmid, err)
			}
		}

		for _, storageID := range templateClass.Spec.StorageIDs {
			storage := p.cloudCapacityProvider.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
				return slices.Contains(info.Capabilities, importContent) && slices.Contains(info.Zones, zone)
			})
			if storage == nil {
				continue
			}

			err := p.deleteImage(ctx, templateClass, region, zone, storage)
			if err != nil {
				return fmt.Errorf("failed to delete image: %w", err)
			}
		}

		removedImages = append(removedImages, key)
	}

	templateClass.Status.Zones = lo.Filter(templateClass.Status.Zones, func(item string, index int) bool {
		return !slices.Contains(removedImages, item)
	})
	if len(templateClass.Status.Zones) > 0 {
		return fmt.Errorf("unable to delete image %s, still installed in zones: %v", imageID, templateClass.Status.Zones)
	}

	p.UpdateInstanceTemplates(ctx)

	return nil
}

func (p *DefaultProvider) ListWithFilter(ctx context.Context, filter ...func(*InstanceTemplateInfo) bool) []InstanceTemplateInfo {
	p.muInstanceTemplates.RLock()
	defer p.muInstanceTemplates.RUnlock()

	instanceTemplates := []InstanceTemplateInfo{}

	for _, region := range p.pool.GetRegions() {
		for _, info := range p.instanceTemplate[region] {
			if info.Status == InstanceTemplateStatusAvailable {
				for _, f := range filter {
					if f(&info) {
						instanceTemplates = append(instanceTemplates, info)
					}
				}
			}
		}
	}

	return instanceTemplates
}

func (p *DefaultProvider) UpdateInstanceTemplates(ctx context.Context) error {
	log := p.log.WithName("UpdateInstanceTemplates()")

	p.muInstanceTemplates.Lock()
	defer p.muInstanceTemplates.Unlock()

	instanceTemplateInfo := make(map[string][]InstanceTemplateInfo)

	for _, region := range p.pool.GetRegions() {
		log.V(1).Info("Syncing instance template for region", "region", region)

		cl, err := p.pool.GetProxmoxCluster(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		cluster, err := cl.Cluster(ctx)
		if err != nil {
			log.Error(err, "Failed to get cluster status", "region", region)

			continue
		}

		vms, err := cluster.Resources(ctx, "vm")
		if err != nil {
			log.Error(err, "Failed to list VM resources", "region", region)

			continue
		}

		templateInfo := make([]InstanceTemplateInfo, 0)

		for _, vm := range vms {
			if vm.Type != "qemu" {
				continue
			}

			if vm.Template == 1 && vm.Type == "qemu" {
				info := InstanceTemplateInfo{
					Name:       vm.Name,
					Region:     region,
					Zone:       vm.Node,
					TemplateID: vm.VMID,
					Status:     InstanceTemplateStatusUnknown,
				}

				node, err := cl.Node(ctx, vm.Node)
				if err != nil {
					log.Error(err, "cannot find node with name", "region", region, "node", vm.Node)
					continue
				}

				vmRes, err := node.VirtualMachine(ctx, int(vm.VMID))
				if err != nil {
					log.Error(err, "Failed to get VM resource", "region", region, "node", vm.Node, "vmid", vm.VMID)

					continue
				}

				info.TemplateTags = strings.Split(vmRes.Tags, ",")
				info.TemplateHash = fmt.Sprintf("%d-%d", vm.VMID, lo.Must(hashstructure.Hash(vmRes.VirtualMachineConfig.Meta, hashstructure.FormatV2, nil)))

				if vmRes.VirtualMachineConfig != nil {
					info.Status = InstanceTemplateStatusAvailable

					disks := vmRes.VirtualMachineConfig.MergeDisks()
					for _, disk := range disks {
						storageID := strings.Split(disk, ":")[0]

						if info.TemplateStorageID == "" {
							info.TemplateStorageID = storageID
						}

						if info.TemplateStorageID != storageID {
							log.V(1).Info("Multiple storage IDs found for template", "templateID", vm.VMID, "storageID", storageID)

							info.TemplateStorageID = ""
							info.Status = InstanceTemplateStatusMultipleStorageIDs

							break
						}
					}
				}

				templateInfo = append(templateInfo, info)
			}
		}

		instanceTemplateInfo[region] = templateInfo
	}

	log.V(4).Info("Instance templates updated", "instanceTemplates", len(instanceTemplateInfo))

	p.instanceTemplate = instanceTemplateInfo

	return nil
}
