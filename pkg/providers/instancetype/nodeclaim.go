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

package instancetype

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// nodeClaimUse is the instance type and capacity type a NodeClaim is labeled with, and the region and zone it runs in.
type nodeClaimUse struct {
	instanceType, capacityType, region, zone string
}

// listNodeClaimUses lists the instance types the cluster's NodeClaims use, each with its capacity type and where it runs.
func (p *DefaultProvider) listNodeClaimUses(ctx context.Context) ([]nodeClaimUse, error) {
	nodeClaims := &karpv1.NodeClaimList{}
	if err := p.readers.API.List(ctx, nodeClaims); err != nil {
		return nil, fmt.Errorf("listing NodeClaims, %w", err)
	}

	uses := make([]nodeClaimUse, 0, len(nodeClaims.Items))

	for _, nodeClaim := range nodeClaims.Items {
		use := nodeClaimUse{
			instanceType: nodeClaim.Labels[corev1.LabelInstanceTypeStable],
			capacityType: nodeClaim.Labels[karpv1.CapacityTypeLabelKey],
			region:       nodeClaim.Labels[corev1.LabelTopologyRegion],
			zone:         nodeClaim.Labels[corev1.LabelTopologyZone],
		}

		if use.instanceType != "" && use.region != "" && use.zone != "" {
			uses = append(uses, use)
		}
	}

	return uses, nil
}
