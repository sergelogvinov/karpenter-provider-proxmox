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

package proxmox

import (
	"context"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	NodeClassDrift cloudprovider.DriftReason = "NodeClassDrift"
	ImageDrift     cloudprovider.DriftReason = "ImageDrift"
)

func (c *CloudProvider) isNodeClassDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) (cloudprovider.DriftReason, error) {
	checks := []func(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) (cloudprovider.DriftReason, error){
		c.areStaticFieldsDrifted,
		c.isTemplateDrifted,
	}

	for _, check := range checks {
		driftReason, err := check(ctx, nodeClaim, nodeClass)
		if err != nil {
			return "", err
		}
		if driftReason != "" {
			return driftReason, nil
		}
	}

	return "", nil
}

func (c *CloudProvider) areStaticFieldsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) (cloudprovider.DriftReason, error) {
	nodeClassHash, foundNodeClassHash := nodeClass.Annotations[v1alpha1.AnnotationProxmoxNodeClassHash]
	nodeClassHashVersion, foundNodeClassHashVersion := nodeClass.Annotations[v1alpha1.AnnotationProxmoxNodeClassHashVersion]
	nodeClaimHash, foundNodeClaimHash := nodeClaim.Annotations[v1alpha1.AnnotationProxmoxNodeClassHash]
	nodeClaimHashVersion, foundNodeClaimHashVersion := nodeClaim.Annotations[v1alpha1.AnnotationProxmoxNodeClassHashVersion]

	if !foundNodeClassHash || !foundNodeClaimHash || !foundNodeClassHashVersion || !foundNodeClaimHashVersion {
		return "", nil
	}

	if nodeClassHashVersion != nodeClaimHashVersion {
		return "", nil
	}

	if nodeClassHash != nodeClaimHash {
		return NodeClassDrift, nil
	}

	return "", nil
}

func (c *CloudProvider) isTemplateDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) (cloudprovider.DriftReason, error) {
	if nodeClaim.Status.ImageID == "" {
		return "", nil
	}

	_, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return "", nil //nolint: nilerr
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]
	templateIDs := nodeClass.GetTemplateIDs(region)

	templates := c.instanceTemplateProvider.ListWithFilter(ctx, func(c *instancetemplate.InstanceTemplateInfo) bool {
		return c.Region == region && c.Zone == zone && lo.Contains(templateIDs, c.TemplateID)
	})

	if len(templates) == 0 {
		return "", nil //nolint: nilerr
	}

	if templates[0].TemplateHash != nodeClaim.Status.ImageID {
		return ImageDrift, nil
	}

	return "", nil
}
