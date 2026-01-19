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
	"fmt"
	"sort"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

func (c *CloudProvider) resolveInstanceTypes(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		return nil, err
	}

	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)

	return lo.Filter(instanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
		return len(i.Offerings.Compatible(reqs).Available()) > 0 &&
			resources.Fits(nodeClaim.Spec.Resources.Requests, i.Allocatable())
	}), nil
}

func (c *CloudProvider) resolveInstanceTypeFromNode(ctx context.Context, node *corev1.Node) (*cloudprovider.InstanceType, error) {
	if typeLabel, ok := node.Labels[corev1.LabelInstanceTypeStable]; ok {
		instanceType, err := c.instanceTypeProvider.Get(ctx, typeLabel)
		if err == nil {
			return instanceType, nil
		}
	}

	// InstanceType was not found, try to match by capacity
	list := c.instanceTypeProvider.ListWithFilter(ctx, func(it *cloudprovider.InstanceType) bool {
		if it.Offerings == nil || len(it.Offerings.Available()) == 0 || it.Capacity == nil {
			return false
		}

		resources := it.Capacity.Cpu().Cmp(*node.Status.Capacity.Cpu()) >= 0 &&
			it.Capacity.Memory().Cmp(*node.Status.Capacity.Memory()) >= 0

		if it.Capacity.Pods() != nil {
			resources = resources && it.Capacity.Pods().Cmp(*node.Status.Capacity.Pods()) >= 0
		}

		return resources
	})

	if len(list) > 0 {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Offerings.Cheapest().Price < list[j].Offerings.Cheapest().Price
		})

		return list[0], nil
	}

	return nil, fmt.Errorf("instanceType not found for node %s", node.Name)
}

func (c *CloudProvider) nodeToNodeClaim(_ context.Context, instanceType *cloudprovider.InstanceType, node *corev1.Node) (*karpv1.NodeClaim, error) {
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              node.Name,
			CreationTimestamp: metav1.Time{Time: node.CreationTimestamp.Time},
		},
		Status: karpv1.NodeClaimStatus{
			NodeName:   node.Name,
			ProviderID: node.Spec.ProviderID,
		},
	}
	labels := map[string]string{}
	annotations := map[string]string{}

	if instanceType != nil {
		for key, req := range instanceType.Requirements {
			if req.Len() == 1 {
				labels[key] = req.Values()[0]
			}
		}

		nodeClaim.Status.Capacity = lo.PickBy(instanceType.Capacity, func(_ corev1.ResourceName, v resource.Quantity) bool { return !resources.IsZero(v) })
		nodeClaim.Status.Allocatable = lo.PickBy(instanceType.Allocatable(), func(_ corev1.ResourceName, v resource.Quantity) bool { return !resources.IsZero(v) })
	} else {
		nodeClaim.Status.Capacity = node.Status.Capacity
		nodeClaim.Status.Allocatable = node.Status.Allocatable
	}

	for _, key := range karpv1.WellKnownLabels.UnsortedList() {
		if labels[key] == "" && node.Labels[key] != "" {
			labels[key] = node.Labels[key]
		}
	}

	for _, key := range []struct {
		label string
		value string
	}{
		{corev1.LabelArchStable, node.Status.NodeInfo.Architecture},
		{corev1.LabelOSStable, node.Status.NodeInfo.OperatingSystem},
	} {
		if labels[key.label] == "" && key.value != "" {
			labels[key.label] = key.value
		}
	}

	nodeClaim.Annotations = annotations
	nodeClaim.Labels = labels

	if id, ok := node.Labels[v1alpha1.LabelInstanceImageID]; ok {
		nodeClaim.Status.ImageID = id
	}

	return nodeClaim, nil
}
