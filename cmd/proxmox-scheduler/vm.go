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
	"strings"

	"github.com/samber/lo"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"

	"k8s.io/utils/cpuset"
)

// loadVMConfig reads vmID's config and applies the affinity-fallback
// convention: when the guest has no explicit affinity option set, look
// for an "affinity=<cpuset>" token in its description.
func loadVMConfig(ctx context.Context, client *local.Client, vmID int) (*qemu.Config, error) {
	cfg, err := client.Qemu().Get(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM config for VM %d: %w", vmID, err)
	}

	if cfg.Affinity == "" {
		for part := range strings.SplitSeq(cfg.Description, ",") {
			affinity, ok := strings.CutPrefix(strings.TrimSpace(part), "affinity=")
			if !ok {
				continue
			}

			if _, err := cpuset.Parse(affinity); err != nil {
				break
			}

			cfg.Affinity = affinity

			break
		}
	}

	return cfg, nil
}

func (r *SchedulerHandler) handleVMStart(ctx context.Context, vmID int, pid int) error {
	if !utilsys.ProcessExists(pid) {
		r.logger.Info("Warning: VM does not exist or is not accessible", "vmID", vmID, "pid", pid)

		return fmt.Errorf("VM %d process %d does not exist", vmID, pid)
	}

	threads, err := utilsys.GetProcessThreads(pid, "CPU")
	if err != nil {
		r.logger.Error(err, "Failed to get threads PID", "vmID", vmID, "pid", pid)

		return err
	}

	if len(threads) == 0 {
		r.logger.Info("VM has no CPU threads yet", "vmID", vmID)

		return fmt.Errorf("VM %d has no CPU threads yet", vmID)
	}

	vmConfig, err := loadVMConfig(ctx, r.client, vmID)
	if err != nil {
		return err
	}

	cores := lo.FromPtr(vmConfig.Cores)

	r.logger.Info("VM config loaded", "vmID", vmID, "name", vmConfig.Name,
		"memoryMB", memoryMB(vmConfig), "cores", cores)

	if vmConfig.Affinity != "" {
		cpus, err := cpuset.Parse(vmConfig.Affinity)
		if err != nil {
			r.logger.Error(err, "Failed to parse CPU affinity for VM", "vmID", vmID)

			return fmt.Errorf("failed to parse CPU affinity: %w", err)
		}

		if cores == cpus.Size() {
			r.logger.Info("VM pinning CPU threads to cores", "vmID", vmID, "threadCount", len(threads), "cores", cpus.String())

			if r.topology != nil && r.topology.CPUDetails.CPUs().Intersection(cpus).Size() != cpus.Size() {
				r.logger.Error(fmt.Errorf("topology mismatch"), "Failed to pin VM to cores",
					"vmID", vmID,
					"affinity", vmConfig.Affinity,
					"topologyCPUs", r.topology.CPUDetails.CPUs().String(),
				)

				// Return nil to avoid retrying, as this is a configuration issue
				// It can be fixed in the VM configuration when the VM is stopped and then restarted
				return nil
			}

			err = utilsys.PinThreadsToCores(ctx, vmID, pid, threads, cpus.UnsortedList())
			if err != nil {
				r.logger.Error(err, "Failed to pin VM threads to cores", "vmID", vmID)
			}

			if *cpuGovernorBusy != "" {
				r.logger.Info("VM governing CPUs", "vmID", vmID, "governor", *cpuGovernorBusy, "cores", cpus.String())

				err = utilsys.SetCPUGovernor(vmID, cpus.List(), *cpuGovernorBusy)
				if err != nil {
					r.logger.Error(err, "Failed to set CPU governor for VM", "vmID", vmID)
				}
			}
		}

		for _, device := range vmConfig.HostPCI {
			if device.Host == "" {
				continue
			}

			irqs, err := utilsys.GetPciDeviceIRQs(device.Host)
			if err != nil {
				r.logger.Error(err, "Failed to find IRQs for PCI device", "vmID", vmID, "device", device.Host)

				continue
			}

			if len(irqs) > 0 {
				r.logger.Info("VM setting IRQ affinity", "vmID", vmID, "device", device.Host, "irqs", irqs, "cpus", cpus.String())

				err = utilsys.SetPciIRQAffinity(vmID, device.Host, irqs, cpus)
				if err != nil {
					r.logger.Error(err, "Failed to set IRQ affinity for PCI device", "vmID", vmID, "device", device.Host)
				}
			}
		}
	}

	if err := r.updateVMInfo(vmID, pid, vmConfig); err != nil {
		r.logger.Error(err, "Failed to update VM info", "vmID", vmID)
	}

	return nil
}

// handleVMStop handles when a VM stops (PID file removed)
func (r *SchedulerHandler) handleVMStop(_ context.Context, vmID int) error {
	r.logger.Info("Handling VM stop", "vmID", vmID)

	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()

	if _, ok := r.tracker.vms[vmID]; !ok {
		r.logger.Info("VM not found in tracker, skipping cleanup", "vmID", vmID)

		return nil
	}

	cpus := r.tracker.vms[vmID].AffinitySet
	for id, vmInfo := range r.tracker.vms {
		if id == vmID {
			continue
		}

		if vmInfo.AffinitySet.Size() > 0 {
			cpus = cpus.Difference(vmInfo.AffinitySet)
		}
	}

	if cpus.Size() > 0 && *cpuGovernorFree != "" {
		r.logger.Info("VM governing CPUs", "vmID", vmID, "governor", *cpuGovernorFree, "cores", cpus.String())

		if err := utilsys.SetCPUGovernor(vmID, cpus.List(), *cpuGovernorFree); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for VM", "vmID", vmID)
		}
	}

	delete(r.tracker.vms, vmID)

	return nil
}

// updateVMInfo updates the tracker when a VM starts
func (r *SchedulerHandler) updateVMInfo(vmID int, pid int, vmConfig *qemu.Config) error {
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()

	vmInfo := &VMInfo{
		VMID:  vmID,
		PID:   pid,
		Cores: lo.FromPtr(vmConfig.Cores),
		Name:  vmConfig.Name,
	}

	if vmConfig.Affinity != "" {
		affinitySet, err := cpuset.Parse(vmConfig.Affinity)
		if err != nil {
			r.logger.Error(err, "Failed to parse CPU affinity for VM", "vmID", vmID, "affinity", vmConfig.Affinity)
		}

		if affinitySet.Size() > 0 {
			vmInfo.AffinitySet = affinitySet
			r.tracker.usedCPUs = r.tracker.usedCPUs.Union(affinitySet)
		}
	}

	r.tracker.vms[vmID] = vmInfo

	if r.topology != nil {
		r.tracker.sharedCPUs = r.topology.CPUDetails.CPUs().Difference(r.tracker.usedCPUs)
	}

	return nil
}

// memoryMB returns cfg's configured memory in MiB, or 0 if unset —
// cfg.Memory and cfg.Memory.Current are both pointers (docs/design.md
// §6.5).
func memoryMB(cfg *qemu.Config) int {
	if cfg.Memory == nil {
		return 0
	}

	return lo.FromPtr(cfg.Memory.Current)
}
