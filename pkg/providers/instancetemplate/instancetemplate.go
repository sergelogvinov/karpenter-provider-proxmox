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
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"go.uber.org/multierr"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/locks"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	SyncInstanceTemplates(ctx context.Context, regions ...string) error

	// Create makes sure every zone the spec targets has a VM template that
	// matches the template hash. It returns the placements it got even when
	// some zones failed; the error joins the per-zone failures.
	Create(ctx context.Context, nodeTemplateClass *v1alpha1.ProxmoxTemplate) (CreateResult, error)
	// Delete deletes the VM templates at placements and the image imageID
	// from their zones. On failure it returns the placements still to delete.
	Delete(ctx context.Context, nodeTemplateClass *v1alpha1.ProxmoxTemplate, imageID string, placements []v1alpha1.TemplatePlacement) ([]v1alpha1.TemplatePlacement, error)
	// Update applies the spec's in-place fields to the VM templates at placements.
	Update(ctx context.Context, nodeTemplateClass *v1alpha1.ProxmoxTemplate, placements []v1alpha1.TemplatePlacement) error

	ListWithFilter(ctx context.Context, filter ...func(*InstanceTemplateInfo) bool) []InstanceTemplateInfo
}

type DefaultProvider struct {
	pool                  *pxpool.ProxmoxPool
	cloudCapacityProvider cloudcapacity.Provider

	muInstanceTemplates sync.RWMutex
	instanceTemplate    map[string][]InstanceTemplateInfo

	zoneLocks *locks.Locks

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

// CreateResult is what Create left in Proxmox for one ProxmoxTemplate.
type CreateResult struct {
	// Placements are the VM templates that match the spec's template hash.
	Placements []v1alpha1.TemplatePlacement
	// Created are the Placements this call rendered, new or replacing an
	// outdated VM template, so they already carry the in-place fields.
	Created []v1alpha1.TemplatePlacement
	// Targets are the zones the spec targets, by region, for every region
	// whose storage resolved. A VM template of a resolved region outside
	// its zones is stale.
	Targets map[string][]string
	// Unresolved are the targeted regions whose storage did not resolve, so
	// their zones are unknown and none of their VM templates are stale.
	Unresolved []string
}

// Placement returns where the template lives.
func (info *InstanceTemplateInfo) Placement() v1alpha1.TemplatePlacement {
	return v1alpha1.TemplatePlacement{Region: info.Region, Zone: info.Zone, TemplateID: info.TemplateID}
}

func NewDefaultProvider(ctx context.Context, pool *pxpool.ProxmoxPool, cloudCapacityProvider cloudcapacity.Provider) *DefaultProvider {
	log := log.FromContext(ctx).WithName("instancetemplate")

	return &DefaultProvider{
		pool:                  pool,
		zoneLocks:             locks.NewLocks(),
		log:                   log,
		cloudCapacityProvider: cloudCapacityProvider,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (CreateResult, error) {
	log := log.FromContext(ctx).WithName("instancetemplate.Create()")

	regions := []string{}
	if templateClass.Spec.Region != "" {
		regions = []string{templateClass.Spec.Region}
	}

	if len(regions) == 0 {
		regions = p.pool.List()
	}

	var (
		result = CreateResult{Targets: map[string][]string{}}
		errs   error
	)

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

			result.Unresolved = append(result.Unresolved, region)

			continue
		}

		existing := p.ListWithFilter(ctx, func(info *InstanceTemplateInfo) bool {
			return info.Region == region && info.Name == templateClass.Name
		})

		zones := targetZones(storageImage, storageTemplate, existing)
		result.Targets[region] = zones

		for _, zone := range zones {
			func(zone string) {
				p.zoneLocks.Lock(zone)
				defer p.zoneLocks.Unlock(zone)

				err := p.downloadImage(ctx, templateClass, region, zone, storageImage)
				if err != nil {
					log.Error(err, "Failed to download image", "region", region, "zone", zone)
					errs = multierr.Append(errs, fmt.Errorf("region %s zone %s: %w", region, zone, err))

					return
				}

				// Refresh instance templates cache before creating new one
				p.SyncInstanceTemplates(ctx, region)

				vmid, created, err := p.createTemplate(ctx, templateClass, region, zone, storageImage, storageTemplate)
				if err == nil && vmid == 0 {
					err = fmt.Errorf("no VM ID for template")
				}

				if err != nil {
					log.Error(err, "Failed to create template", "region", region, "zone", zone)
					errs = multierr.Append(errs, fmt.Errorf("region %s zone %s: %w", region, zone, err))

					return
				}

				placement := v1alpha1.TemplatePlacement{Region: region, Zone: zone, TemplateID: uint64(vmid)}

				result.Placements = append(result.Placements, placement)
				if created {
					result.Created = append(result.Created, placement)
				}
			}(zone)
		}

		p.SyncInstanceTemplates(ctx, region)
	}

	return result, errs
}

func (p *DefaultProvider) Delete(
	ctx context.Context,
	templateClass *v1alpha1.ProxmoxTemplate,
	imageID string,
	placements []v1alpha1.TemplatePlacement,
) ([]v1alpha1.TemplatePlacement, error) {
	log := log.FromContext(ctx).WithName("instancetemplate.Delete()").WithValues("imageID", imageID)
	log.V(1).Info("Deleting templates and images")

	for i, placement := range placements {
		if err := p.deletePlacement(ctx, templateClass, imageID, placement); err != nil {
			return placements[i:], err
		}
	}

	p.SyncInstanceTemplates(ctx)

	return nil, nil
}

func (p *DefaultProvider) Update(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate, placements []v1alpha1.TemplatePlacement) error {
	log := log.FromContext(ctx).WithName("instancetemplate.Update()").WithValues("InPlaceHash", templateClass.InPlaceHash())
	log.V(4).Info("Updating template")

	var errs error

	for _, placement := range placements {
		region := placement.Region
		zone := placement.Zone
		vmid := int(placement.TemplateID)

		log.V(4).Info("Update template for update", "region", region, "zone", zone, "vmid", vmid)

		if err := p.updateTemplate(ctx, templateClass, region, zone, vmid); err != nil {
			errs = multierr.Append(errs, err)

			continue
		}
	}

	return errs
}

func (p *DefaultProvider) ListWithFilter(ctx context.Context, filter ...func(*InstanceTemplateInfo) bool) []InstanceTemplateInfo {
	p.muInstanceTemplates.RLock()
	defer p.muInstanceTemplates.RUnlock()

	instanceTemplates := []InstanceTemplateInfo{}

	for _, region := range p.pool.List() {
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

func (p *DefaultProvider) SyncInstanceTemplates(ctx context.Context, regions ...string) error {
	log := p.log.WithName("SyncInstanceTemplates()")

	p.muInstanceTemplates.Lock()
	defer p.muInstanceTemplates.Unlock()

	if p.instanceTemplate == nil {
		p.instanceTemplate = make(map[string][]InstanceTemplateInfo)
	}

	instanceTemplates := 0

	if len(regions) == 0 {
		regions = p.pool.List()
	}

	for _, region := range regions {
		log.V(4).Info("Syncing instance template for region", "region", region)

		cl, err := p.pool.Get(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		vms, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
			Type:      cluster.ResourceTypeVM,
			GuestType: "qemu",
			Match: func(r *cluster.Resource) (bool, error) {
				return r.Template == 1, nil
			},
		})
		if err != nil {
			log.Error(err, "Failed to list VM resources", "region", region)

			continue
		}

		templateInfo := make([]InstanceTemplateInfo, 0)

		for _, vm := range vms {
			info := InstanceTemplateInfo{
				Name:       vm.Name,
				Region:     region,
				Zone:       vm.Node,
				TemplateID: uint64(vm.VMID),
				Status:     InstanceTemplateStatusUnknown,
			}

			cfg, err := cl.Nodes(vm.Node).Qemu().Config(ctx, vm.VMID, nil)
			if err != nil {
				log.Error(err, "Failed to get VM resource", "region", region, "node", vm.Node, "vmid", vm.VMID)

				continue
			}

			if cfg.Tags != nil {
				info.TemplateTags = *cfg.Tags
			}

			info.TemplateHash = templateHash(vm.VMID, cfg)

			info.Status = InstanceTemplateStatusAvailable

			for _, disk := range mergeDisks(cfg) {
				storageID, _, _ := strings.Cut(disk, ":")

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

			templateInfo = append(templateInfo, info)
			instanceTemplates++
		}

		p.instanceTemplate[region] = templateInfo
	}

	log.V(1).Info("Instance templates updated", "instanceTemplates", instanceTemplates)

	return nil
}

// mergeDisks collects every attached disk's backing volume ("storage:volume")
// across cfg's SCSI/IDE/SATA/VirtIO drives, skipping entries with no real
// backing volume (e.g. an empty ide2 cdrom, whose File is "none").
func mergeDisks(cfg *qemu.Config) []string {
	disks := make([]string, 0, len(cfg.SCSI)+len(cfg.IDE)+len(cfg.SATA)+len(cfg.VirtIO))

	for _, d := range cfg.SCSI {
		disks = append(disks, d.File)
	}

	for _, d := range cfg.IDE {
		disks = append(disks, d.File)
	}

	for _, d := range cfg.SATA {
		disks = append(disks, d.File)
	}

	for _, d := range cfg.VirtIO {
		disks = append(disks, d.File)
	}

	return slices.DeleteFunc(disks, func(d string) bool {
		return !strings.Contains(d, ":")
	})
}
