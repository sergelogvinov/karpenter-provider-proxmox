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

	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	resources "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// GetResourceFromVMConfig extracts VMResources from a go-proxmox-rest cluster
// resource listing entry and the guest's qemu Config.
func GetResourceFromVMConfig(vmr *cluster.Resource, cfg *qemu.Config) (opt *resources.VMResources, err error) {
	if vmr == nil || cfg == nil {
		return nil, fmt.Errorf("virtual machine resource and config cannot be nil")
	}

	opt = &resources.VMResources{
		ID:     vmr.VMID,
		CPUs:   vmr.MaxCPU,
		CPUSet: cpuset.New(),
		Memory: uint64(vmr.MaxMem),
	}

	if cfg.Affinity != "" {
		opt.Affinity = cfg.Affinity
		opt.CPUSet, err = cpuset.Parse(cfg.Affinity)
		if err != nil {
			return nil, fmt.Errorf("failed to parse CPU affinity: %w", err)
		}
	}

	if len(cfg.NUMA) == 0 {
		return opt, nil
	}

	opt.NUMANodes = make(map[int]resources.NUMANodeState)

	numaIdx := make([]int, 0, len(cfg.NUMA))
	for idx := range cfg.NUMA {
		numaIdx = append(numaIdx, idx)
	}

	sort.Ints(numaIdx)

	for _, idx := range numaIdx {
		n := cfg.NUMA[idx]

		if n.Memory == nil || *n.Memory <= 0 || len(n.CPUIDs) == 0 || len(n.HostNodes) == 0 {
			continue
		}

		cpus, err := cpuset.Parse(strings.Join(n.CPUIDs, ","))
		if err != nil {
			return nil, fmt.Errorf("failed to parse CPU IDs for NUMA node numa%d: %w", idx, err)
		}

		hostNuma, err := cpuset.Parse(strings.Join(n.HostNodes, ","))
		if err != nil {
			return nil, fmt.Errorf("failed to parse Host Node Names for NUMA node numa%d: %w", idx, err)
		}

		cpuList := cpus.List()

		numHostNodes := hostNuma.Size()
		if numHostNodes == 0 {
			return nil, fmt.Errorf("NUMA host nodes set is empty for NUMA node numa%d", idx)
		}
		if numHostNodes > 1 && len(cpuList)%numHostNodes != 0 {
			return nil, fmt.Errorf("cannot evenly distribute %d CPUs across %d NUMA nodes for NUMA node numa%d", len(cpuList), numHostNodes, idx)
		}

		nodeCpus := cpus.Size() / numHostNodes

		for i, nodeID := range hostNuma.List() {
			old := opt.NUMANodes[nodeID]

			oldCPUs, err := cpuset.Parse(old.CPUs)
			if err != nil {
				return nil, fmt.Errorf("failed to parse existing CPUs for NUMA node %d: %w", nodeID, err)
			}

			opt.NUMANodes[nodeID] = resources.NUMANodeState{
				Memory: old.Memory + uint64(*n.Memory)/uint64(numHostNodes),
				CPUs:   oldCPUs.Union(cpuset.New(cpuList[i*nodeCpus : (i+1)*nodeCpus]...)).String(),
				Policy: n.Policy,
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
			if node.CPUs == "" {
				return nil, fmt.Errorf("NUMA node %d has no CPUs assigned", k)
			}

			numaKey := fmt.Sprintf("numa%d", i)
			memory := int(node.Memory)
			numaConfig := qemu.NUMA{
				Memory:    &memory,
				Policy:    node.Policy,
				HostNodes: []string{fmt.Sprintf("%d", k)},
			}

			cpus, err := cpuset.Parse(node.CPUs)
			if err != nil {
				return nil, fmt.Errorf("failed to parse CPUs for NUMA node %d: %w", k, err)
			}

			numaConfig.CPUIDs = []string{fmt.Sprintf("%d-%d", cpuIdx, cpuIdx+cpus.Size()-1)}
			cpuIdx += cpus.Size()

			opts[numaKey] = numaConfig.String()
		}
	}

	return opts, nil
}
