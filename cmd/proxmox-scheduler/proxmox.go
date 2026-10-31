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
	"time"

	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/nodecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/apimachinery/pkg/util/wait"
)

// ensureNodeCapacityGuest creates the node-capacity guest for the host once the
// cluster has quorum, or updates the existing one, deleting the options it no
// longer sets.
func ensureNodeCapacityGuest(logger logr.Logger, client *local.Client, serverInfo *info.MachineInfo, tp *topology.Topology) error {
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
	guests, err := client.Qemu().List(ctx, qemu.ListFilter{Name: nodecapacity.Name})
	if err != nil {
		return fmt.Errorf("failed to check existing VMs: %w", err)
	}

	cfg := nodecapacity.Encode(logger, serverInfo, tp)

	if len(guests) > 0 {
		vmID := guests[0].VMID

		current, err := client.Qemu().Get(ctx, vmID)
		if err != nil {
			return fmt.Errorf("failed to get existing VM %d config: %w", vmID, err)
		}

		cfg.Delete = nodecapacity.StaleOptions(current, cfg)

		logger.V(1).Info("Updating the node-capacity guest", "vmID", vmID,
			"memory", memoryMB(current), "desiredMemory", memoryMB(cfg), "delete", cfg.Delete)

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

	logger.Info("Creating the node-capacity guest", "vmID", vmID)

	if err := client.Qemu().Create(ctx, vmID, cfg); err != nil {
		return err
	}

	return nil
}
