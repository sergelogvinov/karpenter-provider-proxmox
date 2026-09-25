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

	affinity := cpuset.New()

	if vmConfig.Affinity != "" {
		affinity, err = cpuset.Parse(vmConfig.Affinity)
		if err != nil {
			r.logger.Error(err, "Failed to parse CPU affinity for VM", "vmID", vmID)

			return fmt.Errorf("failed to parse CPU affinity: %w", err)
		}
	}

	class := classifyVM(cores, affinity, lo.FromPtr(vmConfig.Tags))

	switch class {
	case ClassIgnored:
		r.logger.V(1).Info("Ignoring Karpenter discovery VM", "vmID", vmID, "name", vmConfig.Name)

	case ClassPinned:
		r.logger.Info("VM pinning CPU threads to cores", "vmID", vmID, "threadCount", len(threads), "cores", affinity.String())

		if r.topology != nil && r.topology.CPUDetails.CPUs().Intersection(affinity).Size() != affinity.Size() {
			r.logger.Error(fmt.Errorf("topology mismatch"), "Failed to pin VM to cores",
				"vmID", vmID,
				"affinity", vmConfig.Affinity,
				"topologyCPUs", r.topology.CPUDetails.CPUs().String(),
			)

			// Return nil to avoid retrying, as this is a configuration issue
			// It can be fixed in the VM configuration when the VM is stopped and then restarted
			return nil
		}

		if err := utilsys.PinThreadsToCores(ctx, vmID, pid, threads, affinity.UnsortedList()); err != nil {
			r.logger.Error(err, "Failed to pin VM threads to cores", "vmID", vmID)
		}

		r.setGovernorBusy(vmID, affinity)
		r.steerDeviceIRQs(vmID, vmConfig, affinity)

	case ClassConstrained:
		// The affinity is narrower than a 1:1 pinning: mask every vCPU
		// thread to it instead of pinning each one individually.
		r.logger.Info("VM masking CPU threads to affinity", "vmID", vmID, "threadCount", len(threads), "cores", affinity.String())

		if err := utilsys.SetThreadsAffinity(ctx, vmID, pid, threads, affinity); err != nil {
			r.logger.Error(err, "Failed to mask VM threads to affinity", "vmID", vmID)
		}

		if err := utilsys.SetProcessAffinity(ctx, vmID, pid, affinity); err != nil {
			r.logger.Error(err, "Failed to mask VM process to affinity", "vmID", vmID)
		}

		r.setGovernorBusy(vmID, affinity)
		r.steerDeviceIRQs(vmID, vmConfig, affinity)

	case ClassShared:
		// Confine the VM to the shared pool immediately — but only when
		// shared-pool placement is actually enabled. SharedPolicyNone
		// must be a true no-op (the documented "ship dark" escape
		// hatch), so an unaffined VM is left exactly as Proxmox started
		// it, same as before this feature existed.
		if r.sharedPolicy != SharedPolicyNone {
			if pool := r.sharedPool(); !pool.IsEmpty() {
				r.logger.Info("VM masking CPU threads to shared pool", "vmID", vmID, "threadCount", len(threads), "pool", pool.String())

				if err := utilsys.SetThreadsAffinity(ctx, vmID, pid, threads, pool); err != nil {
					r.logger.Error(err, "Failed to apply provisional shared CPU mask", "vmID", vmID)
				}

				if err := utilsys.SetProcessAffinity(ctx, vmID, pid, pool); err != nil {
					r.logger.Error(err, "Failed to mask shared VM process", "vmID", vmID)
				}

				r.steerDeviceIRQs(vmID, vmConfig, pool)
			}
		}
	}

	if err := r.updateVMInfo(vmID, pid, vmConfig, cpuset.New()); err != nil {
		r.logger.Error(err, "Failed to update VM info", "vmID", vmID)
	}

	if class != ClassIgnored {
		// A VM starting or stopping always changes the boundary of the
		// shared pool (its own request if shared, or the exclusive pool
		// it carves out of it otherwise)
		r.scheduleRebalance(ctx)
	}

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

	r.tracker.mu.Lock()

	stopped, ok := r.tracker.vms[vmID]
	if !ok {
		r.tracker.mu.Unlock()

		r.logger.Info("VM not found in tracker, skipping cleanup", "vmID", vmID)

		return
	}

	freed := stopped.AffinitySet

	delete(r.tracker.vms, vmID)

	// Recompute the exclusive pool from what actually remains —
	// previously usedCPUs/sharedCPUs were left stale after a stop until
	// the next start or resync, which the shared-pool planner depends on being accurate.
	r.tracker.usedCPUs = cpuset.New()

	for _, vmInfo := range r.tracker.vms {
		if vmInfo.AffinitySet.Size() == 0 {
			continue
		}

		r.tracker.usedCPUs = r.tracker.usedCPUs.Union(vmInfo.AffinitySet)
		// A CPU another VM still exclusively owns was never really
		// freed by this VM stopping.
		freed = freed.Difference(vmInfo.AffinitySet)
	}

	if r.topology != nil {
		r.tracker.sharedCPUs = r.topology.CPUDetails.CPUs().Difference(r.tracker.usedCPUs)
	}

	r.tracker.mu.Unlock()

	if freed.Size() > 0 && *cpuGovernorFree != "" {
		r.logger.Info("VM governing CPUs", "vmID", vmID, "governor", *cpuGovernorFree, "cores", freed.String())

		if err := utilsys.SetCPUGovernor(vmID, freed.List(), *cpuGovernorFree); err != nil {
			r.logger.Error(err, "Failed to set CPU governor for VM", "vmID", vmID)
		}
	}

	if stopped.Class != ClassIgnored {
		r.scheduleRebalance(ctx)
	}
}

// updateVMInfo updates the tracker for a VM, classifying it per
// previousAssigned carries over the cpuset
// the daemon had already applied to this VM's threads before this call
// — a fresh start has none (cpuset.New()), while a resync passes the
// pre-rebuild tracker's value, so a resync does not forget a shared VM's
// placement and make the next rebalance reshuffle it needlessly.
func (r *SchedulerHandler) updateVMInfo(vmID int, pid int, vmConfig *qemu.Config, previousAssigned cpuset.CPUSet) error {
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()

	// A re-entrant call for a VMID already in the tracker (e.g. a
	// duplicate/non-Remove fsnotify event for a VM that's already
	// running) must not double-count its old affinity: drop it from
	// usedCPUs before the class/affinity below re-adds whatever the
	// current config says, instead of just unioning on top and leaking
	// stale CPUs into the exclusive pool forever.
	if existing, ok := r.tracker.vms[vmID]; ok && existing.AffinitySet.Size() > 0 {
		r.tracker.usedCPUs = r.tracker.usedCPUs.Difference(existing.AffinitySet)
	}

	cores := lo.FromPtr(vmConfig.Cores)
	affinitySet := cpuset.New()

	if vmConfig.Affinity != "" {
		parsed, err := cpuset.Parse(vmConfig.Affinity)
		if err != nil {
			r.logger.Error(err, "Failed to parse CPU affinity for VM", "vmID", vmID, "affinity", vmConfig.Affinity)
		} else {
			affinitySet = parsed
		}
	}

	class := classifyVM(cores, affinitySet, lo.FromPtr(vmConfig.Tags))

	vmInfo := &VMInfo{
		VMID:        vmID,
		PID:         pid,
		Cores:       cores,
		Name:        vmConfig.Name,
		Class:       class,
		AssignedSet: previousAssigned,
	}

	// Only pinned/constrained VMs contribute to the exclusive pool — the
	// Karpenter discovery VM's affinity (if any) typically spans the
	// whole host and must never be counted as "in use".
	if class == ClassPinned || class == ClassConstrained {
		vmInfo.AffinitySet = affinitySet
		r.tracker.usedCPUs = r.tracker.usedCPUs.Union(affinitySet)

		if class == ClassPinned {
			vmInfo.AssignedSet = affinitySet
		}
	}

	r.tracker.vms[vmID] = vmInfo

	if r.topology != nil {
		r.tracker.sharedCPUs = r.topology.CPUDetails.CPUs().Difference(r.tracker.usedCPUs)
	}

	return nil
}

// memoryMB returns cfg's configured memory in MiB, or 0 if unset —
// cfg.Memory and cfg.Memory.Current are both pointers.
func memoryMB(cfg *qemu.Config) int {
	if cfg.Memory == nil {
		return 0
	}

	return lo.FromPtr(cfg.Memory.Current)
}
