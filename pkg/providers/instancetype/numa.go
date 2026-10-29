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
	"cmp"
	"maps"
	"slices"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// numaTypes generates the NUMA types of the templates from the slot shapes of every zone of regions
// (docs/scheduler-numa.md §6). Each distinct (template, slot shape) is one type, named
// <family>.<cpu>VCPU-<memory>GB, offered in the zones that have that shape, and available where
// the zone has a free slot of it. The caller holds the lock.
func (p *DefaultProvider) numaTypes(regions []string) []*InstanceTypeStatic {
	byName := map[string]*InstanceTypeStatic{}

	taken := map[string]bool{}

	for _, region := range regions {
		for _, zone := range p.cloudCapacityProvider.Zones(region) {
			for _, offer := range p.cloudCapacityProvider.Slots(region, zone) {
				for _, tmpl := range p.numaTemplates {
					if tmpl.NUMA != offer.Shape.Fraction {
						continue
					}

					family := tmpl.Name
					name := numaTypeName(family, offer.Shape)

					// A static entry keeps its name; the NUMA type of the same name is not offered.
					if p.staticNames[name] {
						if !taken[name] {
							taken[name] = true

							p.log.Info("NUMA type name is taken by a static instance type, skipping it", "name", name, "template", tmpl.Name)
						}

						continue
					}

					it, ok := byName[name]
					if !ok {
						it = newNUMAType(tmpl, name, offer.Shape)
						byName[name] = it
					}

					it.Offerings = append(it.Offerings, zoneOfferings(it, region, zone, offer.Free > 0)...)
				}
			}
		}
	}

	return slices.SortedFunc(maps.Values(byName), func(a, b *InstanceTypeStatic) int {
		return cmp.Compare(a.Name, b.Name)
	})
}

// numaTypeName names the NUMA type of family for a slot shape: <family>.<cpu>VCPU-<memory>GB.
func numaTypeName(family string, shape zonecapacity.SlotShape) string {
	return instanceTypeName(family, shape.CPUs, int(shape.Memory/zonecapacity.GiB))
}

// newNUMAType is the NUMA type of tmpl for a slot shape, with no offerings: the slot's CPUs and
// memory, the template's other capacity, and its overhead, computed from the capacity when the
// template has none.
func newNUMAType(tmpl *InstanceTypeStatic, name string, shape zonecapacity.SlotShape) *InstanceTypeStatic {
	capacity := tmpl.Capacity.DeepCopy()
	if capacity == nil {
		capacity = corev1.ResourceList{}
	}

	capacity[corev1.ResourceCPU] = *resource.NewQuantity(int64(shape.CPUs), resource.DecimalSI)
	capacity[corev1.ResourceMemory] = *resource.NewQuantity(int64(shape.Memory), resource.BinarySI)

	overhead := &cloudprovider.InstanceTypeOverhead{
		KubeReserved:      kubeReservedResources(&capacity),
		SystemReserved:    systemReservedResources(&capacity),
		EvictionThreshold: evictionThresholdResources(&capacity),
	}

	if tmpl.Overhead != nil {
		overhead = copyOverhead(tmpl.Overhead)
	}

	return &InstanceTypeStatic{
		Name:         name,
		Capacity:     capacity,
		CapacityType: tmpl.CapacityType,
		Overhead:     overhead,
		Offerings:    cloudprovider.Offerings{},
		NUMA:         tmpl.NUMA,
	}
}
