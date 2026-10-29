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

package instancetype_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	corev1 "k8s.io/api/core/v1"
)

// TestGeneratedCPUShareTypesLoad checks that the generator output with CPU shares 2, 3 and 4 loads in the
// controller as the CPU share types of the e2, e3 and e4 families, next to the dedicated types.
func TestGeneratedCPUShareTypesLoad(t *testing.T) {
	t.Parallel()

	opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2}, Storage: 30, CPUShares: []int{2, 3, 4}}

	instanceTypes, err := opts.Generate()
	require.NoError(t, err)

	file, err := json.Marshal(instanceTypes)
	require.NoError(t, err)

	p, err := newProvider(t, "simple", string(file))
	require.NoError(t, err)

	types, err := p.List(context.Background(), nil)
	require.NoError(t, err)

	type loaded struct {
		family   string
		cpuShare int
	}

	got := map[string]loaded{}
	for _, it := range types {
		got[it.Name] = loaded{
			family:   it.Requirements.Get(v1alpha1.LabelInstanceFamily).Any(),
			cpuShare: p.CPUShare(it.Name),
		}
	}

	require.Equal(t, map[string]loaded{
		"c1.2VCPU-4GB": {family: "c1"},
		"e2.2VCPU-4GB": {family: "e2", cpuShare: 2},
		"e3.2VCPU-4GB": {family: "e3", cpuShare: 3},
		"e4.2VCPU-4GB": {family: "e4", cpuShare: 4},
	}, got)
}

// TestGenerateRejectsUnsupportedMemFactor checks that a memory ratio without a family is rejected, so that a
// dedicated type never lands in the e family of the CPU share types.
func TestGenerateRejectsUnsupportedMemFactor(t *testing.T) {
	t.Parallel()

	for _, memFactor := range []int{0, 1, 5, 6, 32} {
		opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{memFactor}}

		_, err := opts.Generate()
		require.ErrorContains(t, err, "memory factor", "memFactor %d", memFactor)
	}
}

func TestGenerateRejectsInvalidCPUShare(t *testing.T) {
	t.Parallel()

	for _, share := range []int{-1, 1, 5} {
		opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2}, CPUShares: []int{share}}

		_, err := opts.Generate()
		require.ErrorContains(t, err, "cpushare", "cpushare %d", share)
	}
}

// TestGenerateNUMATemplates checks that each slot fraction adds one NUMA template, named after its n<fraction>
// family, after the instance types, and that the output loads in the controller.
func TestGenerateNUMATemplates(t *testing.T) {
	t.Parallel()

	opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2}, Storage: 30, NUMA: []int{1, 2, 4, 2}}

	instanceTypes, err := opts.Generate()
	require.NoError(t, err)

	got := map[string]int{}
	for _, it := range instanceTypes {
		got[it.Name] = it.NUMA
	}

	require.Equal(t, map[string]int{"c1.2VCPU-4GB": 0, "n1": 1, "n2": 2, "n4": 4}, got)

	n1 := instanceTypes[1]
	require.Equal(t, "n1", n1.Name)
	require.Equal(t, "30Gi", n1.Capacity.StorageEphemeral().String())
	require.Equal(t, "110", n1.Capacity.Pods().String())
	require.NotContains(t, n1.Capacity, corev1.ResourceCPU, "cpu comes from the slot")
	require.NotContains(t, n1.Capacity, corev1.ResourceMemory, "memory comes from the slot")
	require.Nil(t, n1.Overhead, "the overhead is computed for each generated NUMA type")

	file, err := json.Marshal(instanceTypes)
	require.NoError(t, err)

	_, err = newProvider(t, "static", string(file))
	require.NoError(t, err)
}

func TestGenerateRejectsInvalidNUMA(t *testing.T) {
	t.Parallel()

	for _, fraction := range []int{-1, 0, 3, 8} {
		opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2}, NUMA: []int{fraction}}

		_, err := opts.Generate()
		require.ErrorContains(t, err, "numa", "numa %d", fraction)
	}
}
