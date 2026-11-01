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

	"github.com/samber/lo"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/vmconfig"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"

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

// VMStarted places a starting VM's threads and rebalances the unpinned CPUs. An error
// means the VM is not ready to place yet, e.g. it has no vCPU threads, and is retried.
func (r *SchedulerHandler) VMStarted(ctx context.Context, vmID int, pid int) error {
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

	r.applyMu.Lock()

	if err := r.trackVM(vmID, pid, vmConfig, true); err != nil {
		r.logger.Error(err, "Failed to track VM", "vmID", vmID)
	}

	plan := r.plan()
	r.logNotes(plan)

	if a, ok := plan.Changed[vmID]; ok {
		r.placeStartedVM(ctx, vmID, pid, threads, vmConfig, a, plan.Ungrouped)
	}

	r.applyMu.Unlock()

	// A VM starting always changes the unpinned CPUs: its own partition if under
	// shared placement, its CPU share group, or the CPUs it takes from them otherwise.
	r.scheduleRebalance(ctx)

	return nil
}

// placeStartedVM puts a starting VM's threads where a says. A pinned VM is pinned one
// vCPU per CPU, a constrained VM masked to its affinity and a CPU share VM to its
// group, all with the busy governor and their devices' IRQs on those CPUs. A VM under
// shared placement is masked to all the ungrouped CPUs until the next rebalance gives
// it its partition.
func (r *SchedulerHandler) placeStartedVM(ctx context.Context, vmID, pid int, threads []int, vmConfig *qemu.Config, a zonecapacity.Assignment, ungrouped cpuset.CPUSet) {
	switch a.Placement {
	case zonecapacity.PlacementPinned, zonecapacity.PlacementConstrained, zonecapacity.PlacementCPUShare:
		r.logger.Info("VM placing CPU threads", "vmID", vmID, "threadCount", len(threads), "cores", a.CPUs.String(), "oneToOne", a.OneToOne())

		if err := r.applyCPUs(ctx, vmID, pid, a); err != nil {
			r.logger.Error(err, "Failed to place VM threads", "vmID", vmID)
		}

		r.setGovernor(vmID, a.CPUs, *cpuGovernorBusy)
		r.steerDeviceIRQs(vmID, vmConfig, a.CPUs)

	case zonecapacity.PlacementShared:
		if ungrouped.IsEmpty() {
			return
		}

		r.logger.Info("VM masking CPU threads to the ungrouped CPUs", "vmID", vmID, "threadCount", len(threads), "cpus", ungrouped.String())

		if err := utilsys.SetThreadsAffinity(ctx, vmID, pid, threads, ungrouped); err != nil {
			r.logger.Error(err, "Failed to apply provisional shared CPU mask", "vmID", vmID)
		}

		if err := utilsys.SetProcessAffinity(ctx, vmID, pid, ungrouped); err != nil {
			r.logger.Error(err, "Failed to mask shared VM process", "vmID", vmID)
		}

		r.steerDeviceIRQs(vmID, vmConfig, ungrouped)
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

// VMStopped releases a stopped VM's CPUs and rebalances the unpinned CPUs.
func (r *SchedulerHandler) VMStopped(ctx context.Context, vmID int) {
	r.logger.Info("Handling VM stop", "vmID", vmID)

	r.applyMu.Lock()

	before := r.plan().Pinned

	if _, ok := r.forgetVM(vmID); !ok {
		r.applyMu.Unlock()

		r.logger.Info("VM not found in tracker, skipping cleanup", "vmID", vmID)

		return
	}

	// A CPU another VM still holds was never really freed by this VM stopping.
	freed := before.Difference(r.plan().Pinned)

	r.applyMu.Unlock()

	r.setGovernor(vmID, freed, *cpuGovernorFree)

	r.scheduleRebalance(ctx)
}

// trackVM records a running VM's process and config. A fresh track (a start), or a
// VM restarted with a new PID, forgets what was applied: it held for the old process.
// An error means the zone could not read the config; the VM is still tracked.
// Callers hold applyMu, so a rebalance never interleaves with tracking.
func (r *SchedulerHandler) trackVM(vmID, pid int, vmConfig *qemu.Config, fresh bool) error {
	if r.tracker.track(vmID, pid, fresh) {
		r.zone.Release(vmID)
	}

	vm, err := vmconfig.VM(vmID, vmConfig)

	switch {
	case errors.Is(err, vmconfig.ErrNUMAIgnored):
		r.logger.Info("Warning: VM has a numa entry that does not parse, its NUMA memory is ignored", "vmID", vmID, "reason", err.Error())
	case err != nil:
		return err
	}

	return r.zone.Track(vm)
}

// forgetVM drops a stopped VM, returning what was tracked for it. Callers hold applyMu.
func (r *SchedulerHandler) forgetVM(vmID int) (trackedVM, bool) {
	vm, ok := r.tracker.forget(vmID)
	r.zone.Release(vmID)

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
