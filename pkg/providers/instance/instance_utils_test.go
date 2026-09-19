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

package instance

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

func TestDetectBootDisk(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		cfg      *qemu.Config
		expected string
	}{
		{
			name:     "no disks",
			cfg:      &qemu.Config{},
			expected: "",
		},
		{
			name:     "virtio0 preferred over scsi0",
			cfg:      &qemu.Config{VirtIO: map[int]qemu.Drive{0: {File: "local:100/vm-100-disk-0.raw"}}, SCSI: map[int]qemu.Drive{0: {File: "local:100/vm-100-disk-1.raw"}}},
			expected: "virtio0",
		},
		{
			name:     "scsi0 preferred over sata0/ide0",
			cfg:      &qemu.Config{SCSI: map[int]qemu.Drive{0: {File: "local:100/vm-100-disk-0.raw"}}, SATA: map[int]qemu.Drive{0: {File: "x"}}, IDE: map[int]qemu.Drive{0: {File: "x"}}},
			expected: "scsi0",
		},
		{
			name:     "sata0 preferred over ide0",
			cfg:      &qemu.Config{SATA: map[int]qemu.Drive{0: {File: "local:100/vm-100-disk-0.raw"}}, IDE: map[int]qemu.Drive{0: {File: "x"}}},
			expected: "sata0",
		},
		{
			name:     "ide0 as last resort",
			cfg:      &qemu.Config{IDE: map[int]qemu.Drive{0: {File: "local:100/vm-100-disk-0.raw"}}},
			expected: "ide0",
		},
		{
			name:     "index 0 empty, other indices ignored",
			cfg:      &qemu.Config{SCSI: map[int]qemu.Drive{1: {File: "local:100/vm-100-disk-1.raw"}}},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.expected, detectBootDisk(tc.cfg))
		})
	}
}

func TestCloneConfigOverrides(t *testing.T) {
	t.Parallel()

	opt := &resources.VMResources{
		CPUs:   4,
		Memory: 8192 * 1024 * 1024,
		CPUSet: cpuset.New(0, 1, 8, 9),
		NUMANodes: map[int]resources.NUMANodeState{
			0: {CPUs: "0-1", Memory: 4096, Policy: "bind"},
		},
	}

	cfg := &qemu.Config{
		SMBios1: &qemu.SMBios1{Family: "template-family", Manufacturer: "acme"},
		Net: map[int]qemu.Net{
			0: {Model: "virtio", MACAddr: "BC:24:11:CD:B9:41", Bridge: "vmbr0"},
		},
	}

	// cloneConfigOverrides joins tags in the order given — sorting, if
	// wanted, is the caller's job (nodeClass.Spec.Tags isn't sorted before
	// reaching here today, matching the old goproxmox.CloneVM's behavior).
	update := cloneConfigOverrides(opt, []string{"web", "prod"}, "cx4.medium", "node-1", 123, cfg)

	assert.Equal(t, 4, *update.Cores)
	assert.Equal(t, "0-1,8-9", update.Affinity)
	assert.Equal(t, "8192", update.Memory.String())
	assert.True(t, *update.NUMAEnabled)
	assert.Equal(t, "cpus=0-1,hostnodes=0,memory=4096,policy=bind", update.NUMA[0].String())
	assert.Equal(t, qemu.Tags{"web", "prod"}, *update.Tags)
	// The model=mac pair (and any other property) must survive verbatim;
	// only queues is added.
	assert.Equal(t, "virtio=BC:24:11:CD:B9:41,bridge=vmbr0,queues=4", update.Net[0].String())

	wantSKU := base64.StdEncoding.EncodeToString([]byte("cx4.medium"))
	wantSerial := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "h=%s;i=%d", "node-1", 123))
	wantFamily := base64.StdEncoding.EncodeToString([]byte("template-family"))
	wantManufacturer := base64.StdEncoding.EncodeToString([]byte("acme"))

	// The template's smbios1 had no base64 flag set, so family/manufacturer
	// were plaintext: since we force base64=1 for the SKU/Serial we add,
	// the inherited plaintext values must be encoded too, or Proxmox would
	// try (and fail) to base64-decode them.
	smbios := update.SMBios1.String()
	assert.Contains(t, smbios, "sku="+wantSKU)
	assert.Contains(t, smbios, "serial="+wantSerial)
	assert.Contains(t, smbios, "family="+wantFamily)
	assert.Contains(t, smbios, "manufacturer="+wantManufacturer)
	assert.Contains(t, smbios, "base64=1")
}

func TestCloneConfigOverridesSMBiosAlreadyBase64(t *testing.T) {
	t.Parallel()

	opt := &resources.VMResources{CPUSet: cpuset.New()}

	familyB64 := base64.StdEncoding.EncodeToString([]byte("template-family"))
	manufacturerB64 := base64.StdEncoding.EncodeToString([]byte("acme"))

	cfg := &qemu.Config{
		SMBios1: &qemu.SMBios1{
			Base64:       new(true),
			Family:       familyB64,
			Manufacturer: manufacturerB64,
			UUID:         "e536baad-cf78-4543-9034-60295eed2b22",
		},
	}

	update := cloneConfigOverrides(opt, nil, "cx4.medium", "node-1", 123, cfg)

	// Already-encoded fields must not be encoded a second time, and the
	// plain UUID must survive untouched (Proxmox never base64-encodes it).
	smbios := update.SMBios1.String()
	assert.Contains(t, smbios, "family="+familyB64)
	assert.Contains(t, smbios, "manufacturer="+manufacturerB64)
	assert.Contains(t, smbios, "uuid=e536baad-cf78-4543-9034-60295eed2b22")
	assert.Contains(t, smbios, "base64=1")
}

func TestCloneConfigOverridesEmpty(t *testing.T) {
	t.Parallel()

	opt := &resources.VMResources{CPUSet: cpuset.New()}
	cfg := &qemu.Config{}

	update := cloneConfigOverrides(opt, nil, "cx4.medium", "node-1", 123, cfg)

	assert.Nil(t, update.Cores)
	assert.Empty(t, update.Affinity)
	assert.Nil(t, update.Memory)
	assert.Nil(t, update.NUMAEnabled)
	assert.Nil(t, update.Tags)
	assert.NotNil(t, update.SMBios1)
}
