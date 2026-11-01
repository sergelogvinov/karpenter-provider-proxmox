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

package cloudcapacity

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	// Reserve admits a new VM into the zone's capacity if the zone has room for it.
	// It returns the reserved resources: req plus, for pinned placement, the host CPUs
	// and NUMA split the VM got.
	Reserve(ctx context.Context, region, zone string, req resources.VMResources) (resources.VMResources, error)
	// ChooseZone is zone choice: it reserves req in the candidate zone of region whose fit
	// best suits strategy, and returns that zone and the reserved resources, as Reserve does.
	// The caller releases the reservation like any other.
	// It fails with ErrNoZoneFits when no candidate zone has room, and with the zone's
	// error, without ErrNoZoneFits, when the request is invalid there.
	ChooseZone(ctx context.Context, region string, zones []string, strategy *v1alpha1.PlacementStrategy, req resources.VMResources) (string, resources.VMResources, error)
	// Release forgets VM id's record and returns what it held to the zone's capacity.
	// Releasing a VM that has no record changes nothing.
	Release(ctx context.Context, region, zone string, id int) error

	// SyncNodeCapacity updates the node CPU and RAM capacity information for all regions.
	SyncNodeCapacity(ctx context.Context) error
	// SyncNodeStorageCapacity updates the node storage capacity information for all regions.
	SyncNodeStorageCapacity(ctx context.Context) error

	// UpdateNodeLoad updates the node CPU load information for all regions.
	UpdateNodeLoad(ctx context.Context) error

	// Regions returns a list of regions available.
	Regions() []string
	// Zones returns a list of zones available in the specified region.
	Zones(region string) []string

	// FitInZone reports whether req has a fit in the zone now: nil when it fits, an error wrapping
	// zonecapacity.ErrNotOffered when the zone's node policy never offers it, any other error otherwise.
	FitInZone(region, zone string, req resources.VMResources) error
	// Slots is every NUMA slot shape of the zone with its free slots; nil when the zone is unavailable
	// or under shared placement, which offers no NUMA slot.
	Slots(region, zone string) []zonecapacity.SlotOffer
	// HugePageSize is the page size, in bytes, of the zone's hugepage pool; 0 when the zone has none
	// or was never synced. A zone missing from later syncs, so unavailable for placement, keeps the
	// page size it was last synced with, so that what depends on the pool does not flip while the
	// zone is out.
	HugePageSize(region, zone string) uint64
	// Root reports whether the controller logs in as root@pam in region (ADR 0003).
	Root(region string) bool

	GetStorage(region string, storage string, filter ...func(*NodeStorageCapacityInfo) bool) *NodeStorageCapacityInfo
	GetNetwork(region string, node string, filter ...func(*NodeNetworkIfaceInfo) bool) *NodeNetworkIfaceInfo
}

type DefaultProvider struct {
	pool *pxpool.ProxmoxPool

	zoneList map[string][]string

	// muSync serializes capacity syncs.
	muSync sync.Mutex

	muCapacityInfo sync.RWMutex
	capacityInfo   map[string]NodeCapacityInfo
	// hugePageSizes is the page size of each zone's hugepage pool as of the last sync that saw the
	// zone, guarded by muCapacityInfo; a zone is never removed from it.
	hugePageSizes map[string]uint64
	// releasedDuringSync collects, per zone, the VMs released while a sync builds new zones,
	// so the swap does not bring back VMs the build saw before their release.
	releasedDuringSync map[string][]int

	muStorageInfo sync.RWMutex
	storageInfo   map[string]NodeStorageCapacityInfo

	muNetworkInfo sync.RWMutex
	networkInfo   map[string]NodeNetworkIfaceInfo

	log logr.Logger

	// privileges tells per region whether the controller logs in as root@pam.
	privileges config.Privileges
	// warnedNoNodeCapacityGuest are the zones already warned about pinned placement without a
	// node-capacity guest, guarded by muSync.
	warnedNoNodeCapacityGuest map[string]bool

	// afterBuild, if set, runs between building the new zones and swapping them in.
	afterBuild func()
}

// NewProvider returns a provider over pool. privileges tells per region whether the controller is root;
// a region missing from it, or a nil privileges, is not root.
func NewProvider(ctx context.Context, pool *pxpool.ProxmoxPool, privileges config.Privileges) *DefaultProvider {
	log := log.FromContext(ctx).WithName("cloudcapacity")

	return &DefaultProvider{
		pool:                      pool,
		log:                       log,
		privileges:                privileges,
		warnedNoNodeCapacityGuest: map[string]bool{},
	}
}

func (p *DefaultProvider) Reserve(ctx context.Context, region, zone string, req resources.VMResources) (resources.VMResources, error) {
	log := log.FromContext(ctx).WithName("Reserve()").WithValues("region", region, "zone", zone)
	log.V(1).Info("Reserving capacity", "vmID", req.ID, "cpu", req.CPUs, "memory", req.Memory, "storage", req.DiskGBytes)

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	key := fmt.Sprintf("%s/%s", region, zone)
	if info, ok := p.capacityInfo[key]; ok && info.Capacity != nil {
		if info.omitted {
			return resources.VMResources{}, fmt.Errorf("zone %s/%s is unavailable", region, zone)
		}

		reserved := req
		if err := info.Capacity.Reserve(&reserved); err != nil {
			return resources.VMResources{}, fmt.Errorf("failed to reserve capacity in zone %s/%s: %w", region, zone, err)
		}

		log.V(1).Info("Capacity reserved successfully", "resourceStatus", info.Capacity.String())

		return reserved, nil
	}

	return resources.VMResources{}, fmt.Errorf("no zone capacity found for zone %s/%s", region, zone)
}

func (p *DefaultProvider) Release(ctx context.Context, region, zone string, id int) error {
	log := log.FromContext(ctx).WithName("Release()").WithValues("region", region, "zone", zone)
	log.V(1).Info("Releasing capacity", "vmID", id)

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	key := fmt.Sprintf("%s/%s", region, zone)
	if info, ok := p.capacityInfo[key]; ok && info.Capacity != nil {
		info.Capacity.Release(id)

		if p.releasedDuringSync != nil {
			p.releasedDuringSync[key] = append(p.releasedDuringSync[key], id)
		}

		log.V(1).Info("Capacity released successfully", "resourceStatus", info.Capacity.String())

		return nil
	}

	return fmt.Errorf("no zone capacity found for zone %s/%s", region, zone)
}

// SyncNodeCapacity rebuilds every zone's capacity from Proxmox without holding the lock,
// then swaps it in, keeping the pending reservations of the zones it replaces or omits.
func (p *DefaultProvider) SyncNodeCapacity(ctx context.Context) error {
	log := p.log.WithName("SyncNodeCapacity()")

	p.muSync.Lock()
	defer p.muSync.Unlock()

	p.muCapacityInfo.Lock()
	p.releasedDuringSync = map[string][]int{}
	p.muCapacityInfo.Unlock()

	capacityInfo := make(map[string]NodeCapacityInfo)
	networkIfaceInfo := make(map[string]NodeNetworkIfaceInfo)
	zoneList := make(map[string][]string)

	for _, region := range p.pool.List() {
		log.V(1).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.Get(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		ns, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
			Type: cluster.ResourceTypeNode,
			Match: func(n *cluster.Resource) (bool, error) {
				return n.Status == "online", nil // nolint:goconst
			},
		})
		if err != nil {
			log.Error(err, "Failed to get nodes for region", "region", region)

			continue
		}

		haMaintenanceNodes, err := getHAMaintenanceNodes(ctx, cl)
		if err != nil {
			log.Error(err, "Failed to get HA status for region", "region", region)
		}

		nodes := make([]string, 0, len(ns))

		// Permission: Sys.Audit
		for idx := range ns {
			item := ns[idx]

			if haMaintenanceNodes[item.Node] {
				log.V(1).Info("Skipping node in HA maintenance mode", "node", item.Node, "region", region)

				continue
			}

			log.V(4).Info("Processing node", "node", item.Node, "region", region, "maxCPU", item.MaxCPU, "maxMem", item.MaxMem)

			key := fmt.Sprintf("%s/%s", region, item.Node)

			nodes = append(nodes, item.Node)

			nodeCapacity, err := getNodeCapacity(ctx, cl, region, &item)
			if err != nil {
				log.Error(err, "Failed to get capacity for node", "node", item.Node, "region", region)

				continue
			}

			capacityInfo[key] = nodeCapacity

			p.warnNoNodeCapacityGuest(log, key, nodeCapacity)

			nodeIfaces, err := getNodeNetwork(ctx, cl, region, &item)
			if err != nil {
				log.Error(err, "Failed to get network interfaces for node", "node", item.Node, "region", region)
			}

			if len(nodeIfaces.Ifaces) > 0 {
				networkIfaceInfo[key] = nodeIfaces
			}
		}

		zoneList[region] = nodes
	}

	log.V(1).Info("Syncing finished", "nodes", len(capacityInfo))

	if p.afterBuild != nil {
		p.afterBuild()
	}

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	p.muNetworkInfo.Lock()
	defer p.muNetworkInfo.Unlock()

	for key, info := range capacityInfo {
		for _, id := range p.releasedDuringSync[key] {
			info.Capacity.Release(id)
		}

		if prev, ok := p.capacityInfo[key]; ok && prev.Capacity != nil {
			info.Capacity.KeepPending(prev.Capacity, info.vms.has)
		}

		log.V(1).Info("Node capacity available", "node", key, "resourceStatus", info.Capacity.String())
	}

	// A node missing from this sync keeps its pending reservations, unavailable for placement,
	// so they still count if it comes back.
	for key, prev := range p.capacityInfo {
		if _, ok := capacityInfo[key]; ok || prev.Capacity == nil || !prev.Capacity.HasPending() {
			continue
		}

		prev.omitted = true
		capacityInfo[key] = prev

		log.V(1).Info("Node missing, keeping its pending reservations", "node", key, "resourceStatus", prev.Capacity.String())
	}

	for key, info := range networkIfaceInfo {
		log.V(1).Info("Node network interfaces available", "node", key, "ifaces", len(info.Ifaces))
	}

	if p.hugePageSizes == nil {
		p.hugePageSizes = map[string]uint64{}
	}

	for key, info := range capacityInfo {
		if !info.omitted && info.Capacity != nil {
			p.hugePageSizes[key] = info.Capacity.HugePageSize()
		}
	}

	p.capacityInfo = capacityInfo
	p.networkInfo = networkIfaceInfo
	p.zoneList = zoneList
	p.releasedDuringSync = nil

	return nil
}

func (p *DefaultProvider) UpdateNodeLoad(ctx context.Context) error {
	log := p.log.WithName("UpdateNodeLoad()")

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	for region := range p.zoneList {
		log.V(4).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.Get(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		ns, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
			Type: cluster.ResourceTypeNode,
			Match: func(n *cluster.Resource) (bool, error) {
				return n.Status == "online", nil // nolint:goconst
			},
		})
		if err != nil {
			log.Error(err, "Failed to get nodes for region", "region", region)

			continue
		}

		for _, item := range ns {
			key := fmt.Sprintf("%s/%s", region, item.Node)

			if info, ok := p.capacityInfo[key]; ok {
				info.CPULoad = int(item.CPU * 100)
				p.capacityInfo[key] = info

				log.V(4).Info("Syncing capacity for region", "region", region, "node", item.Node, "cpuLoad", info.CPULoad)
			}
		}
	}

	return nil
}

func (p *DefaultProvider) SyncNodeStorageCapacity(ctx context.Context) error {
	log := p.log.WithName("SyncNodeStorageCapacity()")

	p.muStorageInfo.Lock()
	defer p.muStorageInfo.Unlock()

	capacityInfo := map[string]NodeStorageCapacityInfo{}

	for _, region := range p.pool.List() {
		log.V(1).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.Get(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		storageResources, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
			Type: cluster.ResourceTypeStorage,
			Match: func(r *cluster.Resource) (bool, error) {
				capabilitys := strings.Split(r.StorageContent, ",")

				return r.Status == "available" && slices.ContainsFunc(capabilitys, func(c string) bool {
					return c == "images" || c == "iso" || c == "import"
				}), nil
			},
		})
		if err != nil {
			log.Error(err, "Failed to get storages for region", "region", region)

			continue
		}

		storages := []string{}

		for _, item := range storageResources {
			key := fmt.Sprintf("%s/%s/%s", region, item.Storage, item.Node)

			info := NodeStorageCapacityInfo{
				Name:         item.Storage,
				Region:       region,
				Zones:        []string{item.Node},
				Shared:       item.Shared == 1,
				Type:         item.PluginType,
				Capabilities: strings.Split(item.StorageContent, ","),
			}

			capacityInfo[key] = info

			if !slices.Contains(storages, item.Storage) {
				storages = append(storages, item.Storage)
			}
		}

		for _, storage := range storages {
			zones := []string{}

			for _, info := range capacityInfo {
				if info.Name == storage && info.Region == region {
					zones = append(zones, info.Zones...)
				}
			}

			for _, info := range capacityInfo {
				if info.Name == storage && info.Region == region {
					capacityInfo[fmt.Sprintf("%s/%s", region, storage)] = NodeStorageCapacityInfo{
						Name:         info.Name,
						Region:       info.Region,
						Zones:        slices.Compact(zones),
						Shared:       info.Shared,
						Type:         info.Type,
						Capabilities: info.Capabilities,
					}
				}
			}
		}
	}

	p.storageInfo = capacityInfo

	log.V(4).Info("Syncing finished", "storages", len(capacityInfo), "capacityInfo", p.storageInfo)

	return nil
}

func (p *DefaultProvider) GetStorage(region string, storage string, filter ...func(*NodeStorageCapacityInfo) bool) *NodeStorageCapacityInfo {
	p.muStorageInfo.RLock()
	defer p.muStorageInfo.RUnlock()

	key := fmt.Sprintf("%s/%s", region, storage)
	if info, ok := p.storageInfo[key]; ok {
		storage := info

		if len(filter) == 0 {
			return &storage
		}

		for _, f := range filter {
			if f(&storage) {
				return &storage
			}
		}
	}

	return nil
}

func (p *DefaultProvider) GetNetwork(region string, node string, filter ...func(*NodeNetworkIfaceInfo) bool) *NodeNetworkIfaceInfo {
	p.muNetworkInfo.RLock()
	defer p.muNetworkInfo.RUnlock()

	key := fmt.Sprintf("%s/%s", region, node)
	if info, ok := p.networkInfo[key]; ok {
		network := info

		if len(filter) == 0 {
			return &network
		}

		for _, f := range filter {
			if f(&network) {
				return &network
			}
		}
	}

	return nil
}

func (p *DefaultProvider) Regions() []string {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	regions := make([]string, 0, len(p.zoneList))
	for region := range p.zoneList {
		regions = append(regions, region)
	}

	return regions
}

func (p *DefaultProvider) Zones(region string) []string {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	return p.zoneList[region]
}

// FitInZone reports whether req has a fit in the zone now: nil when it fits, an error wrapping
// zonecapacity.ErrNotOffered when the zone's node policy never offers it, any other error otherwise.
func (p *DefaultProvider) FitInZone(region, zone string, req resources.VMResources) error {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	info, ok := p.capacityInfo[zoneKey(region, zone)]
	if !ok || info.Capacity == nil || info.omitted {
		return fmt.Errorf("zone %s/%s is unavailable", region, zone)
	}

	_, err := info.Capacity.Fit(req)

	return err
}

// Slots is every NUMA slot shape of the zone with its free slots; nil when the zone is unavailable
// or under shared placement, which offers no NUMA slot.
func (p *DefaultProvider) Slots(region, zone string) []zonecapacity.SlotOffer {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	info, ok := p.capacityInfo[zoneKey(region, zone)]
	if !ok || info.Capacity == nil || info.omitted {
		return nil
	}

	return info.Capacity.Slots()
}

// HugePageSize implements Provider.HugePageSize.
func (p *DefaultProvider) HugePageSize(region, zone string) uint64 {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	return p.hugePageSizes[zoneKey(region, zone)]
}

// Root reports whether the controller logs in as root@pam in region.
func (p *DefaultProvider) Root(region string) bool {
	return p.privileges.Root(region)
}

// warnNoNodeCapacityGuest warns when a zone under pinned placement has no node-capacity guest and the
// region is not root: the VMs' affinity is then only a description token, and nothing pins them unless
// the proxmox-scheduler runs on the zone. The controller cannot tell whether it does.
// It warns once each time the zone enters that condition, and again if the zone leaves and re-enters it.
// It runs under muSync.
func (p *DefaultProvider) warnNoNodeCapacityGuest(log logr.Logger, key string, info NodeCapacityInfo) {
	pinnedWithoutGuest := !p.privileges.Root(info.Region) && info.Capacity.Pinned() && !info.nodeCapacityGuest
	if !pinnedWithoutGuest {
		delete(p.warnedNoNodeCapacityGuest, key)

		return
	}

	if p.warnedNoNodeCapacityGuest[key] {
		return
	}

	p.warnedNoNodeCapacityGuest[key] = true

	log.Info("Warning: pinned zone has no node-capacity guest; without root only the proxmox-scheduler pins its VMs",
		"region", info.Region, "zone", info.Name)
}
