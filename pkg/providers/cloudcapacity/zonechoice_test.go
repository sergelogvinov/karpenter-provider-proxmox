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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
)

// twoZones are two 8-CPU, 16GiB zones on one NUMA node each, 15GiB of each allocatable.
const twoZones = `{"region-1": {
	"pve-1": {"nodes": {"0": {"cpus": "0-7", "memsize": 17179869184}}},
	"pve-2": {"nodes": {"0": {"cpus": "0-7", "memsize": 17179869184}}}
}}`

// newZonesProvider returns a synced provider for the zones of the node settings file,
// each a host with cpus CPUs and 16GiB.
func newZonesProvider(t *testing.T, policy, nodeSettings string, cpus map[string]int) (context.Context, *cloudcapacity.DefaultProvider) {
	t.Helper()

	settingsFile := filepath.Join(t.TempDir(), "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, []byte(nodeSettings), 0o600))

	ctx := options.ToContext(context.Background(), &options.Options{
		NodePolicy:          policy,
		CPUOvercommitRatio:  1,
		NodeSettingFilePath: settingsFile,
	})

	names := make([]string, 0, len(cpus))
	for name := range cpus {
		names = append(names, name)
	}

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(names...))
	for name, n := range cpus {
		cluster.Node(name).SetResources(n, int64(16*gib))
	}

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

	return ctx, p
}

func newTwoZonesProvider(t *testing.T) (context.Context, *cloudcapacity.DefaultProvider) {
	t.Helper()

	return newZonesProvider(t, "simple", twoZones, map[string]int{"pve-1": 8, "pve-2": 8})
}

func strategy(zoneBalance string) *v1alpha1.PlacementStrategy {
	return &v1alpha1.PlacementStrategy{ZoneBalance: zoneBalance}
}

var bothZones = []string{"pve-1", "pve-2"}

func TestChooseZone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		strategy *v1alpha1.PlacementStrategy
		want     string
	}{
		{name: "balanced takes the zone with the most left over", strategy: strategy(v1alpha1.PlacementStrategyBalanced), want: "pve-2"},
		{name: "no strategy is balanced", strategy: nil, want: "pve-2"},
		{name: "best fit takes the zone with the least left over", strategy: strategy(v1alpha1.PlacementStrategyBestFit), want: "pve-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, p := newTwoZonesProvider(t)
			_, err := p.Reserve(ctx, region, "pve-1", resources.VMResources{ID: 100, CPUs: 4, Memory: 4 * gib})
			require.NoError(t, err)

			req := resources.VMResources{ID: 101, CPUs: 2, Memory: gib}
			zone, reserved, err := p.ChooseZone(ctx, region, bothZones, tc.strategy, req)
			require.NoError(t, err)
			require.Equal(t, tc.want, zone)
			require.Equal(t, req, reserved)

			_, err = p.Reserve(ctx, region, zone, req)
			require.ErrorContains(t, err, "already reserved", "the VM is reserved in the chosen zone")
		})
	}
}

func TestChooseZoneCountsPendingReservations(t *testing.T) {
	t.Parallel()

	ctx, p := newTwoZonesProvider(t)

	// A burst of launches before any sync spreads over both zones.
	zones := []string{}

	for id := 100; id < 104; id++ {
		zone, _, err := p.ChooseZone(ctx, region, bothZones, nil, resources.VMResources{ID: id, CPUs: 2, Memory: gib})
		require.NoError(t, err)

		zones = append(zones, zone)
	}

	require.Equal(t, []string{"pve-1", "pve-2", "pve-1", "pve-2"}, zones)
}

func TestChooseZoneSkipsZonesWithoutRoom(t *testing.T) {
	t.Parallel()

	for _, zoneBalance := range []string{v1alpha1.PlacementStrategyBestFit, v1alpha1.PlacementStrategyBalanced, v1alpha1.PlacementStrategyAvailabilityFirst} {
		t.Run(zoneBalance, func(t *testing.T) {
			t.Parallel()

			ctx, p := newTwoZonesProvider(t)
			_, err := p.Reserve(ctx, region, "pve-1", resources.VMResources{ID: 100, CPUs: 7, Memory: gib})
			require.NoError(t, err)

			zone, _, err := p.ChooseZone(ctx, region, bothZones, strategy(zoneBalance), resources.VMResources{ID: 101, CPUs: 2, Memory: gib})
			require.NoError(t, err)
			require.Equal(t, "pve-2", zone)
		})
	}
}

func TestChooseZoneOnlyFromCandidates(t *testing.T) {
	t.Parallel()

	ctx, p := newTwoZonesProvider(t)
	_, err := p.Reserve(ctx, region, "pve-1", resources.VMResources{ID: 100, CPUs: 4, Memory: 4 * gib})
	require.NoError(t, err)

	zone, _, err := p.ChooseZone(ctx, region, []string{"pve-1", "pve-3"}, nil, resources.VMResources{ID: 101, CPUs: 2, Memory: gib})
	require.NoError(t, err)
	require.Equal(t, "pve-1", zone, "the emptier pve-2 is not a candidate, and the unknown pve-3 is skipped")
}

func TestChooseZoneWithoutRoom(t *testing.T) {
	t.Parallel()

	ctx, p := newTwoZonesProvider(t)

	_, _, err := p.ChooseZone(ctx, region, bothZones, nil, resources.VMResources{ID: 100, CPUs: 9, Memory: gib})
	require.ErrorIs(t, err, cloudcapacity.ErrNoZoneFits)

	require.ErrorContains(t, err, "zone pve-1: ", "the zones' reasons are kept")

	_, _, err = p.ChooseZone(ctx, region, nil, nil, resources.VMResources{ID: 100, CPUs: 1, Memory: gib})
	require.ErrorIs(t, err, cloudcapacity.ErrNoZoneFits, "no candidates")
	require.EqualError(t, err, "no zone has room for the VM in region region-1")

	require.True(t, fits(p, 8, 15*gib), "nothing was reserved")
}

func TestChooseZonePropagatesInvalidRequests(t *testing.T) {
	t.Parallel()

	ctx, p := newTwoZonesProvider(t)
	_, err := p.Reserve(ctx, region, "pve-2", resources.VMResources{ID: 100, CPUs: 1, Memory: gib})
	require.NoError(t, err)

	_, _, err = p.ChooseZone(ctx, region, bothZones, nil, resources.VMResources{ID: 100, CPUs: 1, Memory: gib})
	require.ErrorContains(t, err, "zone pve-2: resources for VM 100 are already reserved")
	require.NotErrorIs(t, err, cloudcapacity.ErrNoZoneFits, "a reused VM ID is not a lack of room")

	_, _, err = p.ChooseZone(ctx, region, bothZones, nil, resources.VMResources{ID: 101, Memory: gib})
	require.Error(t, err)
	require.NotErrorIs(t, err, cloudcapacity.ErrNoZoneFits, "a request without CPUs is not a lack of room")

	require.True(t, fits(p, 8, 15*gib), "nothing was reserved in pve-1")
}

// unevenNUMAZones are pve-1, 10 CPUs on two NUMA nodes of 5, and pve-2, 8 CPUs on one NUMA node.
const unevenNUMAZones = `{"region-1": {
	"pve-1": {"nodes": {"0": {"cpus": "0-4", "memsize": 8589934592}, "1": {"cpus": "5-9", "memsize": 8589934592}}},
	"pve-2": {"nodes": {"0": {"cpus": "0-7", "memsize": 17179869184}}}
}}`

func TestChooseZonePrefersOneNUMANode(t *testing.T) {
	t.Parallel()

	for _, zoneBalance := range []string{v1alpha1.PlacementStrategyBestFit, v1alpha1.PlacementStrategyBalanced, v1alpha1.PlacementStrategyAvailabilityFirst} {
		t.Run(zoneBalance, func(t *testing.T) {
			t.Parallel()

			ctx, p := newZonesProvider(t, "static", unevenNUMAZones, map[string]int{"pve-1": 10, "pve-2": 8})

			// 6 CPUs span both NUMA nodes of pve-1, which would keep 4 of 10 CPUs free,
			// but stay on the one node of pve-2, which keeps 2 of 8.
			zone, reserved, err := p.ChooseZone(ctx, region, bothZones, strategy(zoneBalance), resources.VMResources{ID: 100, CPUs: 6, Memory: 4 * gib})
			require.NoError(t, err)
			require.Equal(t, "pve-2", zone)
			require.Len(t, reserved.NUMANodes, 1)
		})
	}
}

func TestFitInZoneAgreesWithReserve(t *testing.T) {
	t.Parallel()

	ctx, p := newZonesProvider(t, "static", unevenNUMAZones, map[string]int{"pve-1": 10})

	// Each NUMA node of pve-1 keeps 3GiB free, the zone 5GiB.
	for id := 100; id < 102; id++ {
		_, err := p.Reserve(ctx, region, "pve-1", resources.VMResources{ID: id, CPUs: 1, Memory: 5 * gib})
		require.NoError(t, err)
	}

	request := func(memory uint64) resources.VMResources {
		return resources.VMResources{CPUs: 1, Memory: memory}
	}

	require.NoError(t, p.FitInZone(region, "pve-1", request(3*gib)))
	require.Error(t, p.FitInZone(region, "pve-1", request(4*gib)), "one CPU cannot take 4GiB from a NUMA node with 3GiB free")

	_, err := p.Reserve(ctx, region, "pve-1", resources.VMResources{ID: 102, CPUs: 1, Memory: 4 * gib})
	require.Error(t, err)
}
