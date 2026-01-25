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

package topology

import (
	"fmt"

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
	nodesettings "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/nodesettings"
)

// Discover returns MemTopology based on proxmox node info
// We do not have access to real information,
// so, we will predict architecture based on the provided CPUInfo.
func Discover(n *proxmox.Node) (*MemTopology, error) {
	if n == nil {
		return nil, fmt.Errorf("cannot discover memory topology from nil node info")
	}

	st, err := nodesettings.GetNodeSettingByNode(n)
	if err != nil {
		return nil, fmt.Errorf("getting node settings: %w", err)
	}

	if st == nil {
		topology := &MemTopology{
			TotalMemory: n.Memory.Total,
		}

		return topology, nil
	}

	return DiscoverFromSettings(st)
}

// DiscoverFromSettings returns MemTopology based on resourcemanager.NodeSettings
func DiscoverFromSettings(settings *settings.NodeSettings) (*MemTopology, error) {
	if settings == nil || len(settings.NUMANodes) == 0 {
		return nil, fmt.Errorf("could not detect memory topology from incomplete node settings")
	}

	numa := make(map[int]uint64)
	total := uint64(0)

	for i, mem := range settings.NUMANodes {
		total += mem.MemSize
		numa[i] = mem.MemSize
	}

	topology := &MemTopology{
		NUMANodes:   numa,
		TotalMemory: total,
	}

	return topology, nil
}
