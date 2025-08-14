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
	"fmt"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
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
	instanceTypes     []*cloudprovider.InstanceType

	log logr.Logger
}

func NewDefaultProvider(ctx context.Context, cloudCapacityProvider cloudcapacity.Provider) *DefaultProvider {
	log := log.FromContext(ctx).WithName("instancetype")

	return &DefaultProvider{
		cloudCapacityProvider: cloudCapacityProvider,
		log:                   log,
	}
}

func (p *DefaultProvider) List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	log := p.log.WithName("List()")

	if len(p.instanceTypes) == 0 {
		if err := p.UpdateInstanceTypes(ctx); err != nil {
			log.Error(err, "Failed to update instance types")

			return nil, fmt.Errorf("failed to update instance types: %w", err)
		}
	}

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
			Requirements: computeRequirements(item.Name, item.Offerings, regions),
			Capacity:     item.Capacity.DeepCopy(),
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved:   item.Overhead.KubeReserved.DeepCopy(),
				SystemReserved: item.Overhead.SystemReserved.DeepCopy(),
			},
		}

		createOfferings(p.cloudCapacityProvider, instanceType, regions)

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
				Requirements: computeRequirements(item.Name, item.Offerings, p.cloudCapacityProvider.Regions()),
				Capacity:     item.Capacity.DeepCopy(),
				Overhead: &cloudprovider.InstanceTypeOverhead{
					KubeReserved:   item.Overhead.KubeReserved.DeepCopy(),
					SystemReserved: item.Overhead.SystemReserved.DeepCopy(),
				},
			}

			createOfferings(p.cloudCapacityProvider, instanceType, p.cloudCapacityProvider.Regions())

			return instanceType, nil
		}
	}

	return nil, fmt.Errorf("instance type not found: %s", name)
}

func (p *DefaultProvider) UpdateInstanceTypes(ctx context.Context) error {
	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	instanceTypes := []*cloudprovider.InstanceType{}

	for _, cpu := range []int{1, 2, 4, 8, 16, 32} {
		for _, memFactor := range []int{2, 3, 4} {
			name := makeGenericInstanceTypeName(cpu, memFactor)

			mem := cpu * memFactor
			opts := cloudprovider.InstanceType{
				Name: name,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:              resource.MustParse(fmt.Sprintf("%d", cpu)),
					corev1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dGi", mem)),
					corev1.ResourcePods:             resource.MustParse("110"),
					corev1.ResourceEphemeralStorage: resource.MustParse("30G"),
				},
				Overhead: &cloudprovider.InstanceTypeOverhead{
					KubeReserved:   kubeReservedResources(int64(cpu), float64(mem)),
					SystemReserved: systemReservedResources(),
				},
			}

			instanceTypes = append(instanceTypes, &opts)
		}
	}

	p.instanceTypesInfo = instanceTypes

	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeOfferings(ctx context.Context) error {
	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	return nil
}

func systemReservedResources() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
}

func kubeReservedResources(_ int64, _ float64) corev1.ResourceList {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("200m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}

	return resources
}

func makeGenericInstanceTypeName(cpu, memFactor int) string {
	var family string

	switch memFactor {
	case 2:
		family = "c" // cpu
	case 3:
		family = "t"
	case 4:
		family = "s" // standard
	case 8:
		family = "m" // memory
	case 16:
		family = "x" // in-memory applications
	default:
		family = "e"
	}

	return fmt.Sprintf("%s1.%dVCPU-%dGB", family, cpu, cpu*memFactor)
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

func createOfferings(cloudcapacityProvider cloudcapacity.Provider, opts *cloudprovider.InstanceType, regions []string) {
	opts.Offerings = []*cloudprovider.Offering{}
	price := priceFromResources(opts.Capacity)

	for _, region := range regions {
		for _, zone := range cloudcapacityProvider.Zones(region) {
			available := cloudcapacityProvider.FitInZone(region, zone, opts.Capacity)

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

func computeRequirements(instanceTypeName string, _ []*cloudprovider.Offering, regions []string) scheduling.Requirements {
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
		scheduling.NewRequirement(v1alpha1.LabelInstanceCPUManufacturer, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceCPU, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceMemory, corev1.NodeSelectorOpDoesNotExist),
	)

	return requirements
}
