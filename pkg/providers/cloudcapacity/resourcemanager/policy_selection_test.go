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

package resourcemanager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
)

// TestNodePolicySelection exercises policy dispatch using discovered Proxmox topology.
func TestNodePolicySelection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/nodes/test/status":
			fmt.Fprint(w, `{"data":{"cpuinfo":{"model":"Intel test","sockets":1,"cores":6,"cpus":12},"memory":{"total":34359738368}}}`)
		case "/cluster/resources":
			fmt.Fprint(w, `{"data":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := proxmoxrest.New(proxmoxrest.ClientConfig{BaseURL: server.URL, Token: "test!token", TokenSecret: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	for _, tc := range []struct {
		policy   string
		ratio    float64
		capacity int
		admit    bool
	}{
		{policy: "simple", ratio: 1, capacity: 12},
		{policy: "static", ratio: 1, capacity: 12},
		{policy: "simple-with-cpu-overcommit", ratio: 1, capacity: 12},
		{policy: "simple-with-cpu-overcommit", ratio: 1.5, capacity: 18, admit: true},
	} {
		t.Run(fmt.Sprintf("%s/%g", tc.policy, tc.ratio), func(t *testing.T) {
			t.Parallel()

			ctx := options.ToContext(context.Background(), &options.Options{NodePolicy: tc.policy, CPUOvercommitRatio: tc.ratio})
			capacity, err := ZoneCapacityFromCluster(ctx, client, "test", "test")
			require.NoError(t, err)
			require.Equal(t, tc.capacity, capacity.Available().CPUs)

			request := &resources.VMResources{ID: 1, CPUs: 16, Memory: 8 << 30}
			err = capacity.Reserve(request)
			if tc.admit {
				require.NoError(t, err)
				require.NoError(t, capacity.Track(zonecapacity.VM{ID: request.ID, CPUs: request.CPUs, Memory: request.Memory}))
				require.Equal(t, zonecapacity.Capacity{CPUs: 2, Memory: 23 << 30}, capacity.Available())
				capacity.Release(request.ID)
				require.Equal(t, 18, capacity.Available().CPUs)
			} else {
				require.ErrorContains(t, err, "not enough CPUs")
			}
		})
	}
}
