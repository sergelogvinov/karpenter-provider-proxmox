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

package vmconfig

import (
	"fmt"
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"

	"k8s.io/utils/cpuset"
)

// LoadVMConfig loads the VM configuration for the given VM ID.
func LoadVMConfig(vmID int) (*proxmox.VirtualMachineConfig, error) {
	vm, err := goproxmox.GetLocalVMConfig(vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM config for VM %d: %w", vmID, err)
	}

	if vm.Affinity == "" {
		for part := range strings.SplitSeq(vm.Description, ",") {
			if affinity, ok := strings.CutPrefix(strings.TrimSpace(part), "affinity="); ok {
				if _, err := cpuset.Parse(affinity); err != nil {
					break
				}

				vm.Affinity = affinity

				break
			}
		}
	}

	return vm, nil
}
