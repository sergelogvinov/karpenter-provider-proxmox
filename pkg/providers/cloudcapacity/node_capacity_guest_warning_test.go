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
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// noNodeCapacityGuestWarning is what the warning for a pinned zone without a node-capacity guest says.
const noNodeCapacityGuestWarning = "no node-capacity guest"

// TestSyncWarnsPinnedZoneWithoutNodeCapacityGuest checks that, without root, a capacity sync warns once for a zone
// under pinned placement that has no node-capacity guest: only the proxmox-scheduler pins its VMs.
func TestSyncWarnsPinnedZoneWithoutNodeCapacityGuest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		policy string
		root   bool
		guest  bool
		warns  bool
	}{
		{name: "pinned without guest", policy: "static", warns: true},
		{name: "pinned with guest", policy: "static", guest: true},
		{name: "pinned as root", policy: "static", root: true},
		{name: "shared", policy: "simple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				mu    sync.Mutex
				lines []string
			)

			logger := funcr.New(func(_, args string) {
				mu.Lock()
				defer mu.Unlock()

				lines = append(lines, args)
			}, funcr.Options{})

			settingsFile := filepath.Join(t.TempDir(), "nodes.json")
			require.NoError(t, os.WriteFile(settingsFile, []byte(nodeSettings), 0o600))

			ctx := log.IntoContext(options.ToContext(context.Background(), &options.Options{
				NodePolicy:          tc.policy,
				CPUOvercommitRatio:  1,
				NodeSettingFilePath: settingsFile,
			}), logger)

			cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
			cluster.Node(zone).SetResources(8, int64(16*gib))

			if tc.guest {
				tags := qemu.Tags{"karpenter"}
				cluster.Node(zone).AddVM(9000, &qemu.Config{Name: "node-capacity", Cores: new(1), Tags: &tags})
			}

			pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{Region: region, URL: "https://127.0.0.1:8006/api2/json"}})
			require.NoError(t, err)
			pool.Set(region, cluster.Client(t))

			p := cloudcapacity.NewProvider(ctx, pool, config.Privileges{region: tc.root})
			require.NoError(t, p.SyncNodeCapacity(ctx))
			require.NoError(t, p.SyncNodeCapacity(ctx))

			mu.Lock()
			defer mu.Unlock()

			warnings := 0

			for _, line := range lines {
				if strings.Contains(line, noNodeCapacityGuestWarning) {
					warnings++

					assert.Contains(t, line, `"zone"="`+zone+`"`)
				}
			}

			if tc.warns {
				assert.Equal(t, 1, warnings, "warned once across syncs: %v", lines)
			} else {
				assert.Zero(t, warnings, "no warning: %v", lines)
			}
		})
	}
}
