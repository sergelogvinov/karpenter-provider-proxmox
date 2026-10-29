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

	"github.com/samber/lo"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	"k8s.io/utils/cpuset"
)

// loadVMConfig reads vmID's config.
func loadVMConfig(ctx context.Context, client *local.Client, vmID int) (*qemu.Config, error) {
	cfg, err := client.Qemu().Get(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM config for VM %d: %w", vmID, err)
	}

	return cfg, nil
}

// vmFromConfig is the zone's view of a VM config: a field copy, the zone parses it.
func vmFromConfig(vmID int, cfg *qemu.Config) zonecapacity.VM {
	return zonecapacity.VM{
		ID:          vmID,
		Name:        cfg.Name,
		CPUs:        lo.FromPtr(cfg.Cores),
		Memory:      uint64(max(memoryMB(cfg), 0)) << 20, //nolint:gosec // clamped at zero
		Affinity:    cfg.Affinity,
		Description: cfg.Description,
		Tags:        lo.FromPtr(cfg.Tags),
	}
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

	r.logger.Info("VM config loaded", "vmID", vmID, "name", vmConfig.Name,
		"memoryMB", memoryMB(vmConfig), "cores", lo.FromPtr(vmConfig.Cores))

	if err := r.trackVM(vmID, pid, vmConfig); err != nil {
		r.logger.Error(err, "Failed to track VM", "vmID", vmID)
	}

	if r.zone == nil {
		return nil
	}

	// Place the starting VM right away; everyone else waits for the debounced rebalance.
	r.applyMu.Lock()

	plan := r.plan()
	if cpus, ok := plan.Changed[vmID]; ok {
		if err := r.applyCPUs(ctx, vmID, pid, cpus, plan.OneToOne[vmID]); err != nil {
			r.logger.Error(err, "Failed to apply CPU plan to VM", "vmID", vmID, "cpus", cpus.String())
		} else {
			r.logger.Info("VM placed", "vmID", vmID, "threadCount", len(threads), "cpus", cpus.String(), "oneToOne", plan.OneToOne[vmID])

			r.setGovernorBusy(vmID, cpus)
			r.steerDeviceIRQs(vmID, vmConfig, cpus)
		}
	}

	r.applyMu.Unlock()

	r.scheduleRebalance(ctx)

	return nil
}

// setGovernorBusy applies the configured busy CPU governor to cpus, a
// no-op if cpus is empty or --cpu-governor-busy is unset.
func (r *SchedulerHandler) setGovernorBusy(vmID int, cpus cpuset.CPUSet) {
	if cpus.IsEmpty() || *cpuGovernorBusy == "" {
		return
	}

	r.logger.Info("VM governing CPUs", "vmID", vmID, "governor", *cpuGovernorBusy, "cores", cpus.String())

	if err := utilsys.SetCPUGovernor(vmID, cpus.List(), *cpuGovernorBusy); err != nil {
		r.logger.Error(err, "Failed to set CPU governor for VM", "vmID", vmID)
	}
}

// steerDeviceIRQs steers every passthrough PCI device's IRQs to cpus.
func (r *SchedulerHandler) steerDeviceIRQs(vmID int, vmConfig *qemu.Config, cpus cpuset.CPUSet) {
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

			if err := utilsys.SetPciIRQAffinity(vmID, device.Host, irqs, cpus); err != nil {
				r.logger.Error(err, "Failed to set IRQ affinity for PCI device", "vmID", vmID, "device", device.Host)
			}
		}
	}
}

// handleVMStop handles when a VM stops (PID file removed)
func (r *SchedulerHandler) handleVMStop(ctx context.Context, vmID int) {
	r.logger.Info("Handling VM stop", "vmID", vmID)

	stopped, ok := r.forgetVM(vmID)
	if !ok {
		r.logger.Info("VM not found in tracker, skipping cleanup", "vmID", vmID)

		return
	}

	if r.zone == nil {
		return
	}

	// A CPU another VM still holds was never really freed by this VM stopping.
	freed := stopped.applied.Intersection(r.plan().Idle)

	if freed.Size() > 0 && *cpuGovernorFree != "" {
		r.logger.Info("VM governing CPUs", "vmID", vmID, "governor", *cpuGovernorFree, "cores", freed.String())

		if err := utilsys.SetCPUGovernor(vmID, freed.List(), *cpuGovernorFree); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for VM", "vmID", vmID)
		}
	}

	r.scheduleRebalance(ctx)
}

// trackVM records a running VM's process and config. A VM restarted with a new
// PID is tracked afresh: what was applied to its old process no longer holds.
// An error means the zone could not read the config; the VM is still tracked.
func (r *SchedulerHandler) trackVM(vmID, pid int, vmConfig *qemu.Config) error {
	r.tracker.mu.Lock()

	prev, ok := r.tracker.vms[vmID]
	restarted := ok && prev.pid != pid

	vm := trackedVM{pid: pid, applied: cpuset.New()}
	if ok && !restarted {
		vm.applied = prev.applied
	}

	r.tracker.vms[vmID] = vm

	r.tracker.mu.Unlock()

	if r.zone == nil {
		return nil
	}

	if restarted {
		r.zone.Release(vmID)
	}

	return r.zone.Track(vmFromConfig(vmID, vmConfig))
}

// forgetVM drops a stopped VM, returning what was tracked for it.
func (r *SchedulerHandler) forgetVM(vmID int) (trackedVM, bool) {
	r.tracker.mu.Lock()

	vm, ok := r.tracker.vms[vmID]
	delete(r.tracker.vms, vmID)

	r.tracker.mu.Unlock()

	if r.zone != nil {
		r.zone.Release(vmID)
	}

	return vm, ok
}

// memoryMB returns cfg's configured memory in MiB, or 0 if unset —
// cfg.Memory and cfg.Memory.Current are both pointers.
func memoryMB(cfg *qemu.Config) int {
	if cfg.Memory == nil {
		return 0
	}

	return lo.FromPtr(cfg.Memory.Current)
}
