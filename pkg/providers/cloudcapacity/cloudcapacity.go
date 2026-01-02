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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	proxmox "github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	// UpdateNodeCapacity updates the node CPU and RAM capacity information for all regions.
	UpdateNodeCapacity(ctx context.Context) error
	// UpdateNodeCapacityInZone updates the node CPU and RAM capacity information for a specific region and zone.
	UpdateNodeCapacityInZone(ctx context.Context, region, zone string) error
	// UpdateNodeLoad updates the node CPU load information for all regions.
	UpdateNodeLoad(ctx context.Context) error

	// UpdateNodeStorageCapacity updates the node storage capacity information for all regions.
	UpdateNodeStorageCapacity(ctx context.Context) error

	// Regions returns a list of regions available in the pool.
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

	muCapacityInfo sync.RWMutex
	capacityInfo   map[string]NodeCapacityInfo

	muStorageInfo sync.RWMutex
	storageInfo   map[string]NodeStorageCapacityInfo

	muNetworkInfo sync.RWMutex
	networkInfo   map[string]NodeNetworkIfaceInfo

	log logr.Logger
}

type NodeCapacityInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// CPULoad is the CPU load of the node in percentage.
	CPULoad int
	// CapacityMaxCPU is the maximum number of CPUs available on the node.
	CapacityMaxCPU uint64
	// CapacityMaxMem is the maximum amount of memory available on the node in bytes.
	CapacityMaxMem uint64
	// Capacity is the total amount of resources available on the node.
	Capacity corev1.ResourceList
	// Allocatable is the total amount of resources available to the VMs.
	Allocatable corev1.ResourceList
}

type NodeStorageCapacityInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// Shared indicates if the storage is shared across nodes.
	Shared bool
	// Type is the type of the storage. (dir/lvm/zfspool)
	Type string
	// Capabilities are the capabilities of the storage.
	Capabilities []string
	// Zone is the zone of the node.
	Zones []string
}

type NodeNetworkIfaceInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// Ifaces is the network interfaces of the node.
	Ifaces map[string]NetworkIfaceInfo
}

type NetworkIfaceInfo struct {
	Address4 string
	Address6 string
	Gateway4 string
	Gateway6 string
	MTU      uint32
}

type StorageOption func(*NodeStorageCapacityInfo)

type NodeCapacity struct {
	// Name is the name of the node.
	Name string
	// Capacity is the total amount of resources available on the node.
	Capacity corev1.ResourceList
	// Overhead is the amount of resource overhead expected to be used by Proxmox host.
	Overhead corev1.ResourceList
	// Allocatable is the total amount of resources available to the VMs.
	Allocatable corev1.ResourceList
}

func NewProvider(ctx context.Context, pool *pxpool.ProxmoxPool) *DefaultProvider {
	log := log.FromContext(ctx).WithName("cloudcapacity")

	return &DefaultProvider{
		pool: pool,
		log:  log,
	}
}

func (p *DefaultProvider) UpdateNodeCapacity(ctx context.Context) error {
	log := p.log.WithName("UpdateNodeCapacity()")

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	p.muNetworkInfo.Lock()
	defer p.muNetworkInfo.Unlock()

	capacityInfo := make(map[string]NodeCapacityInfo)
	networkIfaceInfo := make(map[string]NodeNetworkIfaceInfo)
	zoneList := make(map[string][]string)

	for _, region := range p.pool.GetRegions() {
		log.V(1).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.GetProxmoxCluster(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		ns, err := cl.GetNodeListByFilter(ctx, func(n *proxmox.ClusterResource) (bool, error) {
			return n.Status == "online", nil // nolint:goconst
		})
		if err != nil {
			log.Error(err, "Failed to get nodes for region", "region", region)

			continue
		}

		nodes := make([]string, 0, len(ns))

		// Permission: Sys.Audit
		for _, item := range ns {
			log.V(4).Info("Processing node", "node", item.Node, "region", region, "maxCPU", item.MaxCPU, "maxMem", item.MaxMem)

			key := fmt.Sprintf("%s/%s", region, item.Node)

			allocatable, err := nodeAllocatable(ctx, cl, item.Node, item.MaxCPU, item.MaxMem)
			if err != nil {
				log.Error(err, "Failed to get allocatable resources for node", "node", item.Node, "region", region)

				allocatable = corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("0"),
					corev1.ResourceMemory: resource.MustParse("0"),
				}
			}

			nodes = append(nodes, item.Node)

			capacityInfo[key] = NodeCapacityInfo{
				Name:           item.Node,
				Region:         region,
				CPULoad:        int(item.CPU * 100),
				CapacityMaxCPU: item.MaxCPU,
				CapacityMaxMem: item.MaxMem,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", item.MaxCPU)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", item.MaxMem)),
				},
				Allocatable: allocatable,
			}

			networks, err := (&proxmox.Node{}).New(cl.Client, item.Node).Networks(ctx, "any_bridge")
			if err != nil {
				log.Error(err, "Failed to get network interfaces for node", "node", item.Node, "region", region)
			}

			ifaces := map[string]NetworkIfaceInfo{}

			for _, net := range networks {
				if net.Active == 0 {
					continue
				}

				mtu := 1500
				if net.MTU != "" {
					parsed, err := strconv.Atoi(net.MTU)
					if err != nil {
						log.Error(err, "Failed to parse MTU, using default value", "node", item.Node, "iface", net.Iface, "mtu", net.MTU)

						parsed = 1500
					}

					mtu = parsed
				}

				ifaces[net.Iface] = NetworkIfaceInfo{
					Address4: net.CIDR,
					Address6: net.CIDR6,
					Gateway4: net.Gateway,
					Gateway6: net.Gateway6,
					MTU:      uint32(mtu),
				}
			}

			if len(ifaces) > 0 {
				networkIfaceInfo[key] = NodeNetworkIfaceInfo{
					Name:   item.Node,
					Region: region,
					Ifaces: ifaces,
				}
			}
		}

		zoneList[region] = nodes
	}

	log.V(1).Info("Syncing finished", "nodes", len(capacityInfo))
	log.V(4).Info("Node network info", "networkIfaceInfo", networkIfaceInfo)

	for key, info := range capacityInfo {
		log.V(1).Info("Node capacity available", "node", key, "cpu", info.Allocatable.Cpu().String(), "memory", info.Allocatable.Memory().String())
	}

	p.capacityInfo = capacityInfo
	p.networkInfo = networkIfaceInfo
	p.zoneList = zoneList

	return nil
}

func (p *DefaultProvider) UpdateNodeCapacityInZone(ctx context.Context, region, zone string) error {
	log := p.log.WithName("UpdateNodeCapacityInZone()")

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	log.V(1).Info("Syncing capacity for region", "region", region, "zone", zone)

	cl, err := p.pool.GetProxmoxCluster(region)
	if err != nil {
		log.Error(err, "Failed to get proxmox cluster", "region", region)

		return err
	}

	ns, err := cl.GetNodeListByFilter(ctx, func(n *proxmox.ClusterResource) (bool, error) {
		return n.Status == "online" && n.Name == zone, nil
	})
	if err != nil {
		return fmt.Errorf("cannot find node with name %s in region %s: %w", zone, region, err)
	}

	if len(ns) == 0 {
		return fmt.Errorf("node with name %s not found in region %s", zone, region)
	}

	node := ns[0]

	key := fmt.Sprintf("%s/%s", region, zone)
	nodeCapacity := p.capacityInfo[key]

	allocatable, err := nodeAllocatable(ctx, cl, zone, nodeCapacity.CapacityMaxCPU, nodeCapacity.CapacityMaxMem)
	if err != nil {
		log.Error(err, "Failed to get allocatable resources for node", "node", zone, "region", region)

		allocatable = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("0"),
			corev1.ResourceMemory: resource.MustParse("0"),
		}
	}

	nodeCapacity.CPULoad = int(node.CPU * 100)
	nodeCapacity.Allocatable = allocatable
	p.capacityInfo[key] = nodeCapacity

	return nil
}

func (p *DefaultProvider) UpdateNodeLoad(ctx context.Context) error {
	log := p.log.WithName("UpdateNodeLoad()")

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	for region := range p.zoneList {
		log.V(4).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.GetProxmoxCluster(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		ns, err := cl.GetNodeListByFilter(ctx, func(n *proxmox.ClusterResource) (bool, error) {
			return n.Status == "online", nil // nolint:goconst
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

func (p *DefaultProvider) UpdateNodeStorageCapacity(ctx context.Context) error {
	log := p.log.WithName("UpdateNodeStorageCapacity()")

	p.muStorageInfo.Lock()
	defer p.muStorageInfo.Unlock()

	capacityInfo := map[string]NodeStorageCapacityInfo{}

	for _, region := range p.pool.GetRegions() {
		log.V(1).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.GetProxmoxCluster(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		storageResources, err := cl.GetClusterStoragesByFilter(ctx, func(r *proxmox.ClusterResource) (bool, error) {
			capabilitys := strings.Split(r.Content, ",")

			return r.Status == "available" && slices.ContainsFunc(capabilitys, func(c string) bool {
				return c == "images" || c == "iso" || c == "import"
			}), nil
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
				Capabilities: strings.Split(item.Content, ","),
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
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

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

func (p *DefaultProvider) GetAvailableZonesInRegion(region string, req corev1.ResourceList) []string {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	zones := []string{}

	for _, info := range p.capacityInfo {
		if info.Region != region {
			continue
		}

		if info.Allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && info.Allocatable.Memory().Cmp(*req.Memory()) >= 0 {
			zones = append(zones, info.Name)
		}
	}

	return zones
}

func (p *DefaultProvider) FitInZone(region, zone string, req corev1.ResourceList) bool {
	p.muCapacityInfo.RLock()
	defer p.muCapacityInfo.RUnlock()

	for _, info := range p.capacityInfo {
		if info.Region == region && info.Name == zone {
			return info.Allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && info.Allocatable.Memory().Cmp(*req.Memory()) >= 0
		}
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

func nodeAllocatable(ctx context.Context, c *goproxmox.APIClient, node string, maxCPU, maxMem uint64) (corev1.ResourceList, error) {
	vms, err := c.GetVMsByFilter(ctx, func(vm *proxmox.ClusterResource) (bool, error) {
		return vm.Node == node && vm.Status == "running", nil
	})
	if err != nil && !errors.Is(err, goproxmox.ErrVirtualMachineNotFound) {
		return nil, fmt.Errorf("cannot list vms for node %s: %w", node, err)
	}

	var (
		cpuUsage int64
		memUsage int64
	)

	for _, vm := range vms {
		cpuUsage += int64(vm.MaxCPU)
		memUsage += int64(vm.MaxMem)
	}

	cpu := max(int64(maxCPU)-cpuUsage, 0)
	mem := max(int64(maxMem)-memUsage, 0)

	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", cpu)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", mem)),
	}, nil
}
