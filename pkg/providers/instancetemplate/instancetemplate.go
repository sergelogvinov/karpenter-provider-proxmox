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
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	UpdateInstanceTemplates(context.Context) error

	List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]InstanceTemplateInfo, error)
	ListByRegion(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass, region string) ([]InstanceTemplateInfo, error)
	Get(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass, region string, zone string) (*InstanceTemplateInfo, error)
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

func (p *DefaultProvider) List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]InstanceTemplateInfo, error) {
	log := p.log.WithName("List()")

	log.V(1).Info("Listing instance templates for node class", "nodeClass", nodeClass.Name)

	p.muInstanceTemplates.RLock()
	defer p.muInstanceTemplates.RUnlock()

	instanceTemplates := make([]InstanceTemplateInfo, 0)

	regions := []string{}
	if nodeClass.Spec.Region != "" {
		regions = []string{nodeClass.Spec.Region}
	}

	if len(regions) == 0 {
		regions = p.pool.GetRegions()
	}

	for _, region := range regions {
		for _, info := range p.instanceTemplate[region] {
			if info.Status == InstanceTemplateStatusAvailable && info.Name == nodeClass.Spec.InstanceTemplate.Name {
				instanceTemplates = append(instanceTemplates, info)
			}
		}
	}

	return instanceTemplates, nil
}

func (p *DefaultProvider) ListByRegion(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass, region string) ([]InstanceTemplateInfo, error) {
	log := p.log.WithName("ListByRegion()")

	log.V(1).Info("Listing instance templates for node class", "nodeClass", nodeClass.Name, "region", region)

	if region == "" {
		return nil, fmt.Errorf("region must be specified")
	}

	p.muInstanceTemplates.RLock()
	defer p.muInstanceTemplates.RUnlock()

	instanceTemplates := make([]InstanceTemplateInfo, 0)

	for _, info := range p.instanceTemplate[region] {
		if info.Status == InstanceTemplateStatusAvailable && info.Name == nodeClass.Spec.InstanceTemplate.Name {
			instanceTemplates = append(instanceTemplates, info)
		}
	}

	return instanceTemplates, nil
}

func (p *DefaultProvider) Get(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass, region string, zone string) (*InstanceTemplateInfo, error) {
	log := p.log.WithName("Get()")

	log.V(1).Info("Getting instance template for node class", "nodeClass", nodeClass.Name, "region", region, "zone", zone, "name", nodeClass.Spec.InstanceTemplate.Name)

	if region == "" {
		return nil, fmt.Errorf("region must be specified")
	}

	p.muInstanceTemplates.RLock()
	defer p.muInstanceTemplates.RUnlock()

	for _, info := range p.instanceTemplate[region] {
		if info.Status == InstanceTemplateStatusAvailable && info.Name == nodeClass.Spec.InstanceTemplate.Name && info.Zone == zone {
			return &info, nil
		}
	}

	return nil, fmt.Errorf("instance template %s not found in region %s and zone %s", nodeClass.Spec.InstanceTemplate.Name, region, zone)
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
