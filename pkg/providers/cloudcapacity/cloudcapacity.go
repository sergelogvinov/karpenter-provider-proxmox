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
	"sync"

	"github.com/go-logr/logr"
	proxmox "github.com/luthermonson/go-proxmox"

	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	UpdateNodeCapacity(ctx context.Context) error
	UpdateNodeLoad(ctx context.Context) error

	Sync(context.Context) error

	Regions() []string
	Zones(region string) []string

	GetAvailableZonesInRegion(region string, req corev1.ResourceList) []string
	FitInZone(zone string, req corev1.ResourceList) bool
}

type DefaultProvider struct {
	pool          *pxpool.ProxmoxPool
	capacityZones map[string]NodeCapacity

	muCapacityInfo sync.RWMutex
	capacityInfo   map[string]NodeCapacityInfo
	zoneList       map[string][]string

	log logr.Logger
}

type NodeCapacityInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// CPUInfo is the CPU information of the node.
	CPUInfo proxmox.CPUInfo
	// CPULoad is the CPU load of the node in percentage.
	CPULoad int
	// Capacity is the total amount of resources available on the node.
	Capacity corev1.ResourceList
	// Allocatable is the total amount of resources available to the VMs.
	Allocatable corev1.ResourceList
}

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

	capacityInfo := make(map[string]NodeCapacityInfo)
	zoneList := make(map[string][]string)

	for _, region := range p.pool.GetRegions() {
		log.V(1).Info("Syncing capacity for region", "region", region)

		cl, err := p.pool.GetProxmoxCluster(region)
		if err != nil {
			log.Error(err, "Failed to get proxmox cluster", "region", region)

			continue
		}

		ns, err := cl.Nodes(ctx)
		if err != nil {
			log.Error(err, "Failed to get nodes for region", "region", region)

			continue
		}

		nodes := make([]string, 0, len(ns))

		for _, item := range ns {
			if item.Status != "online" {
				continue
			}

			node, err := cl.Node(ctx, item.Node)
			if err != nil {
				return fmt.Errorf("cannot find node with name %s in region %s: %w", item.Node, region, err)
			}

			key := fmt.Sprintf("%s/%s", region, item.Node)

			allocatable, err := nodeAllocatable(ctx, node, uint64(item.MaxCPU), item.MaxMem)
			if err != nil {
				log.Error(err, "Failed to get allocatable resources for node", "node", item.Node, "region", region)

				allocatable = corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("0"),
					corev1.ResourceMemory: resource.MustParse("0"),
				}
			}

			nodes = append(nodes, item.Node)
			capacityInfo[key] = NodeCapacityInfo{
				Name:    item.Node,
				Region:  region,
				CPUInfo: node.CPUInfo,
				CPULoad: int(node.CPU * 100),
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", item.MaxCPU)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", item.MaxMem)),
				},
				Allocatable: allocatable,
			}
		}

		zoneList[region] = nodes
	}

	log.V(1).Info("Syncing finished", "nodes", len(capacityInfo))

	p.capacityInfo = capacityInfo
	p.zoneList = zoneList

	return nil
}

func (p *DefaultProvider) UpdateNodeLoad(ctx context.Context) error {
	log := p.log.WithName("UpdateNodeLoad()")

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	for _, region := range p.pool.GetRegions() {
		log.V(1).Info("Syncing capacity for region", "region", region)

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

		nodeResources, err := cluster.Resources(ctx, "node")
		if err != nil {
			log.Error(err, "Failed to list node resources", "region", region)

			continue
		}

		for _, item := range nodeResources {
			if item.Status != "online" {
				continue
			}

			key := fmt.Sprintf("%s/%s", region, item.Node)

			info := p.capacityInfo[key]
			info.CPULoad = int(item.CPU * 100)
			p.capacityInfo[key] = info

			log.V(1).Info("Syncing capacity for region", "region", region, "node", item.Node, "cpuLoad", info.CPULoad)
		}
	}

	return nil
}

func (p *DefaultProvider) Sync(ctx context.Context) error {
	capacityZones := make(map[string]NodeCapacity)

	for _, info := range p.capacityInfo {
		capacityZones[info.Name] = NodeCapacity{
			Name:        info.Name,
			Capacity:    info.Capacity.DeepCopy(),
			Allocatable: info.Allocatable.DeepCopy(),
		}
	}

	p.capacityZones = capacityZones

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

func (p *DefaultProvider) FitInZone(zone string, req corev1.ResourceList) bool {
	capacity, ok := p.capacityZones[zone]
	if !ok {
		return false
	}

	return capacity.Allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && capacity.Allocatable.Memory().Cmp(*req.Memory()) >= 0
}

func nodeAllocatable(ctx context.Context, node *proxmox.Node, maxCPU, maxMem uint64) (corev1.ResourceList, error) {
	vms, err := node.VirtualMachines(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot list vms for node %s: %w", node.Name, err)
	}

	var (
		cpuUsage int64
		memUsage int64
	)

	for _, vm := range vms {
		if vm.Status != "running" {
			continue
		}

		cpuUsage += int64(vm.CPUs)
		memUsage += int64(vm.MaxMem)
	}

	cpu := int64(maxCPU) - cpuUsage
	if cpu < 0 {
		cpu = 0
	}

	mem := int64(maxMem) - memUsage
	if mem < 0 {
		mem = 0
	}

	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", cpu)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", mem)),
	}, nil
}
