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
	"sort"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	// AllocateCapacityInZone allocates the specified capacity in the given region and zone.
	AllocateCapacityInZone(ctx context.Context, region, zone string, id int, op *resources.VMResources) error
	// ReleaseCapacityInZone releases the specified capacity in the given region and zone.
	ReleaseCapacityInZone(ctx context.Context, region, zone string, id int, op *resources.VMResources) error

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

	GetAvailableZonesInRegion(region string, req corev1.ResourceList) []string
	SortZonesByCPULoad(region string, zones []string) []string
	FitInZone(region, zone string, req corev1.ResourceList) bool

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
	// releasedDuringSync collects, per zone, the VMs released while a sync builds new zones,
	// so the swap does not bring back VMs the build saw before their release.
	releasedDuringSync map[string][]int

	muStorageInfo sync.RWMutex
	storageInfo   map[string]NodeStorageCapacityInfo

	muNetworkInfo sync.RWMutex
	networkInfo   map[string]NodeNetworkIfaceInfo

	log logr.Logger

	// afterBuild, if set, runs between building the new zones and swapping them in.
	afterBuild func()
}

func NewProvider(ctx context.Context, pool *pxpool.ProxmoxPool) *DefaultProvider {
	log := log.FromContext(ctx).WithName("cloudcapacity")

	return &DefaultProvider{
		pool: pool,
		log:  log,
	}
}

//nolint:dupl
func (p *DefaultProvider) AllocateCapacityInZone(ctx context.Context, region, zone string, id int, op *resources.VMResources) error {
	if op == nil {
		return fmt.Errorf("cannot allocate capacity: VMResources must be provided")
	}

	log := log.FromContext(ctx).WithName("AllocateCapacityInZone()").WithValues("region", region, "zone", zone)
	log.V(1).Info("Allocating capacity", "cpu", op.CPUs, "memory", op.Memory, "storage", op.DiskGBytes)

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	key := fmt.Sprintf("%s/%s", region, zone)
	if info, ok := p.capacityInfo[key]; ok && info.Capacity != nil {
		if info.omitted {
			return fmt.Errorf("zone %s/%s is unavailable", region, zone)
		}

		err := info.Capacity.Reserve(op)
		if err != nil {
			return fmt.Errorf("failed to allocate CPU capacity in zone %s/%s: %w", region, zone, err)
		}

		log.V(1).Info("Capacity allocated successfully", "resourceStatus", info.Capacity.String())

		return nil
	}

	return fmt.Errorf("no resource manager found for zone %s/%s", region, zone)
}

//nolint:dupl
func (p *DefaultProvider) ReleaseCapacityInZone(ctx context.Context, region, zone string, id int, op *resources.VMResources) error {
	if op == nil {
		return fmt.Errorf("cannot release capacity: VMResources must be provided")
	}

	log := log.FromContext(ctx).WithName("ReleaseCapacityInZone()").WithValues("region", region, "zone", zone)
	log.V(1).Info("Releasing capacity", "cpu", op.CPUs, "memory", op.Memory/1024/1024, "storage", op.DiskGBytes)

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

	return fmt.Errorf("no resource manager found for zone %s/%s", region, zone)
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

// FIXME: optimize this functions

func (p *DefaultProvider) GetAvailableZonesInRegion(region string, req corev1.ResourceList) []string {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	zones := []string{}

	for _, info := range p.capacityInfo {
		if info.Region != region || info.Capacity == nil || info.omitted {
			continue
		}

		free := info.Capacity.Available()

		allocatable := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", free.CPUs)),
			corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", free.Memory)),
		}

		if allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && allocatable.Memory().Cmp(*req.Memory()) >= 0 {
			zones = append(zones, info.Name)
		}
	}

	return zones
}

func (p *DefaultProvider) FitInZone(region, zone string, req corev1.ResourceList) bool {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	key := fmt.Sprintf("%s/%s", region, zone)
	if info, ok := p.capacityInfo[key]; ok {
		if info.Capacity == nil || info.omitted {
			return false
		}

		free := info.Capacity.Available()

		allocatable := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", free.CPUs)),
			corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", free.Memory)),
		}

		return allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && allocatable.Memory().Cmp(*req.Memory()) >= 0
	}

	return false
}

func (p *DefaultProvider) SortZonesByCPULoad(region string, zones []string) []string {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	if len(zones) <= 1 {
		return zones
	}

	sortedZones := make([]string, 0, len(zones))
	for _, zone := range zones {
		if slices.Contains(p.zoneList[region], zone) {
			sortedZones = append(sortedZones, zone)
		}
	}

	sort.Slice(sortedZones, func(i, j int) bool {
		return p.capacityInfo[fmt.Sprintf("%s/%s", region, sortedZones[i])].CPULoad < p.capacityInfo[fmt.Sprintf("%s/%s", region, sortedZones[j])].CPULoad
	})

	return sortedZones
}
