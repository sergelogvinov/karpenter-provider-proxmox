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

package nodecapacity_test

import (
	"testing"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/nodecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

const (
	gib = 1024 * 1024 * 1024
	mib = 1024 * 1024
)

func pages1G(n uint64) []info.HugePagesInfo {
	return []info.HugePagesInfo{{PageSize: nodecapacity.HugepageSize1G, NumPages: n}}
}

func pages2M(n uint64) []info.HugePagesInfo {
	return []info.HugePagesInfo{{PageSize: nodecapacity.HugepageSize2M, NumPages: n}}
}

// dualNodeTopology is a 2-socket, 2-NUMA-node, SMT topology with one uncore cache
// per socket: node 0 = {0,1,4,5}, node 1 = {2,3,6,7} (0/4, 1/5, 2/6, 3/7 are SMT
// sibling pairs).
func dualNodeTopology() *topology.Topology {
	return &topology.Topology{ //nolint:modernize // MemTopology is a second embedded field; the suggested elision does not compile
		NumCPUs:        8,
		NumCores:       4,
		NumSockets:     2,
		NumNUMANodes:   2,
		NumUncoreCache: 2,
		TotalMemory:    16 << 30,
		NUMANodes:      map[int]uint64{0: 8 << 30, 1: 8 << 30},
		CPUDetails: topology.CPUDetails{
			0: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			1: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			2: {CoreID: 2, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
			3: {CoreID: 3, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
			4: {CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			5: {CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
			6: {CoreID: 2, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
			7: {CoreID: 3, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
		},
	}
}

// machine is a cadvisor host with Linux CPU numbering: physical cores first,
// numbered socket by socket, then their SMT siblings, each socket one NUMA node and
// each run of coresPerCache cores one uncore cache.
func machine(sockets, cachesPerSocket, coresPerCache, threads int) *info.MachineInfo {
	coresPerSocket := cachesPerSocket * coresPerCache
	numCores := sockets * coresPerSocket

	m := &info.MachineInfo{
		NumCores:         numCores * threads,
		NumPhysicalCores: numCores,
		NumSockets:       sockets,
		MemoryCapacity:   uint64(sockets) * 64 * gib,
	}

	for s := range sockets {
		node := info.Node{Id: s, Memory: 64 * gib}

		for c := s * coresPerSocket; c < (s+1)*coresPerSocket; c++ {
			core := info.Core{
				Id:           c,
				SocketID:     s,
				UncoreCaches: []info.Cache{{Id: c / coresPerCache, Level: 3}},
			}

			for t := range threads {
				core.Threads = append(core.Threads, c+t*numCores)
			}

			node.Cores = append(node.Cores, core)
		}

		m.Topology = append(m.Topology, node)
	}

	return m
}

// TestRoundTrip checks that what the scheduler encodes from a host is what the
// controller decodes of its zone.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serverInfo *info.MachineInfo
		expected   nodecapacity.Guest
	}{
		{
			name:       "host without NUMA topology",
			serverInfo: &info.MachineInfo{NumCores: 8, MemoryCapacity: 16 * gib},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{},
				CPUs:      cpuset.New(),
				Memory:    16 * gib,
			},
		},
		{
			name: "two NUMA nodes without a pool take the host reserve off each",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib},
					{Id: 1, Memory: 32 * gib},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 31 * gib},
					1: {CPUs: "2-3,6-7", MemSize: 31 * gib},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				Memory:       62 * gib,
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 2,
			},
		},
		{
			name: "two NUMA nodes with a 1G pool report the pool",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(24),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages1G(16)},
					{Id: 1, Memory: 32 * gib, HugePages: pages1G(8)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 16 * gib},
					1: {CPUs: "2-3,6-7", MemSize: 8 * gib},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				Memory:       62 * gib,
				HugePageSize: gib,
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 2,
			},
		},
		{
			name: "a node without pages keeps its CPUs",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(16),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages1G(16)},
					{Id: 1, Memory: 32 * gib},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 16 * gib},
					1: {CPUs: "2-3,6-7"},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				Memory:       62 * gib,
				HugePageSize: gib,
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 2,
			},
		},
		{
			name: "a NUMA node without CPUs is skipped",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 96 * gib,
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib},
					{Id: 1, Memory: 32 * gib},
					{Id: 2, Memory: 32 * gib},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 31 * gib},
					1: {CPUs: "2-3,6-7", MemSize: 31 * gib},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				Memory:       62 * gib,
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 2,
			},
		},
		{
			name: "both page sizes report the larger pool",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      append(pages1G(2), pages2M(2048)...),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: append(pages1G(2), pages2M(1024)...)},
					{Id: 1, Memory: 32 * gib, HugePages: pages2M(1024)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 2 * gib},
					1: {CPUs: "2-3,6-7", MemSize: 2 * gib},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				Memory:       62 * gib,
				HugePageSize: 2 * mib,
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := nodecapacity.Encode(logr.Discard(), tt.serverInfo, dualNodeTopology())
			assert.True(t, nodecapacity.IsGuest(cfg.Name, *cfg.Tags))

			guest, err := nodecapacity.Decode(cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, guest)
		})
	}
}

// TestCPUShape checks that the controller rebuilds, from the node-capacity guest,
// the CPU topology the scheduler saw through cadvisor.
func TestCPUShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serverInfo *info.MachineInfo
		cores      int
		expected   nodecapacity.Guest
	}{
		{
			name:       "2-socket EPYC with SMT and 24 uncore caches",
			serverInfo: machine(2, 12, 4, 2),
			cores:      96,
			expected:   nodecapacity.Guest{Sockets: 2, Threads: 2, UncoreCaches: 24},
		},
		{
			name:       "2-socket Intel without SMT",
			serverInfo: machine(2, 1, 16, 1),
			cores:      16,
			expected:   nodecapacity.Guest{Sockets: 2, Threads: 1, UncoreCaches: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tp, err := topology.DiscoverCadvisor(logr.Discard(), tt.serverInfo)
			require.NoError(t, err)

			cfg := nodecapacity.Encode(logr.Discard(), tt.serverInfo, tp)
			assert.Equal(t, tt.expected.Sockets, *cfg.Sockets)
			assert.Equal(t, tt.cores, *cfg.Cores)

			guest, err := nodecapacity.Decode(cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.expected.Sockets, guest.Sockets)
			assert.Equal(t, tt.expected.Threads, guest.Threads)
			assert.Equal(t, tt.expected.UncoreCaches, guest.UncoreCaches)

			decoded, err := topology.DiscoverFromSettings(&settings.NodeSettings{
				NumSockets:      guest.Sockets,
				NumThreads:      guest.Threads,
				NumUncoreCaches: guest.UncoreCaches,
				NUMANodes:       guest.NUMANodes,
			})
			require.NoError(t, err)
			assert.Equal(t, tp.NumUncoreCache, decoded.NumUncoreCache)
			assert.Equal(t, tp.CPUsPerCore(), decoded.CPUsPerCore())
			assert.Equal(t, tp.CPUDetails, decoded.CPUDetails)
		})
	}
}

func TestEncodeHugepages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serverInfo *info.MachineInfo
		hugepages  *string
		memoryMB   int
		numaMB     map[int]int
	}{
		{
			name: "no pool keeps ordinary memory less the host reserve",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib},
					{Id: 1, Memory: 32 * gib},
				},
			},
			memoryMB: 2 * 31 * 1024,
			numaMB:   map[int]int{0: 31 * 1024, 1: 31 * 1024},
		},
		{
			name: "1G pool reports each node's pages in NUMA memory only",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(24),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages1G(16)},
					{Id: 1, Memory: 32 * gib, HugePages: pages1G(8)},
				},
			},
			hugepages: new("1024"),
			memoryMB:  2 * 31 * 1024,
			numaMB:    map[int]int{0: 16 * 1024, 1: 8 * 1024},
		},
		{
			name: "2M pool reports each node's pages",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages2M(1536),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages2M(1024)},
					{Id: 1, Memory: 32 * gib, HugePages: pages2M(512)},
				},
			},
			hugepages: new("2"),
			memoryMB:  2 * 31 * 1024,
			numaMB:    map[int]int{0: 2 * 1024, 1: 1024},
		},
		{
			name: "both sizes use the larger pool",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      append(pages1G(2), pages2M(2048)...),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: append(pages1G(2), pages2M(1024)...)},
					{Id: 1, Memory: 32 * gib, HugePages: pages2M(1024)},
				},
			},
			hugepages: new("2"),
			memoryMB:  2 * 31 * 1024,
			numaMB:    map[int]int{0: 2 * 1024, 1: 2 * 1024},
		},
		{
			name: "node without pages reports 0 and keeps its CPUs",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(16),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages1G(16)},
					{Id: 1, Memory: 32 * gib},
				},
			},
			hugepages: new("1024"),
			memoryMB:  2 * 31 * 1024,
			numaMB:    map[int]int{0: 16 * 1024, 1: 0},
		},
		{
			name: "per-node pages are found without machine-wide counts",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib, HugePages: pages1G(4)},
					{Id: 1, Memory: 32 * gib, HugePages: pages1G(4)},
				},
			},
			hugepages: new("1024"),
			memoryMB:  2 * 31 * 1024,
			numaMB:    map[int]int{0: 4 * 1024, 1: 4 * 1024},
		},
		{
			name: "machine-wide pages the NUMA nodes do not report are no pool",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(8),
				Topology: []info.Node{
					{Id: 0, Memory: 32 * gib},
					{Id: 1, Memory: 32 * gib},
				},
			},
			memoryMB: 2 * 31 * 1024,
			numaMB:   map[int]int{0: 31 * 1024, 1: 31 * 1024},
		},
		{
			name: "no NUMA information keeps ordinary memory with the machine-wide pool",
			serverInfo: &info.MachineInfo{
				NumCores:       8,
				MemoryCapacity: 64 * gib,
				HugePages:      pages1G(8),
			},
			hugepages: new("1024"),
			memoryMB:  64 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := nodecapacity.Encode(logr.Discard(), tt.serverInfo, dualNodeTopology())

			assert.Equal(t, tt.hugepages, cfg.Hugepages)
			require.NotNil(t, cfg.Memory)
			assert.Equal(t, tt.memoryMB, *cfg.Memory.Current)

			numaMB := make(map[int]int, len(cfg.NUMA))
			for idx, n := range cfg.NUMA {
				require.NotNil(t, n.Memory)
				numaMB[idx] = *n.Memory
			}

			if tt.numaMB == nil {
				assert.Empty(t, cfg.NUMA)

				return
			}

			assert.Equal(t, tt.numaMB, numaMB)
			assert.Equal(t, []string{"0-3"}, cfg.NUMA[0].CPUIDs)
			assert.Equal(t, []string{"4-7"}, cfg.NUMA[1].CPUIDs)
		})
	}
}

func TestStaleOptions(t *testing.T) {
	t.Parallel()

	current := &qemu.Config{
		Hugepages: new("1024"),
		NUMA: map[int]qemu.NUMA{
			0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(8192)},
			1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"1"}, Memory: new(8192)},
		},
	}

	assert.Empty(t, nodecapacity.StaleOptions(current, current))
	assert.Equal(t, []string{"numa1"}, nodecapacity.StaleOptions(current, &qemu.Config{
		Hugepages: current.Hugepages,
		NUMA:      map[int]qemu.NUMA{0: current.NUMA[0]},
	}))
	assert.Equal(t, []string{"hugepages", "numa1"}, nodecapacity.StaleOptions(current, &qemu.Config{
		NUMA: map[int]qemu.NUMA{0: current.NUMA[0]},
	}))
}

func TestDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *qemu.Config
		expected nodecapacity.Guest
		error    string
	}{
		{
			name: "guest without NUMA nodes",
			cfg: &qemu.Config{
				Affinity: "0-3",
				Memory:   &qemu.Memory{Current: new(8192)},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{},
				CPUs:      cpuset.New(0, 1, 2, 3),
				Memory:    8192 * mib,
			},
		},
		{
			name: "NUMA nodes map guest CPU indexes to host CPUs",
			cfg: &qemu.Config{
				Affinity: "0-11,48-59,12-23,60-71,24-35,72-83,36-47,84-95",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-23"}, HostNodes: []string{"0"}, Memory: new(126976)},
					1: {CPUIDs: []string{"24-47"}, HostNodes: []string{"1"}, Memory: new(126976)},
					2: {CPUIDs: []string{"48-71"}, HostNodes: []string{"2"}, Memory: new(126976)},
					3: {CPUIDs: []string{"72-95"}, HostNodes: []string{"3"}, Memory: new(126976)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-11,48-59", MemSize: 126976 * mib},
					1: {CPUs: "12-23,60-71", MemSize: 126976 * mib},
					2: {CPUs: "24-35,72-83", MemSize: 126976 * mib},
					3: {CPUs: "36-47,84-95", MemSize: 126976 * mib},
				},
				CPUs: lo.Must(cpuset.Parse("0-95")),
			},
		},
		{
			name: "a NUMA node on another host node",
			cfg: &qemu.Config{
				Affinity: "0-1,8-9",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"1"}, Memory: new(8192)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					1: {CPUs: "0-1,8-9", MemSize: 8192 * mib},
				},
				CPUs: cpuset.New(0, 1, 8, 9),
			},
		},
		{
			name: "a NUMA node on several host nodes splits evenly",
			cfg: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0-1"}, Memory: new(8192)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"2-3"}, Memory: new(8192)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1", MemSize: 4096 * mib},
					1: {CPUs: "2-3", MemSize: 4096 * mib},
					2: {CPUs: "8-9", MemSize: 4096 * mib},
					3: {CPUs: "10-11", MemSize: 4096 * mib},
				},
				CPUs: cpuset.New(0, 1, 2, 3, 8, 9, 10, 11),
			},
		},
		{
			name: "NUMA nodes sharing host nodes add up",
			cfg: &qemu.Config{
				Affinity: "0-3,8-11",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0-1"}, Memory: new(8192)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"0-1"}, Memory: new(8192)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,8-9", MemSize: 8192 * mib},
					1: {CPUs: "2-3,10-11", MemSize: 8192 * mib},
				},
				CPUs: cpuset.New(0, 1, 2, 3, 8, 9, 10, 11),
			},
		},
		{
			name: "a hugepage pool names its page size in MiB",
			cfg: &qemu.Config{
				Affinity:  "0-1,4-5,2-3,6-7",
				Hugepages: new("1024"),
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(16384)},
					1: {CPUIDs: []string{"4-7"}, HostNodes: []string{"1"}, Memory: new(0)},
				},
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{
					0: {CPUs: "0-1,4-5", MemSize: 16384 * mib},
					1: {CPUs: "2-3,6-7"},
				},
				CPUs:         cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				HugePageSize: gib,
			},
		},
		{
			name: "a guest without CPU shape tokens has no CPU shape",
			cfg: &qemu.Config{
				Description: "Karpenter discovery service",
				Sockets:     new(1),
				Cores:       new(8),
				Affinity:    "0-7",
			},
			expected: nodecapacity.Guest{
				NUMANodes: settings.NUMANodes{},
				CPUs:      lo.Must(cpuset.Parse("0-7")),
			},
		},
		{
			name: "the description tokens carry the CPU shape",
			cfg: &qemu.Config{
				Description: "Karpenter discovery service, threads=2, uncorecaches=4",
				Sockets:     new(2),
				Cores:       new(8),
				Affinity:    "0-15",
			},
			expected: nodecapacity.Guest{
				NUMANodes:    settings.NUMANodes{},
				CPUs:         lo.Must(cpuset.Parse("0-15")),
				Sockets:      2,
				Threads:      2,
				UncoreCaches: 4,
			},
		},
		{
			name: "a token without a positive count is unknown",
			cfg: &qemu.Config{
				Description: "Karpenter discovery service, threads=x, uncorecaches=2",
				Sockets:     new(2),
				Cores:       new(8),
				Affinity:    "0-15",
			},
			expected: nodecapacity.Guest{
				NUMANodes:    settings.NUMANodes{},
				CPUs:         lo.Must(cpuset.Parse("0-15")),
				Sockets:      2,
				UncoreCaches: 2,
			},
		},
		{
			name:  "a bad affinity",
			cfg:   &qemu.Config{Affinity: "0-x"},
			error: `failed to parse CPU affinity part "0-x"`,
		},
		{
			name: "a CPU index past the affinity",
			cfg: &qemu.Config{
				Affinity: "0-1",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-3"}, HostNodes: []string{"0"}, Memory: new(8192)},
				},
			},
			error: "NUMA node 0 CPU index 2 exceeds affinity CPU list length 2",
		},
		{
			name: "CPUs that do not split across host nodes",
			cfg: &qemu.Config{
				Affinity: "0-2",
				NUMA: map[int]qemu.NUMA{
					0: {CPUIDs: []string{"0-2"}, HostNodes: []string{"0-1"}, Memory: new(8192)},
				},
			},
			error: "cannot evenly distribute 3 CPUs across 2 NUMA nodes for NUMA node numa0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			guest, err := nodecapacity.Decode(tt.cfg)
			if tt.error != "" {
				assert.ErrorContains(t, err, tt.error)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, guest)
		})
	}
}
