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
	"fmt"
	"os"
	"strconv"
	"strings"
)

func GetPciDeviceIRQs(pciAddress string) ([]int, error) {
	var irqs []int

	data, err := os.ReadFile("/proc/interrupts")
	if err != nil {
		return nil, fmt.Errorf("failed to read /proc/interrupts: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}

		if strings.Contains(line, pciAddress) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				irqField := strings.TrimSuffix(fields[0], ":")
				if irq, err := strconv.Atoi(irqField); err == nil {
					irqs = append(irqs, irq)
				}
			}
		}
	}

	// Look in /sys/bus/pci/devices/{pciAddress}/msi_irqs/ for MSI interrupts
	msiPath := fmt.Sprintf("/sys/bus/pci/devices/%s/msi_irqs", pciAddress)

	entries, err := os.ReadDir(msiPath)
	if err == nil {
		for _, entry := range entries {
			if irq, err := strconv.Atoi(entry.Name()); err == nil {
				irqs = append(irqs, irq)
			}
		}
	}

	return irqs, nil
}
