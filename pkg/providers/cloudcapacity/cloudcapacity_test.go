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

package cloudcapacity_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	region = "region-1"
	zone   = "pve-1"
	gib    = uint64(1 << 30)
)

// nodeSettings describes the zone's host: fakeapi reports a CPU model that topology
// discovery does not recognize, so the host layout comes from the node settings file.
const nodeSettings = `{"region-1": {"pve-1": {"nodes": {"0": {"cpus": "0-7", "memsize": 17179869184}}}}}`

// newProvider returns a synced provider for one 8-CPU, 16GiB zone, 15GiB of it allocatable.
func newProvider(t *testing.T) (context.Context, *fakeapi.Cluster, *cloudcapacity.DefaultProvider) {
	t.Helper()

	return newProviderWithPolicy(t, "simple")
}

// newProviderWithPolicy is newProvider with the given node CPU policy.
func newProviderWithPolicy(t *testing.T, policy string) (context.Context, *fakeapi.Cluster, *cloudcapacity.DefaultProvider) {
	t.Helper()

	settingsFile := filepath.Join(t.TempDir(), "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, []byte(nodeSettings), 0o600))

	ctx := options.ToContext(context.Background(), &options.Options{
		NodePolicy:          policy,
		CPUOvercommitRatio:  1,
		NodeSettingFilePath: settingsFile,
	})

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	cluster.Node(zone).SetResources(8, int64(16*gib))

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{
		Region:      region,
		URL:         "https://127.0.0.1:8006/api2/json",
		TokenID:     "user@pve!token",
		TokenSecret: "secret",
	}})
	require.NoError(t, err)
	pool.Set(region, cluster.Client(t))

	p := cloudcapacity.NewProvider(ctx, pool)
	require.NoError(t, p.SyncNodeCapacity(ctx))
	require.True(t, fits(p, 8, 15*gib), "empty zone fits 8 CPUs and 15GiB")

	return ctx, cluster, p
}

func reserve(ctx context.Context, p *cloudcapacity.DefaultProvider, req resources.VMResources) error {
	_, err := p.Reserve(ctx, region, zone, req)

	return err
}

func fits(p *cloudcapacity.DefaultProvider, cpus int64, memory uint64) bool {
	return p.FitInZone(region, zone, corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(cpus, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(memory), resource.BinarySI),
	})
}

func TestReserveReturnsSharedPlacement(t *testing.T) {
	t.Parallel()

	ctx, _, p := newProvider(t)

	req := resources.VMResources{ID: 100, CPUs: 2, Memory: gib}
	reserved, err := p.Reserve(ctx, region, zone, req)
	require.NoError(t, err)

	require.Equal(t, req, reserved, "shared placement gets no host CPUs or NUMA split")
	require.False(t, fits(p, 7, gib))
}

func TestReserveReturnsPinnedPlacement(t *testing.T) {
	t.Parallel()

	ctx, _, p := newProviderWithPolicy(t, "static")

	req := resources.VMResources{ID: 100, CPUs: 2, Memory: gib}
	reserved, err := p.Reserve(ctx, region, zone, req)
	require.NoError(t, err)

	require.Equal(t, 2, reserved.CPUSet.Size(), "pinned placement gets its own host CPUs: %v", reserved.CPUSet)
	require.NotEmpty(t, reserved.NUMANodes)
	require.True(t, req.CPUSet.IsEmpty(), "the request is not changed")
	require.Nil(t, req.NUMANodes)
}

func TestReserveFailsWithoutRoom(t *testing.T) {
	t.Parallel()

	ctx, _, p := newProvider(t)

	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 8, Memory: gib}))
	require.Error(t, reserve(ctx, p, resources.VMResources{ID: 101, CPUs: 1, Memory: gib}))

	require.NoError(t, p.Release(ctx, region, zone, 100))
	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 101, CPUs: 1, Memory: gib}))
}

func TestSyncKeepsPendingReservation(t *testing.T) {
	t.Parallel()

	ctx, _, p := newProvider(t)

	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 6, Memory: 8 * gib}))
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 2, 7*gib))
	require.False(t, fits(p, 3, gib), "the reservation still holds 6 CPUs after sync")

	require.NoError(t, p.Release(ctx, region, zone, 100))
	require.NoError(t, p.SyncNodeCapacity(ctx))
	require.True(t, fits(p, 8, 15*gib))
}

func TestSyncCountsStartedVMOnce(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)

	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 6, Memory: 8 * gib}))
	cluster.Node(zone).AddVM(100, &qemu.Config{Cores: new(6), Memory: &qemu.Memory{Current: new(8192)}}, fakeapi.WithStatus(qemu.VMStatusRunning))
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 2, 7*gib), "the running VM replaces its reservation instead of adding to it")
	require.False(t, fits(p, 3, gib))
}

func TestSyncIgnoresNodeCapacityGuest(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)

	tags := qemu.Tags{"karpenter"}
	cluster.Node(zone).AddVM(9000, &qemu.Config{
		Cores:  new(8),
		Memory: &qemu.Memory{Current: new(16384)},
		Tags:   &tags,
	}, fakeapi.WithStatus(qemu.VMStatusRunning))
	cluster.Node(zone).AddVM(100, &qemu.Config{
		Cores:       new(2),
		Memory:      &qemu.Memory{Current: new(2048)},
		Description: "Karpenter managed instance, affinity=0,4",
	}, fakeapi.WithStatus(qemu.VMStatusRunning))
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 6, 13*gib), "the node-capacity guest holds nothing")
	require.False(t, fits(p, 7, gib), "the workload's affinity token pins 2 CPUs")
}

func TestReservationDuringSyncSurvivesIt(t *testing.T) {
	t.Parallel()

	ctx, _, p := newProvider(t)

	cloudcapacity.SetAfterBuild(p, func() {
		require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 6, Memory: 8 * gib}))
	})
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.False(t, fits(p, 3, gib), "the reservation made between build and swap still holds 6 CPUs")
}

func TestReleaseDuringSyncStaysReleased(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)

	cluster.Node(zone).AddVM(100, &qemu.Config{Cores: new(6), Memory: &qemu.Memory{Current: new(8192)}}, fakeapi.WithStatus(qemu.VMStatusRunning))
	require.NoError(t, p.SyncNodeCapacity(ctx))
	require.False(t, fits(p, 3, gib))

	// The build lists VM 100 as running; it is deleted and released before the swap.
	cloudcapacity.SetAfterBuild(p, func() {
		require.NoError(t, p.Release(ctx, region, zone, 100))
	})
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 8, 15*gib), "the release is not undone by the swap")
}

func TestReleaseAndReserveSameIDDuringSync(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)

	cluster.Node(zone).AddVM(100, &qemu.Config{Cores: new(6), Memory: &qemu.Memory{Current: new(8192)}}, fakeapi.WithStatus(qemu.VMStatusRunning))
	require.NoError(t, p.SyncNodeCapacity(ctx))

	// VM 100 is released and its ID reused for a new 2-CPU reservation before the swap.
	cloudcapacity.SetAfterBuild(p, func() {
		require.NoError(t, p.Release(ctx, region, zone, 100))
		require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 2, Memory: gib}))
	})
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 6, 14*gib), "only the new reservation is counted")
	require.False(t, fits(p, 7, gib))
}

func TestOmittedZoneKeepsPendingReservation(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)
	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 6, Memory: 8 * gib}))

	cluster.FailNode(zone, fakeapi.FailureUnreachable)
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.NotContains(t, p.Zones(region), zone)
	require.False(t, fits(p, 1, gib), "an omitted zone is not available for placement")
	_, _, err := p.ChooseZone(ctx, region, []string{zone}, nil, resources.VMResources{ID: 101, CPUs: 1, Memory: gib})
	require.ErrorIs(t, err, cloudcapacity.ErrNoZoneFits, "an omitted zone is not a candidate for zone choice")
	require.Error(t, reserve(ctx, p, resources.VMResources{ID: 101, CPUs: 1, Memory: gib}))

	cluster.RecoverNode(zone)
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.Contains(t, p.Zones(region), zone)
	require.True(t, fits(p, 2, 7*gib))
	require.False(t, fits(p, 3, gib), "the reservation outlived the zone's absence")
}

func TestReleaseOnOmittedZone(t *testing.T) {
	t.Parallel()

	ctx, cluster, p := newProvider(t)
	require.NoError(t, reserve(ctx, p, resources.VMResources{ID: 100, CPUs: 6, Memory: 8 * gib}))

	cluster.FailNode(zone, fakeapi.FailureUnreachable)
	require.NoError(t, p.SyncNodeCapacity(ctx))
	require.NoError(t, p.Release(ctx, region, zone, 100))

	cluster.RecoverNode(zone)
	require.NoError(t, p.SyncNodeCapacity(ctx))

	require.True(t, fits(p, 8, 15*gib), "the release while omitted sticks")
}
