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

// Package nodecapacity is the node-capacity guest's config: the proxmox-scheduler
// encodes the host into it, and the controller decodes the zone's CPUs, NUMA nodes
// and hugepage pool back out of it. The name is the guest's, not zone capacity's.
//
// The host memory reserve is split between the two sides: the guest leaves 1GiB of
// each NUMA node to the host, and the controller reserves a further ReservedMemory,
// except in a hugepage zone, whose pool lies outside the host's own memory.
//
// The guest also carries the host's CPU shape. sockets is the host's sockets and
// cores its logical CPUs per socket, so that numaN can name every logical CPU.
// Proxmox has no option for threads, and none fits the uncore caches: numaN stops at
// numa7, fewer than the L3 caches of a 2-socket EPYC, and args needs root@pam; both
// travel as description tokens, `threads=N, uncorecaches=N`. The tokens also mark a
// guest whose sockets is the host's: an older guest has sockets=1 on any host.
package nodecapacity

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"
)

// Name and Tag together identify the node-capacity guest, which describes the host
// and is never a workload. Workload VMs often carry the `karpenter` tag too, so the
// tag alone is not enough.
const (
	Name = "node-capacity"
	Tag  = "karpenter"
)

// description is the node-capacity guest's description, followed by the CPU shape
// tokens when the guest carries the host's CPU shape.
const description = "Karpenter discovery service"

// The description tokens of the host's CPU shape: its threads per core and uncore
// caches of all sockets.
const (
	threadsToken      = "threads="
	uncoreCachesToken = "uncorecaches="
)

// IsGuest reports whether a guest of this name and tags is the node-capacity guest.
func IsGuest(name string, tags []string) bool {
	return name == Name && slices.Contains(tags, Tag)
}

// Hugepage sizes, in KiB, that Proxmox's hugepages option accepts.
const (
	HugepageSize2M = 2 * 1024
	HugepageSize1G = 1024 * 1024
)

// Encode builds the node-capacity guest's config for the host. Its memory is always
// the host's ordinary memory less the host reserve, 1GiB per NUMA node; on a host with
// a hugepage pool each NUMA node's memory is the node's pool instead. Without a NUMA
// topology from cadvisor the guest carries no CPU shape.
func Encode(logger logr.Logger, serverInfo *info.MachineInfo, tp *topology.Topology) *qemu.Config {
	totalCores := serverInfo.NumCores
	totalMemoryMB := int(serverInfo.MemoryCapacity / (1024 * 1024))
	pageSize := hugepageSize(logger, hugepagePools(serverInfo, tp))

	cfg := &qemu.Config{
		Name:        Name,
		Description: description,
		Cores:       &totalCores,
		Sockets:     new(1),
		CPU:         &qemu.CPU{Type: "host"},
		NUMAEnabled: new(true),
		OSType:      new("l26"),
		Tags:        &qemu.Tags{Tag},
	}

	if pageSize > 0 {
		cfg.Hugepages = new(strconv.FormatUint(pageSize/1024, 10))
	}

	if len(serverInfo.Topology) > 0 {
		affinity := make([]string, 0, len(serverInfo.Topology))
		numa := make(map[int]qemu.NUMA, len(serverInfo.Topology))

		cpush := 0
		totalMemoryMB = 0
		idx := 0

		for _, nodeInfo := range serverInfo.Topology {
			cpus := tp.CPUDetails.CPUsInNUMANodes(nodeInfo.Id)
			if cpus.Size() == 0 {
				continue
			}

			affinity = append(affinity, cpus.String())

			memoryInNode := int(nodeInfo.Memory / (1024 * 1024 * 1024))
			if memoryInNode > 1 {
				memoryInNode--
			}

			memoryInNode *= 1024
			totalMemoryMB += memoryInNode

			numaMemory := memoryInNode
			if pageSize > 0 {
				// A node without pages of this size reports 0, but keeps
				// its CPUs.
				numaMemory = poolMB(nodeInfo.HugePages, pageSize)
			}

			numa[idx] = qemu.NUMA{
				CPUIDs:    []string{fmt.Sprintf("%d-%d", cpush, cpush+cpus.Size()-1)},
				HostNodes: []string{strconv.Itoa(nodeInfo.Id)},
				Memory:    &numaMemory,
			}

			cpush += cpus.Size()
			idx++
		}

		cfg.Affinity = strings.Join(affinity, ",")
		cfg.NUMA = numa

		if tp.NumSockets > 0 && totalCores%tp.NumSockets == 0 {
			cfg.Sockets = new(tp.NumSockets)
			cfg.Cores = new(totalCores / tp.NumSockets)
			cfg.Description = fmt.Sprintf("%s, %s%d, %s%d", description,
				threadsToken, tp.CPUsPerCore(), uncoreCachesToken, tp.NumUncoreCache)
		}
	}

	cfg.Memory = &qemu.Memory{Current: &totalMemoryMB}

	return cfg
}

// StaleOptions returns the options the node-capacity guest has but desired no longer sets:
// an update never sends an option the desired config leaves unset, so a hugepage pool
// or a NUMA node the host no longer reports must be deleted explicitly. memory, numaX
// and hugepages are independent here: memory is the memory available to allocate,
// numaX each node's hugepage pool, and the guest is never started, so they need not
// agree.
func StaleOptions(current, desired *qemu.Config) []string {
	var stale []string

	if current.Hugepages != nil && desired.Hugepages == nil {
		stale = append(stale, "hugepages")
	}

	for idx := range current.NUMA {
		if _, ok := desired.NUMA[idx]; !ok {
			stale = append(stale, fmt.Sprintf("numa%d", idx))
		}
	}

	slices.Sort(stale)

	return stale
}

// Guest is what the node-capacity guest says of its zone.
type Guest struct {
	// NUMANodes are the host NUMA nodes, each with its host CPUs and memory; the
	// memory is the node's hugepage pool when HugePageSize is set.
	NUMANodes settings.NUMANodes
	// CPUs are the host CPUs the guest covers.
	CPUs cpuset.CPUSet
	// Memory is the guest's memory, in bytes.
	Memory uint64
	// HugePageSize is the page size, in bytes, of the zone's hugepage pool; 0 means none.
	HugePageSize uint64
	// Sockets, Threads and UncoreCaches are the host's CPU sockets, threads per
	// core and uncore caches of all sockets; 0 means unknown, as for a guest
	// without the CPU shape tokens, whose sockets is not the host's.
	Sockets      int
	Threads      int
	UncoreCaches int
}

// Decode reads the node-capacity guest's config back into what it says of its zone.
// Each numaN option maps guest CPU indexes, through the affinity, to host CPUs; a
// numaN bound to several host nodes splits its CPUs and memory evenly across them.
func Decode(cfg *qemu.Config) (Guest, error) {
	if cfg == nil {
		return Guest{}, fmt.Errorf("invalid input: config cannot be nil")
	}

	guest := Guest{
		NUMANodes: settings.NUMANodes{},
		CPUs:      cpuset.New(),
	}

	if cfg.Memory != nil && cfg.Memory.Current != nil && *cfg.Memory.Current > 0 {
		guest.Memory = uint64(*cfg.Memory.Current) * 1024 * 1024
	}

	// The guest names its hugepage pool's page size in MiB ("2" or "1024").
	if cfg.Hugepages != nil {
		if pageSizeMB, err := strconv.ParseUint(*cfg.Hugepages, 10, 64); err == nil {
			guest.HugePageSize = pageSizeMB * 1024 * 1024
		}
	}

	cpuList := []int{}

	for part := range strings.SplitSeq(cfg.Affinity, ",") {
		cpus, err := cpuset.Parse(part)
		if err != nil {
			return Guest{}, fmt.Errorf("failed to parse CPU affinity part %q: %w", part, err)
		}

		cpuList = append(cpuList, cpus.List()...)
		guest.CPUs = guest.CPUs.Union(cpus)
	}

	numaIdx := make([]int, 0, len(cfg.NUMA))
	for idx := range cfg.NUMA {
		numaIdx = append(numaIdx, idx)
	}

	sort.Ints(numaIdx)

	for _, idx := range numaIdx {
		n := cfg.NUMA[idx]

		// memory=0 is kept: the guest reports a NUMA node without hugepages
		// that way, and its CPUs still count.
		if n.Memory == nil || *n.Memory < 0 || len(n.CPUIDs) == 0 || len(n.HostNodes) == 0 {
			continue
		}

		cpuIDs, err := cpuset.Parse(strings.Join(n.CPUIDs, ","))
		if err != nil {
			return Guest{}, fmt.Errorf("failed to parse CPU IDs for NUMA node numa%d: %w", idx, err)
		}

		hostNuma, err := cpuset.Parse(strings.Join(n.HostNodes, ","))
		if err != nil {
			return Guest{}, fmt.Errorf("failed to parse Host Node Names for NUMA node numa%d: %w", idx, err)
		}

		ids := cpuIDs.List()

		numHostNodes := hostNuma.Size()
		if numHostNodes == 0 {
			return Guest{}, fmt.Errorf("NUMA host nodes set is empty for NUMA node numa%d", idx)
		}

		if numHostNodes > 1 && len(ids)%numHostNodes != 0 {
			return Guest{}, fmt.Errorf("cannot evenly distribute %d CPUs across %d NUMA nodes for NUMA node numa%d", len(ids), numHostNodes, idx)
		}

		nodeCPUs := len(ids) / numHostNodes

		for i, nodeID := range hostNuma.List() {
			old := guest.NUMANodes[nodeID]

			cpus, err := cpuset.Parse(old.CPUs)
			if err != nil {
				return Guest{}, fmt.Errorf("failed to parse existing CPUs for NUMA node %d: %w", nodeID, err)
			}

			for _, c := range ids[i*nodeCPUs : (i+1)*nodeCPUs] {
				if c >= len(cpuList) {
					return Guest{}, fmt.Errorf("NUMA node %d CPU index %d exceeds affinity CPU list length %d", nodeID, c, len(cpuList))
				}

				cpus = cpus.Union(cpuset.New(cpuList[c]))
			}

			guest.NUMANodes[nodeID] = settings.NUMAInfo{
				CPUs:    cpus.String(),
				MemSize: old.MemSize + uint64(*n.Memory)/uint64(numHostNodes)*1024*1024, //nolint:gosec // *n.Memory is not negative
			}
		}
	}

	guest.Threads = descriptionCount(cfg.Description, threadsToken)
	guest.UncoreCaches = descriptionCount(cfg.Description, uncoreCachesToken)

	if (guest.Threads > 0 || guest.UncoreCaches > 0) && cfg.Sockets != nil && *cfg.Sockets > 0 {
		guest.Sockets = *cfg.Sockets
	}

	return guest, nil
}

// descriptionCount returns the count of the description's token, or 0 when it has
// no such token or its value is not a positive number.
func descriptionCount(desc, token string) int {
	for part := range strings.SplitSeq(desc, ",") {
		value, ok := strings.CutPrefix(strings.TrimSpace(part), token)
		if !ok {
			continue
		}

		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			return n
		}

		return 0
	}

	return 0
}

// hugepagePools returns the hugepage counts the node-capacity guest is built from:
// those of the NUMA nodes it reports when cadvisor knows the topology, the
// machine-wide ones otherwise, so the page size chosen and the memory reported come
// from the same pages.
func hugepagePools(serverInfo *info.MachineInfo, tp *topology.Topology) []info.HugePagesInfo {
	if len(serverInfo.Topology) == 0 || tp == nil {
		return serverInfo.HugePages
	}

	var pools []info.HugePagesInfo

	for _, nodeInfo := range serverInfo.Topology {
		if tp.CPUDetails.CPUsInNUMANodes(nodeInfo.Id).Size() > 0 {
			pools = append(pools, nodeInfo.HugePages...)
		}
	}

	return pools
}

// hugepageSize returns the page size (KiB) of the host's hugepage pool, or 0 when it
// has none. Hosts are expected to preallocate one size only; if they have both, the
// size holding more memory wins.
func hugepageSize(logger logr.Logger, pools []info.HugePagesInfo) uint64 {
	pool2M := poolMB(pools, HugepageSize2M)
	pool1G := poolMB(pools, HugepageSize1G)

	if pool2M > 0 && pool1G > 0 {
		logger.Info("Warning: host has both 2M and 1G hugepages, reporting only the larger pool",
			"pool2MMiB", pool2M, "pool1GMiB", pool1G)
	}

	switch {
	case pool1G > 0 && pool1G >= pool2M:
		return HugepageSize1G
	case pool2M > 0:
		return HugepageSize2M
	default:
		return 0
	}
}

// poolMB returns how much memory, in MiB, pools hold in pages of pageSize KiB.
func poolMB(pools []info.HugePagesInfo, pageSize uint64) int {
	total := 0

	for _, pool := range pools {
		if pool.PageSize == pageSize {
			total += int(pool.NumPages * pool.PageSize / 1024)
		}
	}

	return total
}
