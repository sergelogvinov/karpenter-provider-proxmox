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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	providerconfig "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider struct {
	cluster       *pxpool.ProxmoxPool
	capacityZones map[string]NodeCapacity
}

type NodeCapacity struct {
	Name string
	// Capacity is the total amount of resources available on the node.
	Capacity corev1.ResourceList
	// Overhead is the amount of resource overhead expected to be used by Proxmox host.
	Overhead corev1.ResourceList
	// Allocatable is the total amount of resources available to the VMs.
	Allocatable corev1.ResourceList
}

func NewProvider(ctx context.Context) (*Provider, error) {
	cfg, err := providerconfig.ReadCloudConfigFromFile(options.FromContext(ctx).CloudConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	cluster, err := pxpool.NewProxmoxPool(ctx, cfg.Clusters)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxmox cluster client: %v", err)
	}

	return &Provider{cluster: cluster}, nil
}

func (p *Provider) Sync(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("cloudcapacity")

	capacityZones := make(map[string]NodeCapacity)

	for _, region := range p.cluster.GetRegions() {
		cl, err := p.cluster.GetProxmoxCluster(region)
		if err != nil {
			return fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
		}

		ns, err := cl.Nodes(ctx)
		if err != nil {
			return fmt.Errorf("failed to get nodes for region %s: %v", region, err)
		}

		for _, item := range ns {
			log.V(3).Info("Capacity of zones", "region", region, "node", item.Node, "nodeStatus", item.Status)

			if item.Status != "online" {
				continue
			}

			node, err := cl.Node(ctx, item.Node)
			if err != nil {
				return fmt.Errorf("cannot find node with name %s in region %s: %w", item.Node, region, err)
			}

			vms, err := node.VirtualMachines(ctx)
			if err != nil {
				return fmt.Errorf("cannot list vms for node %s in region %s: %w", item.Node, region, err)
			}

			var (
				cpuUsage int
				memUsage uint64
			)

			for _, vm := range vms {
				if vm.Status != "running" {
					continue
				}

				log.V(2).Info("Capacity of zones", "region", region, "node", item.Node, "vm", vm.VMID, "status", vm.Status, "cpu", vm.CPUs, "mem", vm.MaxMem)

				cpuUsage += vm.CPUs
				memUsage += vm.MaxMem
			}

			cpu := item.MaxCPU - cpuUsage
			mem := item.MaxMem - memUsage

			capacityZones[item.Node] = NodeCapacity{
				Name: item.Node,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", item.MaxCPU)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", item.MaxMem)),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", cpu)),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", mem)),
				},
			}
		}
	}

	log.Info("Capacity of zones", "capacityZones", capacityZones)

	p.capacityZones = capacityZones

	return nil
}

func (p *Provider) Zones() []string {
	zones := make([]string, 0, len(p.capacityZones))
	for zone := range p.capacityZones {
		zones = append(zones, zone)
	}

	return zones
}

func (p *Provider) Fit(zone string, req corev1.ResourceList) bool {
	capacity, ok := p.capacityZones[zone]
	if !ok {
		return false
	}

	return capacity.Allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && capacity.Allocatable.Memory().Cmp(*req.Memory()) >= 0
}

func (p *Provider) GetAvailableZones(req corev1.ResourceList) []string {
	zones := []string{}

	for zone := range p.capacityZones {
		capacity, ok := p.capacityZones[zone]
		if !ok {
			continue
		}

		if capacity.Allocatable.Cpu().Cmp(*req.Cpu()) >= 0 && capacity.Allocatable.Memory().Cmp(*req.Memory()) >= 0 {
			zones = append(zones, zone)
		}
	}

	return zones
}
