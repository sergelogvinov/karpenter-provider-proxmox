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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

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

	cfg := buildVMConfig(serverInfo, tp)

	if len(guests) > 0 {
		vmID := guests[0].VMID

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

// buildVMConfig builds the node-capacity guest's desired configuration
// as a typed *qemu.Config — replacing the old map[string]any built with
// fmt.Sprintf, which is how docs/design.md §2.4's idempotency bug
// (comparing a uint64 memory value against the int YAML decoded)
// originated in the first place: there is no second, differently-typed
// representation to drift from here. Update encodes this exact struct
// through the same encoder used to decode the on-disk config.
func buildVMConfig(serverInfo *info.MachineInfo, tp *topology.Topology) *qemu.Config {
	totalCores := serverInfo.NumCores
	totalMemoryMB := int(serverInfo.MemoryCapacity / (1024 * 1024))

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
