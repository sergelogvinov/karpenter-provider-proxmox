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

package instancetype

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error)
	Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error)

	UpdateInstanceTypes(context.Context) error
	UpdateInstanceTypeOfferings(context.Context) error
}

type DefaultProvider struct {
	cloudCapacityProvider cloudcapacity.Provider

	muInstanceTypes   sync.RWMutex
	instanceTypesInfo []*cloudprovider.InstanceType
}

func NewDefaultProvider(ctx context.Context, cloudCapacityProvider cloudcapacity.Provider) *DefaultProvider {
	return &DefaultProvider{
		cloudCapacityProvider: cloudCapacityProvider,
		instanceTypesInfo:     loadDefaultInstanceTypes(),
	}
}

func (p *DefaultProvider) List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	regions := p.cloudCapacityProvider.Regions()
	if nodeClass.Spec.Region != "" {
		regions = []string{nodeClass.Spec.Region}
	}

	instanceTypes := []*cloudprovider.InstanceType{}
	for _, item := range p.instanceTypesInfo {
		instanceType := &cloudprovider.InstanceType{
			Name:         item.Name,
			Requirements: p.computeRequirements(item.Name, item.Offerings, regions),
			Capacity:     item.Capacity.DeepCopy(),
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved:      item.Overhead.KubeReserved.DeepCopy(),
				SystemReserved:    item.Overhead.SystemReserved.DeepCopy(),
				EvictionThreshold: item.Overhead.EvictionThreshold.DeepCopy(),
			},
		}

		p.createOfferings(instanceType, regions)

		instanceTypes = append(instanceTypes, instanceType)
	}

	return instanceTypes, nil
}

func (p *DefaultProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	for _, item := range p.instanceTypesInfo {
		if item.Name == name {
			instanceType := &cloudprovider.InstanceType{
				Name:         item.Name,
				Requirements: p.computeRequirements(item.Name, item.Offerings, p.cloudCapacityProvider.Regions()),
				Capacity:     item.Capacity.DeepCopy(),
				Overhead:     &cloudprovider.InstanceTypeOverhead{},
			}

			if item.Overhead != nil {
				instanceType.Overhead.KubeReserved = item.Overhead.KubeReserved.DeepCopy()
				instanceType.Overhead.SystemReserved = item.Overhead.SystemReserved.DeepCopy()
				instanceType.Overhead.EvictionThreshold = item.Overhead.EvictionThreshold.DeepCopy()
			}

			p.createOfferings(instanceType, p.cloudCapacityProvider.Regions())

			return instanceType, nil
		}
	}

	return nil, fmt.Errorf("instance type not found: %s", name)
}

func (p *DefaultProvider) UpdateInstanceTypes(ctx context.Context) error {
	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	if name := options.FromContext(ctx).InstanceTypesFilePath; name != "" {
		instanceTypes, err := loadInstanceTypesFromFile(name)
		if err != nil {
			return err
		}

		p.instanceTypesInfo = instanceTypes
	}

	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeOfferings(ctx context.Context) error {
	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	return nil
}

func priceFromResources(resources corev1.ResourceList) float64 {
	// Let's assume the price is electricity cost
	price := 0.0

	for k, v := range resources {
		switch k { //nolint:exhaustive
		case corev1.ResourceCPU:
			price += 0.025 * v.AsApproximateFloat64()
		case corev1.ResourceMemory:
			price += 0.001 * v.AsApproximateFloat64() / (1e9)
		}
	}

	return price
}

func (p *DefaultProvider) createOfferings(opts *cloudprovider.InstanceType, regions []string) {
	opts.Offerings = []*cloudprovider.Offering{}
	price := priceFromResources(opts.Capacity)

	for _, region := range regions {
		for _, zone := range p.cloudCapacityProvider.Zones(region) {
			available := p.cloudCapacityProvider.FitInZone(region, zone, opts.Capacity)

			opts.Offerings = append(opts.Offerings, &cloudprovider.Offering{
				Price:     price,
				Available: available,
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, opts.Name),
					scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
					scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
					scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, strings.Split(opts.Name, ".")[0]),
				),
			})
		}
	}
}

func (p *DefaultProvider) computeRequirements(instanceTypeName string, _ []*cloudprovider.Offering, regions []string) scheduling.Requirements {
	requirements := scheduling.NewRequirements(
		// Well Known Upstream
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, instanceTypeName),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, string(karpv1.ArchitectureAmd64)),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
		scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, regions...),
		// scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpDoesNotExist),

		// Well Known to Karpenter
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),

		// Well Known to Proxmox
		scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, strings.Split(instanceTypeName, ".")[0]),
		scheduling.NewRequirement(v1alpha1.LabelInstanceCPUType, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceImageID, corev1.NodeSelectorOpDoesNotExist),
	)

	return requirements
}

func loadInstanceTypesFromFile(name string) ([]*cloudprovider.InstanceType, error) {
	instanceTypes := []*cloudprovider.InstanceType{}

	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read instance types file %s: %w", name, err)
	}

	instanceTypeStatic := []InstanceTypeStatic{}

	if err := json.Unmarshal(data, &instanceTypeStatic); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instance types %s: %w", name, err)
	}

	for _, i := range instanceTypeStatic {
		instance := cloudprovider.InstanceType{
			Name:     i.Name,
			Capacity: i.Capacity,
			Overhead: &cloudprovider.InstanceTypeOverhead{},
		}

		if i.Overhead != nil {
			instance.Overhead.KubeReserved = i.Overhead.KubeReserved.DeepCopy()
			instance.Overhead.SystemReserved = i.Overhead.SystemReserved.DeepCopy()
			instance.Overhead.EvictionThreshold = i.Overhead.EvictionThreshold.DeepCopy()
		}

		instanceTypes = append(instanceTypes, &instance)
	}

	return instanceTypes, nil
}

func loadDefaultInstanceTypes() []*cloudprovider.InstanceType {
	instanceTypes := []*cloudprovider.InstanceType{}

	options := InstanceTypeOptions{
		CPUs:              []int{1, 2, 4, 8, 16},
		MemFactors:        []int{2, 3, 4, 8},
		Storage:           30,
		KubeletOverhead:   true,
		SystemOverhead:    true,
		EvictionThreshold: true,
	}

	for _, i := range options.Generate() {
		instance := cloudprovider.InstanceType{
			Name:     i.Name,
			Capacity: i.Capacity,
			Overhead: &cloudprovider.InstanceTypeOverhead{},
		}

		if i.Overhead != nil {
			instance.Overhead.KubeReserved = i.Overhead.KubeReserved.DeepCopy()
			instance.Overhead.SystemReserved = i.Overhead.SystemReserved.DeepCopy()
			instance.Overhead.EvictionThreshold = i.Overhead.EvictionThreshold.DeepCopy()
		}

		instanceTypes = append(instanceTypes, &instance)
	}

	return instanceTypes
}
