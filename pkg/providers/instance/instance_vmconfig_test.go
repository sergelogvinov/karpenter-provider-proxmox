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
	"fmt"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/vmconfig"
)

// roundTripSettings is one socket, 8 cores with 2 threads, NUMA node 0 on cores 0-3 and node 1 on cores 4-7.
var roundTripSettings = settings.NodeSettings{
	NumSockets:      1,
	NumThreads:      2,
	NumUncoreCaches: 2,
	NUMANodes: settings.NUMANodes{
		0: {CPUs: "0-3,8-11", MemSize: 16 << 30},
		1: {CPUs: "4-7,12-15", MemSize: 16 << 30},
	},
	ReservedMemory: 1 << 30,
}

func newRoundTripZone(t *testing.T, policy zonecapacity.Policy) *resourcemanager.ZoneCapacity {
	t.Helper()

	zone, err := resourcemanager.NewZoneCapacity(zonecapacity.Host{
		Topology:       lo.Must(topology.DiscoverFromSettings(&roundTripSettings)),
		ReservedMemory: roundTripSettings.ReservedMemory,
	}, policy)
	require.NoError(t, err)

	return zone
}

// launchedConfig is the config a launch leaves on a clone of template: the clone overrides over
// the template, and the description with the reservation's tokens, as the launch writes them.
func launchedConfig(template qemu.Config, update *qemu.Config, tokens []string) *qemu.Config {
	cfg := template
	cfg.Cores = update.Cores
	cfg.Memory = update.Memory
	cfg.Affinity = update.Affinity
	cfg.NUMA = update.NUMA
	cfg.Description = strings.Join(append([]string{"Karpenter managed instance"}, tokens...), ", ")

	return &cfg
}

// numaMemory is the memory a reservation binds to each host NUMA node, in bytes; nil without a binding.
func numaMemory(reserved resources.VMResources) map[int]uint64 {
	if len(reserved.NUMANodes) == 0 {
		return nil
	}

	mem := make(map[int]uint64, len(reserved.NUMANodes))
	for node, state := range reserved.NUMANodes {
		mem[node] = state.Memory * zonecapacity.MiB
	}

	return mem
}

// TestLaunchedVMReadsAsReserved checks that a launched VM, read back from its config, holds in
// a zone exactly what its reservation held: the zone keeps a pending reservation as that VM.
func TestLaunchedVMReadsAsReserved(t *testing.T) {
	t.Parallel()

	static := zonecapacity.Policy{Kind: zonecapacity.PolicyStatic}
	shared := zonecapacity.Policy{Kind: zonecapacity.PolicySimple}

	slot := func(t *testing.T) *zonecapacity.SlotShape {
		t.Helper()

		for _, offer := range newRoundTripZone(t, static).Slots() {
			if offer.Shape.Fraction == 2 {
				return &offer.Shape
			}
		}

		t.Fatal("no half NUMA slot")

		return nil
	}

	for _, tc := range []struct {
		name   string
		policy zonecapacity.Policy
		req    func(t *testing.T) resources.VMResources
	}{
		{
			name:   "shared",
			policy: shared,
			req:    func(*testing.T) resources.VMResources { return resources.VMResources{CPUs: 4, Memory: 4 << 30} },
		},
		{
			name:   "pinned",
			policy: static,
			req:    func(*testing.T) resources.VMResources { return resources.VMResources{CPUs: 10, Memory: 10 << 30} },
		},
		{
			name:   "numa slot",
			policy: static,
			req: func(t *testing.T) resources.VMResources {
				s := slot(t)

				return resources.VMResources{CPUs: s.CPUs, Memory: s.Memory, Slot: s}
			},
		},
		{
			name:   "cpu share",
			policy: static,
			req: func(*testing.T) resources.VMResources {
				return resources.VMResources{CPUs: 2, Memory: 2 << 30, CPUShare: 2}
			},
		},
	} {
		for _, root := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/root=%t", tc.name, root), func(t *testing.T) {
				t.Parallel()

				reserved := tc.req(t)
				reserved.ID = 100

				reserving := newRoundTripZone(t, tc.policy)
				require.NoError(t, reserving.Reserve(&reserved))

				update := cloneConfigOverrides(cloneVMParams{resources: &reserved, root: root}, reserved.ID, &qemu.Config{})
				cfg := launchedConfig(qemu.Config{Sockets: new(1), Cores: new(1)}, update, reserved.DescriptionTokens)

				vm, err := vmconfig.VM(reserved.ID, cfg)
				require.NoError(t, err)
				require.Equal(t, reserved.CPUs, vm.CPUs)
				require.Equal(t, reserved.Memory, vm.Memory)
				require.Equal(t, numaMemory(reserved), vm.NUMAMemory, "the launched VM binds the reserved NUMA memory")

				// Only root writes the affinity option; the description token carries it either way.
				if reserved.CPUSet.IsEmpty() {
					require.Empty(t, vm.Affinity)
				} else {
					require.Contains(t, strings.Split(vm.Description, ", "), "affinity="+reserved.CPUSet.String())

					if root {
						require.Equal(t, reserved.CPUSet.String(), vm.Affinity)
					} else {
						require.Empty(t, vm.Affinity)
					}
				}

				tracking := newRoundTripZone(t, tc.policy)
				require.NoError(t, tracking.Track(vm))

				require.Equal(t, reserving.Available(), tracking.Available(), "the launched VM holds what was reserved")
				require.Equal(t, reserving.Slots(), tracking.Slots(), "the launched VM holds the reserved NUMA slots")

				// A further reservation lands on the same host CPUs beside either.
				next := resources.VMResources{ID: 101, CPUs: 2, Memory: 2 << 30}
				nextTracked := next

				require.NoError(t, reserving.Reserve(&next))
				require.NoError(t, tracking.Reserve(&nextTracked))
				require.Equal(t, next.CPUSet, nextTracked.CPUSet, "the launched VM holds the reserved host CPUs")
			})
		}
	}
}
