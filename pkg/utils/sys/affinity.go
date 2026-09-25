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

package sys

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/utils/cpuset"
)

func PinThreadsToCores(ctx context.Context, vmID int, pid int, threads []int, cores []int) (err error) {
	if len(threads) == 0 || len(cores) == 0 || pid <= 0 {
		return nil
	}

	if len(threads) != len(cores) {
		return fmt.Errorf("VM %d: thread count %d does not match core count %d", vmID, len(threads), len(cores))
	}

	return func() error {
		defer func() {
			// Revert the process affinity to all threads on exit
			if err != nil {
				strs := make([]string, len(cores))
				for i, c := range cores {
					strs[i] = strconv.Itoa(c)
				}

				cmd := exec.CommandContext(ctx, "taskset", "-pc", strings.Join(strs, ","), strconv.Itoa(pid))
				if output, err := cmd.CombinedOutput(); err != nil {
					fmt.Printf("VM %d: Failed to set process %d CPU affinity to cores %v: %v, output: %s\n", vmID, pid, strings.Join(strs, ","), err, output)
				}
			}
		}()

		var output []byte

		for i, threadID := range threads {
			core := cores[i]

			cmd := exec.CommandContext(ctx, "taskset", "--cpu-list", "--pid", strconv.Itoa(core), strconv.Itoa(threadID))
			if output, err = cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("VM %d: Failed to pin thread %d to core %d: %w, output: %s", vmID, threadID, core, err, output)
			}
		}

		return nil
	}()
}

// GetThreadAffinity returns the CPU affinity mask currently applied to
// thread tid of process pid, read directly from /proc rather than by
// shelling out to taskset — this is what makes SetThreadsAffinity and
// SetProcessAffinity idempotent: a thread whose mask already matches is
// left untouched, so a resync over an unchanged host issues no taskset
// calls at all.
func GetThreadAffinity(pid, tid int) (cpuset.CPUSet, error) {
	statusFile := fmt.Sprintf("/proc/%d/task/%d/status", pid, tid)

	data, err := os.ReadFile(statusFile)
	if err != nil {
		return cpuset.New(), fmt.Errorf("failed to read %s: %w", statusFile, err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		list, ok := strings.CutPrefix(line, "Cpus_allowed_list:")
		if !ok {
			continue
		}

		return cpuset.Parse(strings.TrimSpace(list))
	}

	return cpuset.New(), fmt.Errorf("cpus_allowed_list not found in %s", statusFile)
}

// SetThreadsAffinity masks every thread in threads to cpus. Unlike
// PinThreadsToCores, which pins each thread 1:1 to its own dedicated
// core, every thread here shares the same mask — the shape a shared
// (unpinned) VM's confinement takes, since it owns a slice of the pool
// rather than one core per vCPU. A thread
// that already carries the right mask is left alone.
func SetThreadsAffinity(ctx context.Context, vmID, pid int, threads []int, cpus cpuset.CPUSet) error {
	if len(threads) == 0 || cpus.IsEmpty() || pid <= 0 {
		return nil
	}

	cpuList := cpus.String()

	for _, threadID := range threads {
		if current, err := GetThreadAffinity(pid, threadID); err == nil && current.Equals(cpus) {
			continue
		}

		cmd := exec.CommandContext(ctx, "taskset", "--cpu-list", "--pid", cpuList, strconv.Itoa(threadID))
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("VM %d: failed to set thread %d CPU affinity to %s: %w, output: %s", vmID, threadID, cpuList, err, output)
		}
	}

	return nil
}

// SetProcessAffinity masks the process pid itself — its thread-group
// leader — so any thread QEMU spawns afterwards (a hotplugged vCPU, an
// IO or migration thread) inherits cpus instead of starting out
// unconfined on the whole host. A no-op if
// the mask already matches.
func SetProcessAffinity(ctx context.Context, vmID, pid int, cpus cpuset.CPUSet) error {
	if pid <= 0 || cpus.IsEmpty() {
		return nil
	}

	if current, err := GetThreadAffinity(pid, pid); err == nil && current.Equals(cpus) {
		return nil
	}

	cpuList := cpus.String()

	cmd := exec.CommandContext(ctx, "taskset", "--cpu-list", "--pid", cpuList, strconv.Itoa(pid))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("VM %d: failed to set process %d CPU affinity to %s: %w, output: %s", vmID, pid, cpuList, err, output)
	}

	return nil
}

// SetPciIRQAffinity masks IRQs to VM cores.
func SetPciIRQAffinity(vmID int, pciAddress string, irqs []int, cpus cpuset.CPUSet) error {
	if len(irqs) == 0 || cpus.IsEmpty() {
		return nil
	}

	for _, irq := range irqs {
		affinityFile := fmt.Sprintf("/proc/irq/%d/smp_affinity_list", irq)

		if err := os.WriteFile(affinityFile, []byte(cpus.String()+"\n"), 0o644); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return fmt.Errorf("failed to set IRQ affinity for VM %d, PCI device %s, IRQ %d: %w", vmID, pciAddress, irq, err)
		}
	}

	return nil
}
