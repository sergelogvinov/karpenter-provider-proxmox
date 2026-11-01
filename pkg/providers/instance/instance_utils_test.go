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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

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
	update := cloneConfigOverrides(cloneVMParams{
		resources: opt, tags: []string{"web", "prod"}, instanceTypeName: "cx4.medium", vmName: "node-1", root: true,
	}, 123, cfg)

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

// TestCloneConfigOverridesWithoutRoot checks that without root a pinned NUMA VM gets neither affinity nor
// hugepages, which only root@pam may set (ADR 0003), while its vCPUs and its NUMA binding stay set.
func TestCloneConfigOverridesWithoutRoot(t *testing.T) {
	t.Parallel()

	opt := &resources.VMResources{
		CPUs:         4,
		Memory:       8192 * 1024 * 1024,
		CPUSet:       cpuset.New(0, 1, 8, 9),
		HugePageSize: 2 * 1024 * 1024,
		NUMANodes: map[int]resources.NUMANodeState{
			1: {CPUs: "0-3", Memory: 8192, Policy: "bind"},
		},
	}

	update := cloneConfigOverrides(cloneVMParams{resources: opt, instanceTypeName: "cx4.medium", vmName: "node-1"}, 123, &qemu.Config{})

	assert.Empty(t, update.Affinity, "only root@pam may set affinity")
	assert.Nil(t, update.Hugepages, "only root@pam may set hugepages")
	assert.Equal(t, 4, *update.Cores)
	assert.True(t, *update.NUMAEnabled)
	assert.Equal(t, "cpus=0-3,hostnodes=1,memory=8192,policy=bind", update.NUMA[0].String())
}

var (
	// oneNodePool and twoNodePool are hugepage pools of 1GiB pages, hugepageTemplate's page size, in a zone of
	// one and of two NUMA nodes.
	oneNodePool = zonecapacity.Pool{PageSize: 1 << 30, NUMANodes: 1}
	twoNodePool = zonecapacity.Pool{PageSize: 1 << 30, NUMANodes: 2}
)

// hugepageTemplate is the cloned config of a hugepage template of 1GiB pages that carries its own NUMA
// config of two guest NUMA nodes, one per socket.
func hugepageTemplate() *qemu.Config {
	return &qemu.Config{
		Hugepages:   new("1024"),
		NUMAEnabled: new(true),
		Sockets:     new(2),
		NUMA: map[int]qemu.NUMA{
			0: {CPUIDs: []string{"0-1"}, HostNodes: []string{"0"}, Memory: new(4096), Policy: "preferred"},
			1: {CPUIDs: []string{"2-3"}, HostNodes: []string{"1"}, Memory: new(4096), Policy: "preferred"},
		},
	}
}

// TestCloneConfigOverridesNonNUMATypeFromHugepageTemplate checks that without root a non-NUMA type launched
// from a hugepage template in a zone of one NUMA node gets one guest NUMA node holding all its vCPUs and
// memory, bound to host node 0, in place of the template's NUMA config: Proxmox needs numa for hugepages, and
// its default per-socket split would want a host node per socket.
func TestCloneConfigOverridesNonNUMATypeFromHugepageTemplate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opt  *resources.VMResources
		numa string
	}{
		{
			name: "shared",
			opt:  &resources.VMResources{CPUs: 4, Memory: 8 << 30, CPUSet: cpuset.New()},
			numa: "cpus=0-3,hostnodes=0,memory=8192,policy=bind",
		},
		{
			name: "CPU share",
			opt:  &resources.VMResources{CPUs: 2, Memory: 2 << 30, CPUShare: 2, CPUSet: cpuset.New()},
			numa: "cpus=0-1,hostnodes=0,memory=2048,policy=bind",
		},
		{
			name: "pinned",
			opt: &resources.VMResources{
				CPUs: 4, Memory: 8 << 30, CPUSet: cpuset.New(4, 5, 6, 7),
				NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0-3", Memory: 8192, Policy: "bind"}},
			},
			numa: "cpus=0-3,hostnodes=0,memory=8192,policy=bind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			update := cloneConfigOverrides(cloneVMParams{resources: tc.opt, zonePool: oneNodePool}, 123, hugepageTemplate())

			assert.True(t, *update.NUMAEnabled)
			assert.Equal(t, new(1), update.Sockets, "one guest NUMA node is one socket, in place of the template's two")
			assert.Equal(t, new(tc.opt.CPUs), update.Cores, "the vCPUs are the instance type's")
			assert.Len(t, update.NUMA, 1)
			assert.Equal(t, tc.numa, update.NUMA[0].String())
			assert.Equal(t, []string{"numa1"}, update.Delete, "the template's other guest NUMA nodes are removed")
			assert.Nil(t, update.Hugepages, "hugepages come from the template")
			assert.Empty(t, update.Affinity)
		})
	}
}

// TestCloneConfigOverridesNonNUMATypeBindsZoneNUMANode checks that the one guest NUMA node is bound to the
// zone's only host NUMA node, whatever its number.
func TestCloneConfigOverridesNonNUMATypeBindsZoneNUMANode(t *testing.T) {
	t.Parallel()

	pool := zonecapacity.Pool{PageSize: 1 << 30, NUMANodes: 1, HostNode: 1}
	opt := &resources.VMResources{CPUs: 2, Memory: 4 << 30, CPUSet: cpuset.New()}

	update := cloneConfigOverrides(cloneVMParams{resources: opt, zonePool: pool}, 123, hugepageTemplate())

	assert.Equal(t, "cpus=0-1,hostnodes=1,memory=4096,policy=bind", update.NUMA[0].String())
}

// TestCloneConfigOverridesOtherLaunchesUnchanged checks that only a non-root launch of a non-NUMA type from a
// hugepage template of the pool's page size in a zone of one NUMA node gets the one guest NUMA node: where the
// template offers non-NUMA types.
func TestCloneConfigOverridesOtherLaunchesUnchanged(t *testing.T) {
	t.Parallel()

	shared := func() *resources.VMResources {
		return &resources.VMResources{CPUs: 4, Memory: 8 << 30, CPUSet: cpuset.New()}
	}

	otherPageSize := hugepageTemplate()
	otherPageSize.Hugepages = new("2")

	for _, tc := range []struct {
		name   string
		params cloneVMParams
		cfg    *qemu.Config
	}{
		{name: "two NUMA nodes", params: cloneVMParams{resources: shared(), zonePool: twoNodePool}, cfg: hugepageTemplate()},
		{name: "NUMA nodes not known", params: cloneVMParams{resources: shared()}, cfg: hugepageTemplate()},
		{
			name:   "one NUMA node without a pool",
			params: cloneVMParams{resources: shared(), zonePool: zonecapacity.Pool{NUMANodes: 1}}, cfg: hugepageTemplate(),
		},
		{name: "template of another page size", params: cloneVMParams{resources: shared(), zonePool: oneNodePool}, cfg: otherPageSize},
		{name: "template without hugepages", params: cloneVMParams{resources: shared(), zonePool: oneNodePool}, cfg: &qemu.Config{}},
		{name: "root", params: cloneVMParams{resources: shared(), zonePool: oneNodePool, root: true}, cfg: hugepageTemplate()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			update := cloneConfigOverrides(tc.params, 123, tc.cfg)

			assert.Nil(t, update.Sockets)
			assert.Nil(t, update.NUMAEnabled)
			assert.Empty(t, update.NUMA)
			assert.Empty(t, update.Delete)
		})
	}

	t.Run("NUMA type", func(t *testing.T) {
		t.Parallel()

		opt := &resources.VMResources{
			CPUs: 2, Memory: 4 << 30, CPUSet: cpuset.New(2, 3), HugePageSize: 1 << 30,
			Slot:      &zonecapacity.SlotShape{CPUs: 2, Memory: 4 << 30},
			NUMANodes: map[int]resources.NUMANodeState{0: {CPUs: "0-1", Memory: 4096, Policy: "bind"}},
		}

		update := cloneConfigOverrides(cloneVMParams{resources: opt, zonePool: oneNodePool}, 123, hugepageTemplate())

		assert.True(t, *update.NUMAEnabled)
		assert.Len(t, update.NUMA, 1)
		assert.Equal(t, "cpus=0-1,hostnodes=0,memory=4096,policy=bind", update.NUMA[0].String())
		assert.Empty(t, update.Delete)
	})
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

	update := cloneConfigOverrides(cloneVMParams{resources: opt, instanceTypeName: "cx4.medium", vmName: "node-1", root: true}, 123, cfg)

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

	update := cloneConfigOverrides(cloneVMParams{resources: opt, instanceTypeName: "cx4.medium", vmName: "node-1", root: true}, 123, cfg)

	assert.Nil(t, update.Cores)
	assert.Empty(t, update.Affinity)
	assert.Nil(t, update.Memory)
	assert.Nil(t, update.NUMAEnabled)
	assert.Nil(t, update.Tags)
	assert.NotNil(t, update.SMBios1)
}
