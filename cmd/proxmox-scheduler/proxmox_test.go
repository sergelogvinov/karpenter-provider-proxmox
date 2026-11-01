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

package main

import (
	"slices"
	"testing"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-local/fakelocal"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/nodecapacity"
)

// TestEnsureNodeCapacityGuest drives ensureNodeCapacityGuest
// against a fakelocal node, with no coverage before this test existed
// (docs/design.md §12 Phase 3, item 6): a first run creates the
// node-capacity guest, and a second run against the same (now existing)
// guest updates it in place rather than creating a second one.
func TestEnsureNodeCapacityGuest(t *testing.T) {
	serverInfo := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 16 * 1024 * 1024 * 1024, // 16 GiB
	}

	f := fakelocal.New(t, fakelocal.WithQuorum(true), fakelocal.WithNextID(101))
	client := f.Client()

	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, serverInfo, nil))

	f.AssertRan("qm", "create", "101",
		"--cores", "8",
		"--cpu", "host",
		"--description", "Karpenter discovery service",
		"--memory", "16384",
		"--name", "node-capacity",
		"--numa", "1",
		"--ostype", "l26",
		"--sockets", "1",
		"--tags", "karpenter",
	)

	got := f.Guest(101)
	assert.Equal(t, "node-capacity", got.Name)
	assert.Equal(t, "Karpenter discovery service", got.Description)
	require.NotNil(t, got.Cores)
	assert.Equal(t, 8, *got.Cores)
	require.NotNil(t, got.Memory)
	require.NotNil(t, got.Memory.Current)
	assert.Equal(t, 16384, *got.Memory.Current)

	// A second run against a hypervisor that already has the guest, with
	// the same desired config, must find the existing guest and skip the
	// update entirely — nothing differs, so Update never calls `qm set`
	// (docs/design.md §7.3). This is the case the old map[string]any
	// idempotency bug (§2.4) broke: a uint64 memory value could never
	// compare equal to the int a YAML decode produced, so this exact
	// scenario ran `qm set --memory ...` on every resync.
	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, serverInfo, nil))

	guests, err := client.Qemu().List(t.Context(), qemu.ListFilter{Name: "node-capacity"})
	require.NoError(t, err)
	assert.Len(t, guests, 1, "the second run must update, not duplicate, the existing guest")

	for _, argv := range f.Ran() {
		if argv[0] == "qm" && argv[1] == "set" {
			t.Fatalf("qm set was called when nothing had changed: %v", argv)
		}
	}
}

// TestEnsureNodeCapacityGuestHugepages covers the hugepage pool
// on the wire: a host with a pool writes hugepages and the per-node pool
// (memory=0 included), and once the pool is gone the option is deleted
// rather than left behind.
func TestEnsureNodeCapacityGuestHugepages(t *testing.T) {
	const gib = 1024 * 1024 * 1024

	tp := dualNodeTopology()
	withPool := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 64 * gib,
		HugePages:      []info.HugePagesInfo{{PageSize: nodecapacity.HugepageSize1G, NumPages: 16}},
		Topology: []info.Node{
			{Id: 0, Memory: 32 * gib, HugePages: []info.HugePagesInfo{{PageSize: nodecapacity.HugepageSize1G, NumPages: 16}}},
			{Id: 1, Memory: 32 * gib},
		},
	}

	f := fakelocal.New(t, fakelocal.WithQuorum(true), fakelocal.WithNextID(101))
	client := f.Client()

	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, withPool, tp))

	got := f.Guest(101)
	require.NotNil(t, got.Hugepages)
	assert.Equal(t, "1024", *got.Hugepages)
	require.NotNil(t, got.NUMA[1].Memory)
	assert.Equal(t, 0, *got.NUMA[1].Memory)
	assert.Equal(t, []string{"4-7"}, got.NUMA[1].CPUIDs)

	withoutPool := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 64 * gib,
		Topology: []info.Node{
			{Id: 0, Memory: 32 * gib},
			{Id: 1, Memory: 32 * gib},
		},
	}

	ran := len(f.Ran())

	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, withoutPool, tp))

	// fakelocal does not apply --delete, so assert on the command sent.
	deleted := false

	for _, argv := range f.Ran()[ran:] {
		if argv[0] == "qm" && argv[1] == "set" && slices.Contains(argv, "--delete") {
			i := slices.Index(argv, "--delete")
			deleted = i+1 < len(argv) && argv[i+1] == "hugepages"
		}
	}

	assert.True(t, deleted, "a pool the host no longer has must be deleted: %v", f.Ran()[ran:])

	got = f.Guest(101)
	require.NotNil(t, got.NUMA[0].Memory)
	assert.Equal(t, 31*1024, *got.NUMA[0].Memory)
}

// TestEnsureNodeCapacityGuestMemory covers an update of the
// existing guest: memory and each NUMA node's hugepage pool change
// independently (nodecapacity.StaleOptions covers the options deleted).
func TestEnsureNodeCapacityGuestMemory(t *testing.T) {
	const gib = 1024 * 1024 * 1024

	pages1G := func(n uint64) []info.HugePagesInfo {
		return []info.HugePagesInfo{{PageSize: nodecapacity.HugepageSize1G, NumPages: n}}
	}

	tp := dualNodeTopology()
	before := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 64 * gib,
		HugePages:      pages1G(16),
		Topology: []info.Node{
			{Id: 0, Memory: 32 * gib, HugePages: pages1G(8)},
			{Id: 1, Memory: 32 * gib, HugePages: pages1G(8)},
		},
	}

	f := fakelocal.New(t, fakelocal.WithQuorum(true), fakelocal.WithNextID(101))
	client := f.Client()

	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, before, tp))

	// Only memory changes: the hugepage pools stay the same.
	memoryOnly := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 48 * gib,
		HugePages:      pages1G(16),
		Topology: []info.Node{
			{Id: 0, Memory: 24 * gib, HugePages: pages1G(8)},
			{Id: 1, Memory: 24 * gib, HugePages: pages1G(8)},
		},
	}

	ran := len(f.Ran())

	require.NoError(t, ensureNodeCapacityGuest(logr.Discard(), client, memoryOnly, tp))

	var set []string

	for _, argv := range f.Ran()[ran:] {
		if argv[0] == "qm" && argv[1] == "set" {
			set = argv
		}
	}

	require.NotNil(t, set, "a memory change must update the guest")
	assert.Contains(t, set, "--memory")
	assert.NotContains(t, set, "--numa0", "unchanged NUMA nodes are not sent")
	assert.NotContains(t, set, "--delete")

	got := f.Guest(101)
	require.NotNil(t, got.Memory)
	assert.Equal(t, 2*23*1024, *got.Memory.Current)
	assert.Equal(t, 8*1024, *got.NUMA[0].Memory)
	assert.Equal(t, 8*1024, *got.NUMA[1].Memory)
}
