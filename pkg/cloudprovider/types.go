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

package proxmox

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

// ConstructInstanceTypes create many instance types based on the embedded instance type data
func ConstructInstanceTypes(ctx context.Context, cloudcapacityProvider *cloudcapacity.Provider) ([]*cloudprovider.InstanceType, error) {
	var instanceTypes []*cloudprovider.InstanceType

	for _, cpu := range []int{1, 2, 4, 8, 16, 32} {
		for _, memFactor := range []int{2, 3, 4} {
			// Construct instance type details, then construct offerings.
			name := makeGenericInstanceTypeName(cpu, memFactor)

			mem := cpu * memFactor
			opts := cloudprovider.InstanceType{
				Name: name,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:              resource.MustParse(fmt.Sprintf("%d", cpu)),
					corev1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dGi", mem)),
					corev1.ResourcePods:             resource.MustParse("110"),
					corev1.ResourceEphemeralStorage: resource.MustParse("30Gi"),
				},
				Overhead: &cloudprovider.InstanceTypeOverhead{
					KubeReserved:   KubeReservedResources(int64(cpu), float64(mem)),
					SystemReserved: SystemReservedResources(),
				},
			}

			createOfferings(cloudcapacityProvider, &opts)

			instanceTypes = append(instanceTypes, &opts)
		}
	}

	return instanceTypes, nil
}

func instanceTypeByName(instanceTypes []*cloudprovider.InstanceType, name string) (*cloudprovider.InstanceType, error) {
	for _, instanceType := range instanceTypes {
		if instanceType.Name == name {
			return instanceType, nil
		}
	}

	return nil, fmt.Errorf("instance type not found")
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

func SystemReservedResources() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
}

func KubeReservedResources(_ int64, _ float64) corev1.ResourceList {
	resources := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("200m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}

	return resources
}

func createOfferings(cloudcapacityProvider *cloudcapacity.Provider, opts *cloudprovider.InstanceType) {
	region := "region-1"
	zones := cloudcapacityProvider.Zones()
	price := priceFromResources(opts.Capacity)

	opts.Offerings = []cloudprovider.Offering{}

	for _, zone := range zones {
		available := cloudcapacityProvider.Fit(zone, opts.Capacity)

		opts.Offerings = append(opts.Offerings, cloudprovider.Offering{
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
