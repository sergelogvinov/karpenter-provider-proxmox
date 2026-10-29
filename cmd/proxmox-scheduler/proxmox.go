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

package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/apimachinery/pkg/util/wait"
)

func createProxmoxTopologyDiscoveryVM(logger logr.Logger, client *local.Client, serverInfo *info.MachineInfo, tp *topology.Topology) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		ready, err := client.Cluster().Quorate(ctx)
		if err != nil {
			logger.Error(err, "Failed to check Proxmox cluster quorum status, retrying...")

			return false, nil //nolint:nilerr
		}

		if !ready {
			logger.Info("Proxmox cluster has no quorum")

			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for Proxmox cluster quorum: %w", err)
	}

	// List never errors just because nothing matches (empty slice, no
	// error)
	guests, err := client.Qemu().List(ctx, qemu.ListFilter{Name: "node-capacity"})
	if err != nil {
		return fmt.Errorf("failed to check existing VMs: %w", err)
	}

	cfg := buildVMConfig(logger, serverInfo, tp)

	if len(guests) > 0 {
		vmID := guests[0].VMID

		// Update never sends an option the desired config leaves unset, so
		// a hugepage pool the host no longer has must be deleted explicitly.
		if cfg.Hugepages == nil {
			current, err := client.Qemu().Get(ctx, vmID)
			if err != nil {
				return fmt.Errorf("failed to get existing VM %d config: %w", vmID, err)
			}

			if current.Hugepages != nil {
				cfg.Delete = []string{"hugepages"}
			}
		}

		if err := client.Qemu().Update(ctx, vmID, cfg); err != nil {
			return fmt.Errorf("failed to update existing VM %d: %w", vmID, err)
		}

		return nil
	}

	vmID, err := client.Cluster().NextID(ctx)
	if err != nil || vmID == 0 {
		if err != nil {
			logger.Error(err, "Failed to get next VM ID")
		}

		return fmt.Errorf("failed to get available VM ID")
	}

	logger.Info("Creating Proxmox VM for Karpenter discovery service", "vmID", vmID)

	if err := client.Qemu().Create(ctx, vmID, cfg); err != nil {
		return err
	}

	return nil
}

// Hugepage sizes, in KiB, that Proxmox's hugepages option accepts.
const (
	hugepageSize2M = 2 * 1024
	hugepageSize1G = 1024 * 1024
)

// hugepagePools returns the hugepage counts the node-capacity guest is
// built from: those of the NUMA nodes it reports when cadvisor knows the
// topology, the machine-wide ones otherwise, so the page size chosen and
// the memory reported come from the same pages.
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

// hugepageSize returns the page size (KiB) of the host's hugepage pool, or
// 0 when it has none. Hosts are expected to preallocate one size only; if
// they have both, the size holding more memory wins.
func hugepageSize(logger logr.Logger, pools []info.HugePagesInfo) uint64 {
	pool2M := poolMB(pools, hugepageSize2M)
	pool1G := poolMB(pools, hugepageSize1G)

	if pool2M > 0 && pool1G > 0 {
		logger.Info("Warning: host has both 2M and 1G hugepages, reporting only the larger pool",
			"pool2MMiB", pool2M, "pool1GMiB", pool1G)
	}

	switch {
	case pool1G > 0 && pool1G >= pool2M:
		return hugepageSize1G
	case pool2M > 0:
		return hugepageSize2M
	default:
		return 0
	}
}

// poolMB returns how much memory, in MiB, pools hold in pages of pageSize
// KiB.
func poolMB(pools []info.HugePagesInfo, pageSize uint64) int {
	total := 0

	for _, pool := range pools {
		if pool.PageSize == pageSize {
			total += int(pool.NumPages * pool.PageSize / 1024)
		}
	}

	return total
}

// buildVMConfig builds the node-capacity guest's desired configuration
// as a typed *qemu.Config. On a host with a hugepage pool the memory it
// reports is the pool's, per NUMA node, not the host's ordinary memory.
func buildVMConfig(logger logr.Logger, serverInfo *info.MachineInfo, tp *topology.Topology) *qemu.Config {
	totalCores := serverInfo.NumCores
	totalMemoryMB := int(serverInfo.MemoryCapacity / (1024 * 1024))
	pageSize := hugepageSize(logger, hugepagePools(serverInfo, tp))

	cfg := &qemu.Config{
		Name:        "node-capacity",
		Description: "Karpenter discovery service",
		Cores:       &totalCores,
		Sockets:     new(1),
		CPU:         &qemu.CPU{Type: "host"},
		NUMAEnabled: new(true),
		OSType:      new("l26"),
		Tags:        &qemu.Tags{"karpenter"},
	}

	if pageSize > 0 {
		cfg.Hugepages = new(strconv.FormatUint(pageSize/1024, 10))
		totalMemoryMB = poolMB(serverInfo.HugePages, pageSize)
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

			if pageSize > 0 {
				// A node without pages of this size reports 0, but keeps
				// its CPUs.
				memoryInNode = poolMB(nodeInfo.HugePages, pageSize)
			}

			totalMemoryMB += memoryInNode

			numa[idx] = qemu.NUMA{
				CPUIDs:    []string{fmt.Sprintf("%d-%d", cpush, cpush+cpus.Size()-1)},
				HostNodes: []string{strconv.Itoa(nodeInfo.Id)},
				Memory:    &memoryInNode,
			}

			cpush += cpus.Size()
			idx++
		}

		cfg.Affinity = strings.Join(affinity, ",")
		cfg.NUMA = numa
	}

	cfg.Memory = &qemu.Memory{Current: &totalMemoryMB}

	return cfg
}
