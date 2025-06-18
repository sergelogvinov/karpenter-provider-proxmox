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

	cluster "github.com/sergelogvinov/proxmox-cloud-controller-manager/pkg/cluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider struct {
	cluster       *cluster.Cluster
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
	cfg, err := cluster.ReadCloudConfigFromFile("cloud.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	cluster, err := cluster.NewCluster(&cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxmox cluster client: %v", err)
	}

	return &Provider{cluster: cluster}, nil
}

func (p *Provider) Sync(ctx context.Context) error {
	region := "region-1"

	cl, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster: %v", err)
	}

	data, err := cl.GetNodeList()
	if err != nil {
		return fmt.Errorf("failed to get node list: %v", err)
	}

	if data["data"] == nil {
		return fmt.Errorf("failed to parce node list: %v", err)
	}

	capacityZones := make(map[string]NodeCapacity)

	for _, item := range data["data"].([]interface{}) {
		node, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if st, ok := node["status"].(string); !ok || st != "online" {
			continue
		}

		name := node["node"].(string)

		vms, err := cl.GetResourceList("vm")
		if err != nil {
			return fmt.Errorf("error get resources %v", err)
		}

		cpuUsage := 0.0
		memUsage := 0.0

		for vmii := range vms {
			vm, ok := vms[vmii].(map[string]interface{})
			if !ok {
				return fmt.Errorf("failed to cast response to map, vm: %v", vm)
			}

			if vm["type"].(string) != "qemu" { //nolint:errcheck
				continue
			}

			if nodeName, ok := vm["node"].(string); !ok || nodeName != name {
				continue
			}

			if vmStatus, ok := vm["status"].(string); !ok || vmStatus != "running" {
				continue
			}

			cpu, ok := vm["maxcpu"].(float64)
			if !ok {
				continue
			}

			cpuUsage += cpu

			mem, ok := vm["maxmem"].(float64)
			if !ok {
				continue
			}

			memUsage += mem
		}

		cpu, ok := node["maxcpu"].(float64)
		if !ok {
			continue
		}

		mem, ok := node["maxmem"].(float64)
		if !ok {
			continue
		}

		capacityZones[name] = NodeCapacity{
			Name: name,
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%f", cpu)),
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%f", mem)),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%f", cpu-cpuUsage)),
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%f", mem-memUsage)),
			},
		}
	}

	log.FromContext(ctx).V(1).Info("Capacity of zones", "capacityZones", capacityZones)

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
