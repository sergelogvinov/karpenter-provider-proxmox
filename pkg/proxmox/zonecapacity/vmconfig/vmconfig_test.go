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

package vmconfig_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/vmconfig"
)

const gib = uint64(1 << 30)

func TestVM(t *testing.T) {
	t.Parallel()

	tags := qemu.Tags{"karpenter"}

	for _, tc := range []struct {
		name    string
		cfg     *qemu.Config
		want    zonecapacity.VM
		ignored bool
	}{
		{
			name: "field copy",
			cfg: &qemu.Config{
				Name:        "worker-7",
				Cores:       new(4),
				Memory:      &qemu.Memory{Current: new(8192)},
				Affinity:    "0-3",
				Description: "Karpenter managed instance, affinity=0-3",
				Tags:        &tags,
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"1"}, Memory: new(8192)},
				},
			},
			want: zonecapacity.VM{
				ID: 7, Name: "worker-7", CPUs: 4, Memory: 8 * gib,
				Affinity:    "0-3",
				Description: "Karpenter managed instance, affinity=0-3",
				Tags:        []string{"karpenter"},
				NUMAMemory:  map[int]uint64{1: 8 * gib},
			},
		},
		{
			name: "sockets times cores",
			cfg:  &qemu.Config{Sockets: new(2), Cores: new(3), Memory: &qemu.Memory{Current: new(1024)}},
			want: zonecapacity.VM{ID: 7, CPUs: 6, Memory: gib},
		},
		{
			name: "vcpus wins over sockets and cores",
			cfg:  &qemu.Config{Sockets: new(2), Cores: new(4), VCPUs: new(3), Memory: &qemu.Memory{Current: new(1024)}},
			want: zonecapacity.VM{ID: 7, CPUs: 3, Memory: gib},
		},
		{
			name: "unset CPUs and memory take the Proxmox defaults",
			cfg:  &qemu.Config{},
			want: zonecapacity.VM{ID: 7, CPUs: 1, Memory: 512 * zonecapacity.MiB},
		},
		{
			name: "numa memory splits evenly across host nodes",
			cfg: &qemu.Config{
				Cores:  new(4),
				Memory: &qemu.Memory{Current: new(8192)},
				NUMA: map[int]qemu.NUMA{
					0: {HostNodes: []string{"0-1"}, Memory: new(4096)},
					1: {HostNodes: []string{"1"}, Memory: new(4096)},
					2: {HostNodes: []string{"0"}},
				},
			},
			want: zonecapacity.VM{ID: 7, CPUs: 4, Memory: 8 * gib, NUMAMemory: map[int]uint64{0: 2 * gib, 1: 6 * gib}},
		},
		{
			name: "unparseable host nodes skip the entry with a warning",
			cfg: &qemu.Config{
				Cores:  new(2),
				Memory: &qemu.Memory{Current: new(2048)},
				NUMA: map[int]qemu.NUMA{
					0: {HostNodes: []string{"x"}, Memory: new(1024)},
					1: {HostNodes: []string{"1"}, Memory: new(1024)},
				},
			},
			want:    zonecapacity.VM{ID: 7, CPUs: 2, Memory: 2 * gib, NUMAMemory: map[int]uint64{1: gib}},
			ignored: true,
		},
		{
			name: "the affinity is left to the zone",
			cfg:  &qemu.Config{Cores: new(2), Memory: &qemu.Memory{Current: new(1024)}, Affinity: "0-x"},
			want: zonecapacity.VM{ID: 7, CPUs: 2, Memory: gib, Affinity: "0-x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			vm, err := vmconfig.VM(7, tc.cfg)
			if tc.ignored {
				require.ErrorIs(t, err, vmconfig.ErrNUMAIgnored)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.want, vm)
		})
	}
}

func TestVMWithoutConfig(t *testing.T) {
	t.Parallel()

	_, err := vmconfig.VM(7, nil)
	require.ErrorIs(t, err, zonecapacity.ErrInvalid)
}
