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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type InstanceTypeStatic struct {
	Name         string                              `json:"name,omitempty"`
	Capacity     corev1.ResourceList                 `json:"capacity,omitempty"`
	Overhead     *cloudprovider.InstanceTypeOverhead `json:"overhead,omitempty"`
	Requirements scheduling.Requirements             `json:"requirements,omitempty"`
}

type InstanceTypeOptions struct {
	CPUs       []int
	MemFactors []int
	Storage    int

	KubeletOverhead   bool
	SystemOverhead    bool
	EvictionThreshold bool
}

func (o *InstanceTypeOptions) Generate() []*InstanceTypeStatic {
	instanceTypes := []*InstanceTypeStatic{}

	for _, cpu := range o.CPUs {
		for _, memFactor := range o.MemFactors {
			mem := cpu * memFactor
			pods := 110

			switch {
			case mem <= 1:
				pods = 32
			case mem <= 2:
				pods = 64
			}

			capacity := corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", cpu)),
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dGi", mem)),
				corev1.ResourcePods:   resource.MustParse(fmt.Sprintf("%d", pods)),
			}

			if o.Storage > 0 {
				capacity[corev1.ResourceEphemeralStorage] = resource.MustParse(fmt.Sprintf("%dGi", o.Storage))
			}

			instanceType := InstanceTypeStatic{
				Name:     makeGenericInstanceTypeName(cpu, memFactor),
				Capacity: capacity,
			}

			if o.KubeletOverhead || o.SystemOverhead || o.EvictionThreshold {
				instanceType.Overhead = &cloudprovider.InstanceTypeOverhead{}

				if o.KubeletOverhead {
					instanceType.Overhead.KubeReserved = kubeReservedResources(&capacity)
				}

				if o.SystemOverhead {
					instanceType.Overhead.SystemReserved = systemReservedResources(&capacity)
				}

				if o.EvictionThreshold {
					instanceType.Overhead.EvictionThreshold = evictionThresholdResources(&capacity)
				}
			}

			instanceTypes = append(instanceTypes, &instanceType)
		}
	}

	return instanceTypes
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

func systemReservedResources(_ *corev1.ResourceList) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("64Mi"),
	}
}

func kubeReservedResources(capacity *corev1.ResourceList) corev1.ResourceList {
	cpuResource := resource.MustParse("100m")
	memResource := resource.MustParse("384Mi")

	cpu := capacity.Cpu().Value()
	mem := capacity.Memory().Value() / 1024 / 1024

	switch {
	case cpu < 1:
		cpuResource = resource.MustParse("10m")
	case cpu < 2:
		cpuResource = resource.MustParse("20m")
	case cpu < 4:
		cpuResource = resource.MustParse("50m")
	}

	switch {
	case mem < 1:
		memResource = resource.MustParse("128Mi")
	case mem < 2:
		memResource = resource.MustParse("192Mi")
	case mem < 4:
		memResource = resource.MustParse("256Mi")
	case mem < 8:
		memResource = resource.MustParse("384Mi")
	}

	resources := corev1.ResourceList{
		corev1.ResourceCPU:    cpuResource,
		corev1.ResourceMemory: memResource,
	}

	return resources
}

func evictionThresholdResources(_ *corev1.ResourceList) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("100Mi"),
	}
}
