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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"
	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
)

func createProxmoxTopologyDiscoveryVM(logger logr.Logger, serverInfo *info.MachineInfo, tp *topology.CPUTopology) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vmID, err := goproxmox.GetLocalNextID(ctx)
	if err != nil || vmID == 0 {
		return fmt.Errorf("failed to get available VM ID: %w", err)
	}

	vm, err := goproxmox.GetLocalVMConfigByFilter(func(v *proxmox.VirtualMachineConfig) (bool, error) {
		return v.Name == "node-capacity" && v.Tags == "karpenter", nil
	})
	if err != nil && !errors.Is(err, goproxmox.ErrVirtualMachineNotFound) {
		return fmt.Errorf("failed to check existing VMs: %w", err)
	}

	if vm != nil {
		return nil
	}

	logger.Info("Creating Proxmox VM for Karpenter discovery service", "vmID", vmID)

	options := buildVMOptions(serverInfo, tp)
	if err := goproxmox.CreateLocalVM(ctx, vmID, options); err != nil {
		return err
	}

	return nil
}

func buildVMOptions(serverInfo *info.MachineInfo, tp *topology.CPUTopology) map[string]any {
	totalCores := serverInfo.NumCores
	totalMemoryMB := serverInfo.MemoryCapacity / (1024 * 1024)

	options := map[string]any{
		"name":        "node-capacity",
		"description": "Karpenter discovery service",
		"cores":       totalCores,
		"sockets":     1,
		"cpu":         "host",
		"numa":        1,
		"ostype":      "l26",
		"tags":        "karpenter",
	}

	if len(serverInfo.Topology) > 0 {
		affinity := []string{}

		cpush := 0
		totalMemoryMB = 0
		idx := 0

		for _, nodeInfo := range serverInfo.Topology {
			cpus := tp.CPUDetails.CPUsInNUMANodes(nodeInfo.Id)
			if cpus.Size() == 0 {
				continue
			}

			affinity = append(affinity, fmt.Sprintf("%s", cpus))

			memoryInNode := nodeInfo.Memory / (1024 * 1024 * 1024)
			if memoryInNode > 1 {
				memoryInNode -= 1
			}

			memoryInNode *= 1024
			totalMemoryMB += memoryInNode

			numaArg := fmt.Sprintf("cpus=%d-%d,hostnodes=%d,memory=%d", cpush, cpush+cpus.Size()-1, nodeInfo.Id, memoryInNode)
			cpush += cpus.Size()

			options[fmt.Sprintf("numa%d", idx)] = numaArg
			idx++
		}

		options["affinity"] = strings.Join(affinity, ",")
	}

	options["memory"] = totalMemoryMB

	return options
}
