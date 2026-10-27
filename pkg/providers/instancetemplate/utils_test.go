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
