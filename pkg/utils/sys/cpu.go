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
	"strings"
)

func SetCPUGovernor(vmID int, cpus []int, governor string) error {
	if len(cpus) == 0 {
		return nil
	}

	if !checkCpuGovernorAvailable(cpus[0], governor) {
		return fmt.Errorf("CPU governor %s not available", governor)
	}

	for _, cpuID := range cpus {
		governorFile := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_governor", cpuID)

		currentData, err := os.ReadFile(governorFile)
		if err != nil {
			continue
		}

		if strings.TrimSpace(string(currentData)) == governor {
			continue
		}

		err = os.WriteFile(governorFile, []byte(governor+"\n"), 0o644)
		if err != nil {
			return fmt.Errorf("failed to set CPU governor for VM %d, CPU %d: %w", vmID, cpuID, err)
		}
	}

	return nil
}

// checkCpuGovernorAvailable checks if a CPU governor is available on a specific CPU
func checkCpuGovernorAvailable(cpuID int, governor string) bool {
	governorFile := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_available_governors", cpuID)

	data, err := os.ReadFile(governorFile)
	if err != nil {
		return false
	}

	for availableGov := range strings.FieldsSeq(string(data)) {
		if availableGov == governor {
			return true
		}
	}

	return false
}
