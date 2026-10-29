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
	"slices"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// cpuShareFamily is the family letter of the CPU share types; the digit after it is the number of seats (ADR 0002).
const cpuShareFamily = "e"

// dedicatedFamilies are the family letters of the dedicated types, by memory factor (memory GiB per vCPU).
var dedicatedFamilies = map[int]string{
	2:  "c", // cpu
	3:  "t",
	4:  "s", // standard
	8:  "m", // memory
	16: "x", // in-memory applications
}

type InstanceTypeStatic struct {
	Name         string                              `json:"name,omitempty"`
	Capacity     corev1.ResourceList                 `json:"capacity,omitempty"`
	CapacityType string                              `json:"capacitytype,omitempty"`
	Overhead     *cloudprovider.InstanceTypeOverhead `json:"overhead,omitempty"`
	Offerings    cloudprovider.Offerings             `json:"offerings,omitempty"`
	// CPUShare is the number of VMs that share the instance's CPUs: 2, 3 or 4. Zero means dedicated CPUs.
	CPUShare int `json:"cpushare,omitempty"`
	// NUMA marks the entry as a NUMA template and is its slot fraction: 1, 2 or 4.
	NUMA int `json:"numa,omitempty"`
}

type InstanceTypeOptions struct {
	CPUs       []int
	MemFactors []int
	Storage    int
	// CPUShares adds the CPU share family e<N> of the same sizes for each N (2, 3 or 4).
	CPUShares []int

	KubeletOverhead   bool
	SystemOverhead    bool
	EvictionThreshold bool
}

// Generate returns the instance types of every size, followed by the CPU share types of each of CPUShares.
// Repeated values are generated once. It fails for a memory factor without a dedicated family, and for
// a CPU share that is not a valid number of seats.
func (o *InstanceTypeOptions) Generate() ([]*InstanceTypeStatic, error) {
	for _, memFactor := range o.MemFactors {
		if _, ok := dedicatedFamilies[memFactor]; !ok {
			factors := lo.Keys(dedicatedFamilies)
			slices.Sort(factors)

			return nil, fmt.Errorf("memory factor %d has no instance family, must be one of %v", memFactor, factors)
		}
	}

	for _, share := range o.CPUShares {
		if !zonecapacity.ValidCPUShare(share) {
			return nil, fmt.Errorf("cpushare %d must be %s", share, zonecapacity.CPUShareSeats)
		}
	}

	instanceTypes := o.generate(0)

	for _, share := range lo.Uniq(o.CPUShares) {
		instanceTypes = append(instanceTypes, o.generate(share)...)
	}

	return instanceTypes, nil
}

// generate returns the instance types of every size: CPU share types of share seats, or dedicated ones if share is 0.
func (o *InstanceTypeOptions) generate(share int) []*InstanceTypeStatic {
	instanceTypes := []*InstanceTypeStatic{}

	for _, cpu := range lo.Uniq(o.CPUs) {
		for _, memFactor := range lo.Uniq(o.MemFactors) {
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
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%d", uint64(mem)*zonecapacity.GiB)),
				corev1.ResourcePods:   resource.MustParse(fmt.Sprintf("%d", pods)),
				// corev1.ResourceHugePagesPrefix + "1Gi": resource.MustParse(fmt.Sprintf("%dGi", 1)),
			}

			if o.Storage > 0 {
				capacity[corev1.ResourceEphemeralStorage] = resource.MustParse(fmt.Sprintf("%dGi", o.Storage))
			}

			instanceType := InstanceTypeStatic{
				Name:         makeGenericInstanceTypeName(cpu, memFactor, share),
				Capacity:     capacity,
				CapacityType: karpv1.CapacityTypeOnDemand,
				CPUShare:     share,
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

// makeGenericInstanceTypeName names the instance type <family><digit>.<vCPU>VCPU-<memory>GB. A dedicated type's
// family letter comes from its memory factor, one of dedicatedFamilies, and its digit is the generation, 1.
// A CPU share type's family letter is e and its digit is the number of seats (see ADR 0002).
func makeGenericInstanceTypeName(cpu, memFactor, share int) string {
	family, digit := dedicatedFamilies[memFactor], 1
	if share > 0 {
		family, digit = cpuShareFamily, share
	}

	return instanceTypeName(fmt.Sprintf("%s%d", family, digit), cpu, cpu*memFactor)
}

// instanceTypeName is the name <family>.<vCPU>VCPU-<memory>GB of every generated instance type (see ADR 0002).
func instanceTypeName(family string, cpu, memoryGB int) string {
	return fmt.Sprintf("%s.%dVCPU-%dGB", family, cpu, memoryGB)
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
