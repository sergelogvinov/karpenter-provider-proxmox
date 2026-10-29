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
)

// TestGeneratedCPUShareTypesLoad checks that the generator output with CPU shares 2, 3 and 4 loads in the
// controller as the CPU share types of the e2, e3 and e4 families, next to the dedicated types.
func TestGeneratedCPUShareTypesLoad(t *testing.T) {
	t.Parallel()

	opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2}, Storage: 30, CPUShares: []int{2, 3, 4}}

	file, err := json.Marshal(opts.Generate())
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
