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

package resourcemanager

import (
	"context"
	"fmt"
	"maps"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/nodesettings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/nodecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"

	"k8s.io/utils/cpuset"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Node CPU policies, as named by the --node-policy option.
const (
	policyStatic                  = "static"
	policySimpleWithCPUOvercommit = "simple-with-cpu-overcommit"
)

// ZoneCapacityFromCluster discovers a zone's host topology and settings and returns
// its empty capacity, placing new VMs as the node policy option says.
func ZoneCapacityFromCluster(ctx context.Context, cl *proxmoxrest.Client, region, zone string) (*ZoneCapacity, error) {
	log := log.FromContext(ctx).WithName("ZoneCapacity").WithValues("node", zone)

	opts := options.FromContext(ctx)
	if opts == nil {
		return nil, fmt.Errorf("missing options in context")
	}

	nodeSettings, err := nodeSettingsFromCluster(ctx, cl, zone)
	if err != nil {
		return nil, fmt.Errorf("failed to get node settings from VM: %w", err)
	}

	if name := opts.NodeSettingFilePath; name != "" {
		setting, err := settings.LoadNodeSettingsFromFile(name, region, zone)
		if err != nil {
			return nil, err
		}

		if setting != nil {
			if setting.NumSockets != 0 {
				nodeSettings.NumSockets = setting.NumSockets
			}

			if setting.NumThreads != 0 {
				nodeSettings.NumThreads = setting.NumThreads
			}

			if setting.NumUncoreCaches != 0 {
				nodeSettings.NumUncoreCaches = setting.NumUncoreCaches
			}

			if len(setting.ReservedCPUs) != 0 {
				nodeSettings.ReservedCPUs = setting.ReservedCPUs
			}

			if setting.ReservedMemory != 0 {
				nodeSettings.ReservedMemory = setting.ReservedMemory
			}

			if len(setting.NUMANodes) != 0 {
				nodeSettings.NUMANodes = setting.NUMANodes
			}

			if setting.HugePageSize != 0 {
				nodeSettings.HugePageSize = setting.HugePageSize
			}

			log.V(1).Info("Loaded node settings from file", "file", name, "settings", nodeSettings)
		}
	}

	sysTopology, err := topology.DiscoverFromSettings(&nodeSettings)
	if err != nil {
		return nil, fmt.Errorf("failed to discover topology from settings for node %s: %w", zone, err)
	}

	policy := zonecapacity.Policy{Kind: zonecapacity.PolicySimple}

	switch opts.NodePolicy {
	case policyStatic:
		policy = zonecapacity.Policy{Kind: zonecapacity.PolicyStatic}
	case policySimpleWithCPUOvercommit:
		policy = zonecapacity.Policy{Kind: zonecapacity.PolicyOvercommit, CPUOvercommitRatio: opts.CPUOvercommitRatio}
	}

	capacity, err := NewZoneCapacity(zonecapacity.Host{
		Topology:       sysTopology,
		ReservedCPUs:   cpuset.New(nodeSettings.ReservedCPUs...),
		ReservedMemory: nodeSettings.ReservedMemory,
		HugePageSize:   nodeSettings.HugePageSize,
	}, policy)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s zone capacity for node %s: %w", opts.NodePolicy, zone, err)
	}

	log.V(1).Info("Created zone capacity",
		"capacity", capacity.String(),
		"settings", nodeSettings,
		"policy", opts.NodePolicy,
	)

	return capacity, nil
}

func nodeSettingsFromCluster(ctx context.Context, cl *proxmoxrest.Client, zone string) (settings.NodeSettings, error) {
	nodeSettings := settings.NodeSettings{
		ReservedMemory: 1024 * 1024 * 1024, // 1GiB
	}

	n, err := cl.Nodes(zone).Status(ctx)
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get node %s: %w", zone, err)
	}

	st, err := nodesettings.GetNodeSettingByStatus(n)
	if err != nil {
		return nodeSettings, fmt.Errorf("getting node settings: %w", err)
	}

	if st != nil {
		nodeSettings = *st
		nodeSettings.ReservedMemory = 1024 * 1024 * 1024 // 1GiB
	}

	vmrs, err := cl.Cluster().Resources().List(ctx, cluster.ListFilter{
		Type:          cluster.ResourceTypeVM,
		GuestType:     "qemu",
		Node:          zone,
		SkipTemplates: true,
		Match: func(r *cluster.Resource) (bool, error) {
			return nodecapacity.IsGuest(r.Name, r.Tags), nil
		},
	})
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get VM by filter for node settings: %w", err)
	}

	if len(vmrs) == 0 {
		return nodeSettings, nil
	}

	cfg, err := cl.Nodes(vmrs[0].Node).Qemu().Config(ctx, vmrs[0].VMID, nil)
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get VM config for node settings: %w", err)
	}

	err = nodeSettingsFromGuest(cfg, &nodeSettings)
	if err != nil {
		return nodeSettings, fmt.Errorf("failed to get node settings from VM: %w", err)
	}

	return nodeSettings, nil
}

// nodeSettingsFromGuest merges what the node-capacity guest says of its zone into
// nodeSettings. The guest is the source of truth, so a pool it no longer has clears
// the page size, and a pool takes no host reserve: the host's own memory lies
// outside it.
func nodeSettingsFromGuest(cfg *qemu.Config, nodeSettings *settings.NodeSettings) error {
	if cfg == nil || nodeSettings == nil {
		return fmt.Errorf("invalid input: config and nodeSettings cannot be nil")
	}

	guest, err := nodecapacity.Decode(cfg)
	if err != nil {
		return fmt.Errorf("failed to read the node-capacity guest: %w", err)
	}

	if nodeSettings.NUMANodes == nil {
		nodeSettings.NUMANodes = make(settings.NUMANodes, len(guest.NUMANodes))
	}

	nodeSettings.HugePageSize = guest.HugePageSize
	if guest.HugePageSize > 0 {
		nodeSettings.ReservedMemory = 0
	}

	maps.Copy(nodeSettings.NUMANodes, guest.NUMANodes)

	if len(nodeSettings.NUMANodes) == 0 && !guest.CPUs.IsEmpty() {
		nodeSettings.NUMANodes[0] = settings.NUMAInfo{
			CPUs:    guest.CPUs.String(),
			MemSize: guest.Memory,
		}
	}

	return nil
}
