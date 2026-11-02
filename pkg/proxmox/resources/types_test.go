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

package resources_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/launchrule"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
)

// TestSlotFollowsProfile checks that only a VM of a NUMA type asks for a NUMA slot, of its slot fraction and
// sized exactly as the VM.
func TestSlotFollowsProfile(t *testing.T) {
	t.Parallel()

	vm := func(p launchrule.Profile) resources.VMResources {
		return resources.VMResources{CPUs: 4, Memory: 8 << 30, Profile: p}
	}

	require.Equal(t, &zonecapacity.SlotShape{Fraction: 2, CPUs: 4, Memory: 8 << 30}, vm(launchrule.NUMAType(2)).Slot())
	require.Nil(t, vm(launchrule.DedicatedType()).Slot())
	require.Nil(t, vm(launchrule.CPUShareType(2)).Slot())
}
