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

// Package vmconfig reads a running VM's config as the zone takes it. The controller
// reads configs over the Proxmox API and the proxmox-scheduler from local files; both
// read them here, so both count a VM alike.
package vmconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	"k8s.io/utils/cpuset"
)

// defaultMemory is the memory, in MiB, Proxmox gives a VM without a `memory` option.
const defaultMemory = 512

// ErrNUMAIgnored means VM read the VM but skipped a numaN entry whose host nodes do not parse:
// the VM counts without that entry's NUMA memory. It is a warning, not a failure.
var ErrNUMAIgnored = errors.New("numa entry ignored")

// VM is VM id as the zone reads it from cfg. Its vCPUs are counted as Proxmox does, `vcpus` if set,
// else sockets times cores, and its memory is `memory` or the Proxmox default. The zone parses the
// affinity and the description; only the numaN memory is summed per host node, split evenly
// across a numaN entry's host nodes.
// An error wrapping ErrNUMAIgnored is a warning: the VM is still returned.
func VM(id int, cfg *qemu.Config) (zonecapacity.VM, error) {
	if cfg == nil {
		return zonecapacity.VM{}, fmt.Errorf("%w: VM %d has no config", zonecapacity.ErrInvalid, id)
	}

	vm := zonecapacity.VM{
		ID:          id,
		Name:        cfg.Name,
		CPUs:        vcpus(cfg),
		Memory:      memory(cfg) * zonecapacity.MiB,
		Affinity:    cfg.Affinity,
		Description: cfg.Description,
	}

	if cfg.Tags != nil {
		vm.Tags = *cfg.Tags
	}

	var errs []error

	for idx, n := range cfg.NUMA {
		if n.Memory == nil || *n.Memory <= 0 || len(n.HostNodes) == 0 {
			continue
		}

		hostNodes, err := cpuset.Parse(strings.Join(n.HostNodes, ","))
		if err != nil || hostNodes.IsEmpty() {
			errs = append(errs, fmt.Errorf("%w: host nodes of numa%d do not parse: %v", ErrNUMAIgnored, idx, n.HostNodes))

			continue
		}

		if vm.NUMAMemory == nil {
			vm.NUMAMemory = map[int]uint64{}
		}

		for _, node := range hostNodes.List() {
			vm.NUMAMemory[node] += uint64(*n.Memory) / uint64(hostNodes.Size()) * zonecapacity.MiB
		}
	}

	return vm, errors.Join(errs...)
}

// vcpus is cfg's vCPU count: `vcpus` if set, else sockets times cores, each 1 when unset.
func vcpus(cfg *qemu.Config) int {
	if cfg.VCPUs != nil && *cfg.VCPUs > 0 {
		return *cfg.VCPUs
	}

	return orOne(cfg.Sockets) * orOne(cfg.Cores)
}

// memory is cfg's memory in MiB, the Proxmox default when unset.
func memory(cfg *qemu.Config) uint64 {
	if cfg.Memory == nil || cfg.Memory.Current == nil || *cfg.Memory.Current <= 0 {
		return defaultMemory
	}

	return uint64(*cfg.Memory.Current)
}

func orOne(n *int) int {
	if n == nil || *n <= 0 {
		return 1
	}

	return *n
}
