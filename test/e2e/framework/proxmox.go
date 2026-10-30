//go:build e2e

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

package framework

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/samber/lo"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	providerconfig "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
)

// ProxmoxClient bundles the clients the e2e suite uses to talk to Proxmox
// directly, independently of what the controller under test reports back
// through Kubernetes.
type ProxmoxClient struct {
	Pool          *pxpool.ProxmoxPool
	CloudCapacity cloudcapacity.Provider
}

// NewProxmoxClient builds a ProxmoxClient from cfg.ProxmoxConfig - the same
// cloud-config.yaml format pkg/providers/config.ReadCloudConfigFromFile
// reads for the controller itself (see hack/proxmox-config.yaml) - and
// syncs its storage capacity cache once. Node capacity is not synced: the
// suite doesn't use it, and its per-node resource managers need the
// operator's options in the context. Returns (nil, nil) when
// cfg.ProxmoxConfig is unset; callers should treat a nil client as "skip
// this check" (t.Skip), the same convention used by
// proxmox-csi-plugin/test/e2e's E2E_PROXMOX_CONFIG.
func NewProxmoxClient(ctx context.Context, cfg Config) (*ProxmoxClient, error) {
	if cfg.ProxmoxConfig == "" {
		return nil, nil //nolint:nilnil
	}

	cc, err := providerconfig.ReadCloudConfigFromFile(cfg.ProxmoxConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to read proxmox cloud config %s: %w", cfg.ProxmoxConfig, err)
	}

	pool, err := pxpool.NewProxmoxPool(cc.Clusters)
	if err != nil {
		return nil, fmt.Errorf("failed to build proxmox pool from %s: %w", cfg.ProxmoxConfig, err)
	}

	capacity := cloudcapacity.NewProvider(ctx, pool, nil)

	if err := capacity.SyncNodeStorageCapacity(ctx); err != nil {
		return nil, fmt.Errorf("failed to sync proxmox node storage capacity: %w", err)
	}

	return &ProxmoxClient{Pool: pool, CloudCapacity: capacity}, nil
}

// ExpectedTemplateZones mirrors the zone-selection logic
// instancetemplate.DefaultProvider.Create applies when it provisions a
// ProxmoxTemplate: for the given region, the zones backed by a storage ID
// (from storageIDs) that supports the "import" content type (for the
// source image) intersected with a storage ID that supports the "images"
// content type (for the template itself) - or, when that template storage
// is shared, just its first zone. Kept here instead of imported from
// pkg/providers/instancetemplate so the e2e assertion stays independent of
// that package's implementation.
func (p *ProxmoxClient) ExpectedTemplateZones(region string, storageIDs []string) []string {
	var storageImage, storageTemplate *cloudcapacity.NodeStorageCapacityInfo

	for _, storageID := range storageIDs {
		if storageImage == nil {
			storageImage = p.CloudCapacity.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
				return slices.Contains(info.Capabilities, "import") && len(info.Zones) != 0
			})
		}

		if storageTemplate == nil {
			storageTemplate = p.CloudCapacity.GetStorage(region, storageID, func(info *cloudcapacity.NodeStorageCapacityInfo) bool {
				return slices.Contains(info.Capabilities, "images") && len(info.Zones) != 0
			})
		}
	}

	if storageImage == nil || storageTemplate == nil {
		return nil
	}

	if storageTemplate.Shared {
		return storageTemplate.Zones[:1]
	}

	return lo.Intersect(storageImage.Zones, storageTemplate.Zones)
}

// TemplateConfig reads the current config of the Proxmox VM template at
// placement straight from the Proxmox API, bypassing the controller's
// template cache.
func (p *ProxmoxClient) TemplateConfig(ctx context.Context, placement v1alpha1.TemplatePlacement) (*qemu.Config, error) {
	cl, err := p.Pool.Get(placement.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster %s: %w", placement.Region, err)
	}

	cfg, err := cl.Nodes(placement.Zone).Qemu().Config(ctx, int(placement.TemplateID), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get config of vm template %s: %w", placement, err)
	}

	return cfg, nil
}

// VMConfig reads the current config of the Proxmox VM behind providerID
// (proxmox://<region>/<vmid>) straight from the Proxmox API. The VM's node
// comes from the cluster's resource list, not the NodeClaim's zone label,
// so a VM that migrated is still found.
func (p *ProxmoxClient) VMConfig(ctx context.Context, providerID string) (*qemu.Config, error) {
	vmid, region, err := provider.ParseProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse providerID %s: %w", providerID, err)
	}

	cl, err := p.Pool.Get(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster %s: %w", region, err)
	}

	vm, err := p.Pool.Cluster(region).Get(ctx, pxpool.ResourceKindVM, strconv.Itoa(vmid))
	if err != nil {
		return nil, fmt.Errorf("failed to find vm %d in region %s: %w", vmid, region, err)
	}

	cfg, err := cl.Nodes(vm.Node).Qemu().Config(ctx, vmid, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get config of vm %d on %s/%s: %w", vmid, region, vm.Node, err)
	}

	return cfg, nil
}
