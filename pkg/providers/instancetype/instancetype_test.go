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

package instancetype_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	region = "region-1"
	zone   = "pve-1"
)

// nodeSettings describes the zone's host: 8 CPUs and 16GiB on one NUMA node.
const nodeSettings = `{"region-1": {"pve-1": {"nodes": {"0": {"cpus": "0-7", "memsize": 17179869184}}}}}`

// instanceTypes are a dedicated type and CPU share types of the same memory.
const instanceTypes = `[
  {"name": "c1.1VCPU-4GB", "capacity": {"cpu": "1", "memory": "4Gi"}},
  {"name": "e2.2VCPU-4GB", "cpushare": 2, "capacity": {"cpu": "2", "memory": "4Gi"}},
  {"name": "e4.4VCPU-4GB", "cpushare": 4, "capacity": {"cpu": "4", "memory": "4Gi"}}
]`

// newProvider returns an instance type provider with the types of file, its offerings
// updated for one synced zone under the node policy.
func newProvider(t *testing.T, policy, file string) (*instancetype.DefaultProvider, error) {
	t.Helper()

	dir := t.TempDir()
	settingsFile := filepath.Join(dir, "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, []byte(nodeSettings), 0o600))

	typesFile := filepath.Join(dir, "instancetypes.json")
	require.NoError(t, os.WriteFile(typesFile, []byte(file), 0o600))

	ctx := options.ToContext(context.Background(), &options.Options{
		NodePolicy:            policy,
		CPUOvercommitRatio:    1,
		NodeSettingFilePath:   settingsFile,
		InstanceTypesFilePath: typesFile,
	})

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	cluster.Node(zone).SetResources(8, int64(16<<30))

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{
		Region:      region,
		URL:         "https://127.0.0.1:8006/api2/json",
		TokenID:     "user@pve!token",
		TokenSecret: "secret",
	}})
	require.NoError(t, err)
	pool.Set(region, cluster.Client(t))

	capacity := cloudcapacity.NewProvider(ctx, pool)
	require.NoError(t, capacity.SyncNodeCapacity(ctx))

	p := instancetype.NewDefaultProvider(ctx, capacity)
	if err := p.UpdateInstanceTypes(ctx); err != nil {
		return nil, err
	}

	require.NoError(t, p.UpdateInstanceTypeOfferings(ctx))

	return p, nil
}

func offerings(t *testing.T, p *instancetype.DefaultProvider, name string) cloudprovider.Offerings {
	t.Helper()

	it, err := p.Get(context.Background(), name)
	require.NoError(t, err)

	return it.Offerings
}

func TestLoaderValidatesCPUShare(t *testing.T) {
	t.Parallel()

	for _, share := range []int{0, 2, 3, 4} {
		_, err := newProvider(t, "simple", fmt.Sprintf(`[{"name": "e.2VCPU-4GB", "cpushare": %d, "capacity": {"cpu": "2", "memory": "4Gi"}}]`, share))
		require.NoError(t, err, "cpushare %d", share)
	}

	for _, share := range []int{-1, 1, 5, 8} {
		_, err := newProvider(t, "simple", fmt.Sprintf(`[{"name": "e.2VCPU-4GB", "cpushare": %d, "capacity": {"cpu": "2", "memory": "4Gi"}}]`, share))
		require.ErrorContains(t, err, "cpushare", "cpushare %d", share)
	}
}

// TestCPUSharePrice checks that a CPU share type costs vCPU/N CPUs and its full memory.
func TestCPUSharePrice(t *testing.T) {
	t.Parallel()

	p, err := newProvider(t, "simple", instanceTypes)
	require.NoError(t, err)

	dedicated := offerings(t, p, "c1.1VCPU-4GB")
	require.Len(t, dedicated, 1)

	for _, name := range []string{"e2.2VCPU-4GB", "e4.4VCPU-4GB"} {
		share := offerings(t, p, name)
		require.Len(t, share, 1, name)
		require.InDelta(t, dedicated[0].Price, share[0].Price, 1e-9, "%s costs as much as one dedicated vCPU", name)
	}
}

// TestCPUShareOfferings checks that CPU share types are offered under static and simple, and not under
// CPU overcommit.
func TestCPUShareOfferings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		policy  string
		offered bool
	}{
		{policy: "static", offered: true},
		{policy: "simple", offered: true},
		{policy: "simple-with-cpu-overcommit"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			t.Parallel()

			p, err := newProvider(t, tc.policy, instanceTypes)
			require.NoError(t, err)

			require.Len(t, offerings(t, p, "c1.1VCPU-4GB").Available(), 1)

			if tc.offered {
				require.Len(t, offerings(t, p, "e2.2VCPU-4GB").Available(), 1)
			} else {
				require.Empty(t, offerings(t, p, "e2.2VCPU-4GB"))
			}
		})
	}
}
