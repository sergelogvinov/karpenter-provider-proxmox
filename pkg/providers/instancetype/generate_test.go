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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
)

func TestGenerateCPUShareFamilies(t *testing.T) {
	t.Parallel()

	opts := instancetype.InstanceTypeOptions{CPUs: []int{2}, MemFactors: []int{2, 4}, CPUShares: []int{2, 4}}

	got := map[string]int{}
	for _, it := range opts.Generate() {
		got[it.Name] = it.CPUShare
	}

	require.Equal(t, map[string]int{
		"c1.2VCPU-4GB": 0,
		"s1.2VCPU-8GB": 0,
		"e2.2VCPU-4GB": 2,
		"e2.2VCPU-8GB": 2,
		"e4.2VCPU-4GB": 4,
		"e4.2VCPU-8GB": 4,
	}, got)
}
