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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"
)

func TestLaunchRollbackRunsNewestFirst(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rollback := &launchRollback{}
	order := []int{}

	for i := range 3 {
		rollback.add(func(ctx context.Context) {
			assert.NoError(t, ctx.Err(), "undo runs although the launch's context is canceled")

			order = append(order, i)
		})
	}

	rollback.run(ctx)
	rollback.run(ctx)

	assert.Equal(t, []int{2, 1, 0}, order, "newest first, and only once")
}

func TestGenerateNetworkIPsRollback(t *testing.T) {
	t.Parallel()

	ipam := nodeipam.NewDefaultProvider(t.Context(), nil)
	p := &DefaultProvider{nodeIpamProvider: ipam}

	// The first interface gets an address; the second fails to parse.
	networkConfig := cloudinit.NetworkConfig{Interfaces: []cloudinit.InterfaceConfig{
		{Address4: []string{"10.0.0.0/24"}},
		{Address4: []string{"not-a-cidr"}},
	}}

	rollback := &launchRollback{}
	require.Error(t, p.generateNetworkIPs(rollback, &networkConfig))

	allocated := networkConfig.Interfaces[0].Address4
	require.Len(t, allocated, 1)

	rollback.run(t.Context())

	ip, err := ipam.AllocateIP("10.0.0.0/24")
	require.NoError(t, err)
	assert.Equal(t, allocated[0], ip.String()+"/24", "the released address is handed out again")
}

func TestRemoveCloudInitISO(t *testing.T) {
	t.Parallel()

	const (
		zone  = "pve-1"
		vmID  = 100
		volid = "local:iso/vm-100-custom-cloudinit.iso"
	)

	tests := []struct {
		name     string
		mounted  string
		expected string
	}{
		{name: "mounted", mounted: volid, expected: "none"},
		{name: "not mounted", mounted: "local:iso/other.iso", expected: "local:iso/other.iso"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
			cluster.Node(zone).AddStorage("local", "dir", fakeapi.WithVolume(storage.Volume{VolID: volid}))
			cluster.Node(zone).AddVM(vmID, &qemu.Config{IDE: map[int]qemu.Drive{2: {File: tt.mounted, Media: "cdrom"}}})

			px := cluster.Client(t)

			cfg, err := px.Nodes(zone).Qemu().Config(t.Context(), vmID, nil)
			require.NoError(t, err)

			require.NoError(t, removeCloudInitISO(t.Context(), px, zone, vmID, cfg, volid))

			cfg, err = px.Nodes(zone).Qemu().Config(t.Context(), vmID, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, cfg.IDE[2].File)

			volumes, err := px.Nodes(zone).Storage().Content("local").List(t.Context(), nil)
			require.NoError(t, err)
			assert.Empty(t, volumes, "the ISO is deleted")
		})
	}
}
