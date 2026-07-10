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
