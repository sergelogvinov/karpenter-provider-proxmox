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

package instance

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// vmLocation is where a NodeClaim's virtual machine runs, with the client
// for its region.
type vmLocation struct {
	px     *proxmoxrest.Client
	region string
	zone   string
	vm     *cluster.Resource
}

// locate finds the virtual machine behind nodeClaim. The VM id and region
// come from the provider ID, the region falling back to the NodeClaim's
// region label when the provider ID has none. The zone comes from Proxmox,
// not the zone label, which goes stale once the VM migrates; the label is
// used only if Proxmox reports no node. A VM Proxmox doesn't know is a
// NodeClaimNotFoundError.
func (p *DefaultProvider) locate(ctx context.Context, nodeClaim *karpv1.NodeClaim) (vmLocation, error) {
	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return vmLocation{}, fmt.Errorf("failed to parse providerID: %w", err)
	}

	if region == "" {
		region = nodeClaim.Labels[corev1.LabelTopologyRegion]
	}

	px, err := p.cluster.Get(region)
	if err != nil {
		return vmLocation{}, fmt.Errorf("region %q: %w", region, err)
	}

	vm, err := p.cluster.Cluster(region).Get(ctx, pxpool.ResourceKindVM, strconv.Itoa(vmid))
	if err != nil {
		if errors.Is(err, pxpool.ErrResourceNotFound) {
			return vmLocation{}, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("vm %d in region %s: %w", vmid, region, err))
		}

		return vmLocation{}, fmt.Errorf("failed to get vm %d in region %s: %w", vmid, region, err)
	}

	zone := vm.Node
	if zone == "" {
		zone = nodeClaim.Labels[corev1.LabelTopologyZone]
	}

	return vmLocation{px: px, region: region, zone: zone, vm: vm}, nil
}
