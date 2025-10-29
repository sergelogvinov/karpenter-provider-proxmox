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

	"github.com/go-logr/logr"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error)
	ListWithFilter(ctx context.Context, filter func(*cloudprovider.InstanceType) bool) []*cloudprovider.InstanceType
	Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error)

	UpdateInstanceTypes(context.Context) error
	UpdateInstanceTypeOfferings(context.Context) error
}

type DefaultProvider struct {
	cloudCapacityProvider cloudcapacity.Provider

	muInstanceTypes   sync.RWMutex
	instanceTypesInfo []*InstanceTypeStatic

	log logr.Logger
}

func NewDefaultProvider(ctx context.Context, cloudCapacityProvider cloudcapacity.Provider) *DefaultProvider {
	log := log.FromContext(ctx).WithName("instancetype")

	return &DefaultProvider{
		cloudCapacityProvider: cloudCapacityProvider,
		instanceTypesInfo:     loadDefaultInstanceTypes(),
		log:                   log,
	}
}

func (p *DefaultProvider) List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	return p.ListWithFilter(ctx, func(_ *cloudprovider.InstanceType) bool {
		return true
	}), nil
}

func (p *DefaultProvider) ListWithFilter(ctx context.Context, filter func(*cloudprovider.InstanceType) bool) []*cloudprovider.InstanceType {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	filtered := []*cloudprovider.InstanceType{}

	for _, item := range p.instanceTypesInfo {
		instanceType := &cloudprovider.InstanceType{
			Name:      item.Name,
			Offerings: item.Offerings.DeepCopy(),
			Capacity:  item.Capacity.DeepCopy(),
			Overhead: func() *cloudprovider.InstanceTypeOverhead {
				if item.Overhead == nil {
					return &cloudprovider.InstanceTypeOverhead{}
				}

				return &cloudprovider.InstanceTypeOverhead{
					KubeReserved:      item.Overhead.KubeReserved.DeepCopy(),
					SystemReserved:    item.Overhead.SystemReserved.DeepCopy(),
					EvictionThreshold: item.Overhead.EvictionThreshold.DeepCopy(),
				}
			}(),
		}

		if filter(instanceType) {
			instanceType.Requirements = computeRequirements(item.Name, instanceType.Offerings, p.cloudCapacityProvider.Regions())

			filtered = append(filtered, instanceType)
		}
	}

	return filtered
}

func (p *DefaultProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	filtered := p.ListWithFilter(ctx, func(item *cloudprovider.InstanceType) bool {
		return item.Name == name
	})

	if len(filtered) == 0 {
		return nil, fmt.Errorf("instance type not found: %s", name)
	}

	return filtered[0], nil
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
	log := p.log.WithName("UpdateInstanceTypeOfferings()")

	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	offers := 0

	for _, item := range p.instanceTypesInfo {
		p.createOfferings(item, p.cloudCapacityProvider.Regions(), item.CapacityType)
		offers += len(item.Offerings.Available())
	}

	log.V(1).Info("Instance type offerings updated", "instanceTypes", len(p.instanceTypesInfo), "offers", offers)

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

func (p *DefaultProvider) createOfferings(opts *InstanceTypeStatic, regions []string, capacityType string) {
	opts.Offerings = []*cloudprovider.Offering{}
	price := priceFromResources(opts.Capacity)

	for _, region := range regions {
		for _, zone := range p.cloudCapacityProvider.Zones(region) {
			available := p.cloudCapacityProvider.FitInZone(region, zone, opts.Capacity)

			// We use capacityType array to allow multiple capacity types per instance type in the future
			for _, ct := range lo.Uniq([]string{capacityType}) {
				opts.Offerings = append(opts.Offerings, &cloudprovider.Offering{
					Price:     lo.Ternary(ct == karpv1.CapacityTypeSpot, price*.5, price),
					Available: available,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, opts.Name),
						scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
						scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, ct),
						scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, strings.Split(opts.Name, ".")[0]),
					),
				})
			}
		}
	}
}

func computeRequirements(instanceTypeName string, offerings cloudprovider.Offerings, regions []string) scheduling.Requirements {
	requirements := scheduling.NewRequirements(
		// Well Known Upstream
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, instanceTypeName),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, string(karpv1.ArchitectureAmd64)),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
		scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, regions...),
		// scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, lo.Map(offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
		// 	return o.Requirements.Get(corev1.LabelTopologyZone).Any()
		// })...),

		// Well Known to Karpenter
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, lo.Map(offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
			return o.Requirements.Get(karpv1.CapacityTypeLabelKey).Any()
		})...),

		// Well Known to Proxmox
		scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, strings.Split(instanceTypeName, ".")[0]),
		scheduling.NewRequirement(v1alpha1.LabelInstanceCPUType, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceImageID, corev1.NodeSelectorOpDoesNotExist),
	)

	return requirements
}

func loadInstanceTypesFromFile(name string) ([]*InstanceTypeStatic, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read instance types file %s: %w", name, err)
	}

	instanceTypeStatic := []*InstanceTypeStatic{}

	if err := json.Unmarshal(data, &instanceTypeStatic); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instance types %s: %w", name, err)
	}

	for _, i := range instanceTypeStatic {
		if i.CapacityType == "" {
			i.CapacityType = karpv1.CapacityTypeOnDemand
		}
	}

	return instanceTypeStatic, nil
}

func loadDefaultInstanceTypes() []*InstanceTypeStatic {
	instanceTypes := []*InstanceTypeStatic{}

	options := InstanceTypeOptions{
		CPUs:              []int{1, 2, 4, 8, 16},
		MemFactors:        []int{2, 3, 4, 8},
		Storage:           30,
		KubeletOverhead:   true,
		SystemOverhead:    true,
		EvictionThreshold: true,
	}

	for _, i := range options.Generate() {
		instance := InstanceTypeStatic{
			Name:         i.Name,
			Capacity:     i.Capacity,
			CapacityType: i.CapacityType,
			Overhead:     &cloudprovider.InstanceTypeOverhead{},
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
