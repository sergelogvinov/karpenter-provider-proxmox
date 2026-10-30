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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

	return newProviderOn(t, policy, nodeSettings, file, nil, zone)
}

// newProviderOn is newProvider on the zones of region, described by the node settings file nodes,
// with the VMs setup adds to the cluster, if any, before the sync.
func newProviderOn(t *testing.T, policy, nodes, file string, setup func(*fakeapi.Cluster), zones ...string) (*instancetype.DefaultProvider, error) {
	t.Helper()

	f, err := newFixture(t, policy, nodes, file, setup, zones...)
	if err != nil {
		return nil, err
	}

	return f.p, nil
}

// fixture is an instance type provider on fake Proxmox zones, with the node settings file and the
// NodeClaims of the cluster it sees.
type fixture struct {
	t         *testing.T
	ctx       context.Context //nolint:containedctx
	nodesFile string
	typesFile string
	capacity  *cloudcapacity.DefaultProvider
	kube      client.Client
	p         *instancetype.DefaultProvider
	// listErr, when set, fails the provider's listing of the NodeClaims.
	listErr error
}

// newFixture is newProviderOn, keeping what a test needs to change the hosts or the NodeClaims later.
func newFixture(t *testing.T, policy, nodes, file string, setup func(*fakeapi.Cluster), zones ...string) (*fixture, error) {
	t.Helper()

	dir := t.TempDir()
	settingsFile := filepath.Join(dir, "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, []byte(nodes), 0o600))

	// An empty file is no instance types file: the provider keeps the predefined list.
	typesFile := ""
	if file != "" {
		typesFile = filepath.Join(dir, "instancetypes.json")
		require.NoError(t, os.WriteFile(typesFile, []byte(file), 0o600))
	}

	ctx := options.ToContext(context.Background(), &options.Options{
		NodePolicy:            policy,
		CPUOvercommitRatio:    1,
		NodeSettingFilePath:   settingsFile,
		InstanceTypesFilePath: typesFile,
	})

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zones...))
	for _, z := range zones {
		cluster.Node(z).SetResources(8, int64(16<<30))
	}

	if setup != nil {
		setup(cluster)
	}

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{
		Region:      region,
		URL:         "https://127.0.0.1:8006/api2/json",
		TokenID:     "user@pve!token",
		TokenSecret: "secret",
	}})
	require.NoError(t, err)
	pool.Set(region, cluster.Client(t))

	capacity := cloudcapacity.NewProvider(ctx, pool, nil)
	require.NoError(t, capacity.SyncNodeCapacity(ctx))

	f := &fixture{t: t, ctx: ctx, nodesFile: settingsFile, typesFile: typesFile, capacity: capacity}
	f.kube = fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if f.listErr != nil {
				return f.listErr
			}

			return c.List(ctx, list, opts...)
		},
	}).Build()

	f.p = instancetype.NewDefaultProvider(ctx, capacity, f.kube)
	if err := f.p.UpdateInstanceTypes(ctx); err != nil {
		return nil, err
	}

	require.NoError(t, f.p.UpdateInstanceTypeOfferings(ctx))

	return f, nil
}

// setHosts replaces the node settings file with nodes, then syncs the zones and refreshes the offerings.
func (f *fixture) setHosts(nodes string) {
	f.t.Helper()

	f.syncHosts(nodes)
	require.NoError(f.t, f.p.UpdateInstanceTypeOfferings(f.ctx))
}

// syncHosts replaces the node settings file with nodes and syncs the zones, without refreshing the offerings.
func (f *fixture) syncHosts(nodes string) {
	f.t.Helper()

	require.NoError(f.t, os.WriteFile(f.nodesFile, []byte(nodes), 0o600))
	require.NoError(f.t, f.capacity.SyncNodeCapacity(f.ctx))
}

// setTypes replaces the instance types file with file, then reloads the instance types and refreshes the offerings.
func (f *fixture) setTypes(file string) {
	f.t.Helper()

	require.NoError(f.t, os.WriteFile(f.typesFile, []byte(file), 0o600))
	require.NoError(f.t, f.p.UpdateInstanceTypes(f.ctx))
	require.NoError(f.t, f.p.UpdateInstanceTypeOfferings(f.ctx))
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
