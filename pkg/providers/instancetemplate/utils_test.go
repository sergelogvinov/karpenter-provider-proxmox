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

package instancetemplate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
)

func TestMergeDisks(t *testing.T) {
	t.Parallel()

	cfg := &qemu.Config{
		SCSI: map[int]qemu.Drive{
			0: {File: "local-lvm:vm-100-disk-0"},
		},
		IDE: map[int]qemu.Drive{
			2: {File: "none"},
		},
		SATA: map[int]qemu.Drive{
			0: {File: "local:100/vm-100-disk-1.raw"},
		},
	}

	disks := mergeDisks(cfg)

	assert.ElementsMatch(t, []string{"local-lvm:vm-100-disk-0", "local:100/vm-100-disk-1.raw"}, disks)
}

func TestTargetZones(t *testing.T) {
	t.Parallel()

	local := func(zones ...string) *cloudcapacity.NodeStorageCapacityInfo {
		return &cloudcapacity.NodeStorageCapacityInfo{Name: "local", Zones: zones}
	}
	shared := func(zones ...string) *cloudcapacity.NodeStorageCapacityInfo {
		return &cloudcapacity.NodeStorageCapacityInfo{Name: "ceph", Shared: true, Zones: zones}
	}
	existing := func(zones ...string) []InstanceTemplateInfo {
		infos := []InstanceTemplateInfo{}
		for _, zone := range zones {
			infos = append(infos, InstanceTemplateInfo{Name: "talos", Zone: zone})
		}

		return infos
	}

	for _, tc := range []struct {
		name            string
		storageImage    *cloudcapacity.NodeStorageCapacityInfo
		storageTemplate *cloudcapacity.NodeStorageCapacityInfo
		existing        []InstanceTemplateInfo
		want            []string
	}{
		{
			name:            "local template storage targets every zone with both storages",
			storageImage:    local("pve-1", "pve-2", "pve-3"),
			storageTemplate: local("pve-2", "pve-3", "pve-4"),
			want:            []string{"pve-2", "pve-3"},
		},
		{
			name:            "shared template storage without a template takes the first zone",
			storageImage:    local("pve-1", "pve-2"),
			storageTemplate: shared("pve-1", "pve-2"),
			want:            []string{"pve-1"},
		},
		{
			name:            "shared template storage keeps the zone of the existing template",
			storageImage:    local("pve-1", "pve-2"),
			storageTemplate: shared("pve-1", "pve-2"),
			existing:        existing("pve-2"),
			want:            []string{"pve-2"},
		},
		{
			name:            "shared template storage ignores a template outside the storage",
			storageImage:    local("pve-1", "pve-2"),
			storageTemplate: shared("pve-1", "pve-2"),
			existing:        existing("pve-9"),
			want:            []string{"pve-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, targetZones(tc.storageImage, tc.storageTemplate, tc.existing))
		})
	}
}
