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

package vmresources

import (
	"fmt"
	"sort"
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	resources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

// GetResourceFromVM extracts VMResources from a Proxmox VirtualMachine object.
func GetResourceFromVM(vm *proxmox.VirtualMachine) (opt *resources.VMResources, err error) {
	if vm == nil {
		return nil, fmt.Errorf("virtual machine config cannot be nil")
	}

	opt = &resources.VMResources{
		ID:     int(vm.VMID),
		CPUs:   vm.CPUs,
		CPUSet: cpuset.New(),
		Memory: vm.MaxMem,
	}

	if vm.VirtualMachineConfig != nil {
		if vm.VirtualMachineConfig.Affinity != "" {
			opt.CPUSet, err = cpuset.Parse(vm.VirtualMachineConfig.Affinity)
			if err != nil {
				return nil, fmt.Errorf("failed to parse CPU affinity: %w", err)
			}
		}

		if vm.VirtualMachineConfig.Numa == 1 {
			numas := vm.VirtualMachineConfig.MergeNumas()

			opt.NUMANodes = make(map[int]goproxmox.NUMANodeState)

			for _, numa := range numas {
				n := goproxmox.VMNUMA{}

				err := n.UnmarshalString(numa)
				if err != nil {
					return nil, fmt.Errorf("failed to parse NUMA config: %w", err)
				}

				if (n.Memory != nil && *n.Memory > 0) && len(n.CPUIDs) > 0 && len(n.HostNodeNames) > 0 {
					cpus, err := cpuset.Parse(strings.Join(n.CPUIDs, ","))
					if err != nil {
						return nil, fmt.Errorf("failed to parse CPU IDs for NUMA node %s: %w", numa, err)
					}

					hostNuma, err := cpuset.Parse(strings.Join(n.HostNodeNames, ","))
					if err != nil {
						return nil, fmt.Errorf("failed to parse Host Node Names for NUMA node %s: %w", numa, err)
					}

					cpuList := cpus.List()

					numHostNodes := hostNuma.Size()
					if numHostNodes == 0 {
						return nil, fmt.Errorf("NUMA host nodes set is empty for NUMA node %s", numa)
					}
					if numHostNodes > 1 && len(cpuList)%numHostNodes != 0 {
						return nil, fmt.Errorf("cannot evenly distribute %d CPUs across %d NUMA nodes for NUMA node %s", len(cpuList), numHostNodes, numa)
					}

					nodeCpus := cpus.Size() / numHostNodes

					for i, nodeID := range hostNuma.List() {
						old := opt.NUMANodes[nodeID]
						opt.NUMANodes[nodeID] = goproxmox.NUMANodeState{
							Memory: old.Memory + uint64(*n.Memory)/uint64(numHostNodes),
							CPUs:   old.CPUs.Union(cpuset.New(cpuList[i*nodeCpus : (i+1)*nodeCpus]...)),
							Policy: n.Policy,
						}
					}
				}
			}
		}
	}

	return opt, nil
}

// GenerateVMOptionsFromResources creates Proxmox VirtualMachineOptions from VMResources.
func GenerateVMOptionsFromResources(res *resources.VMResources) (opts map[string]any, err error) {
	if res == nil {
		return nil, fmt.Errorf("VM resources cannot be nil")
	}

	opts = map[string]any{
		"cores":  res.CPUs,
		"memory": res.Memory / 1024 / 1024,
	}

	if !res.CPUSet.IsEmpty() {
		opts["affinity"] = res.CPUSet.String()
	}

	if len(res.NUMANodes) > 0 {
		opts["numa"] = 1

		cpuIdx := 0

		numaKeys := make([]int, 0, len(res.NUMANodes))
		for k := range res.NUMANodes {
			numaKeys = append(numaKeys, k)
		}

		sort.Ints(numaKeys)

		for i, k := range numaKeys {
			node := res.NUMANodes[k]
			if node.CPUs.IsEmpty() {
				return nil, fmt.Errorf("NUMA node %d has no CPUs assigned", k)
			}

			numaKey := fmt.Sprintf("numa%d", i)
			numaConfig := goproxmox.VMNUMA{
				Memory:        ptr.To(int(node.Memory)),
				CPUIDs:        []string{},
				Policy:        node.Policy,
				HostNodeNames: []string{fmt.Sprintf("%d", k)},
			}

			numaConfig.CPUIDs = []string{fmt.Sprintf("%d-%d", cpuIdx, cpuIdx+node.CPUs.Size()-1)}
			cpuIdx += node.CPUs.Size()

			opts[numaKey], err = numaConfig.ToString()
			if err != nil {
				return nil, fmt.Errorf("failed to generate NUMA config string for NUMA node %d: %w", i, err)
			}
		}
	}

	return opts, nil
}
