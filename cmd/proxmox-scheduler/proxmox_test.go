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
	"testing"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-local/fakelocal"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
)

// TestCreateProxmoxTopologyDiscoveryVM drives createProxmoxTopologyDiscoveryVM
// against a fakelocal node, with no coverage before this test existed
// (docs/design.md §12 Phase 3, item 6): a first run creates the
// node-capacity guest, and a second run against the same (now existing)
// guest updates it in place rather than creating a second one.
func TestCreateProxmoxTopologyDiscoveryVM(t *testing.T) {
	serverInfo := &info.MachineInfo{
		NumCores:       8,
		MemoryCapacity: 16 * 1024 * 1024 * 1024, // 16 GiB
	}

	f := fakelocal.New(t, fakelocal.WithQuorum(true), fakelocal.WithNextID(101))
	client := f.Client()

	require.NoError(t, createProxmoxTopologyDiscoveryVM(logr.Discard(), client, serverInfo, nil))

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
	require.NoError(t, createProxmoxTopologyDiscoveryVM(logr.Discard(), client, serverInfo, nil))

	guests, err := client.Qemu().List(t.Context(), qemu.ListFilter{Name: "node-capacity"})
	require.NoError(t, err)
	assert.Len(t, guests, 1, "the second run must update, not duplicate, the existing guest")

	for _, argv := range f.Ran() {
		if argv[0] == "qm" && argv[1] == "set" {
			t.Fatalf("qm set was called when nothing had changed: %v", argv)
		}
	}
}
