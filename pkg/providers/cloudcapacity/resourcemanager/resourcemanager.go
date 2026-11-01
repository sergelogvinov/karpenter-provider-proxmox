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

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/nodecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/zonesettings"

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

	status, guest, err := zoneFromCluster(ctx, cl, zone)
	if err != nil {
		return nil, err
	}

	file, err := settings.LoadNodeSettingsFromFile(opts.NodeSettingFilePath, region, zone)
	if err != nil {
		return nil, err
	}

	if file != nil {
		log.V(1).Info("Loaded node settings from file", "file", opts.NodeSettingFilePath, "settings", file)
	}

	nodeSettings, err := zonesettings.Resolve(status, guest, file)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the zone settings of node %s: %w", zone, err)
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

// zoneFromCluster reads what Proxmox says of a zone: its node status and, when the
// scheduler keeps one there, the node-capacity guest's config.
func zoneFromCluster(ctx context.Context, cl *proxmoxrest.Client, zone string) (*nodes.Status, *qemu.Config, error) {
	status, err := cl.Nodes(zone).Status(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get node %s: %w", zone, err)
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
		return nil, nil, fmt.Errorf("failed to get VM by filter for node settings: %w", err)
	}

	if len(vmrs) == 0 {
		return status, nil, nil
	}

	cfg, err := cl.Nodes(vmrs[0].Node).Qemu().Config(ctx, vmrs[0].VMID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get VM config for node settings: %w", err)
	}

	return status, cfg, nil
}
