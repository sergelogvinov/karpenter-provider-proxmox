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
	"strings"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func (c *CloudProvider) nodeToNodeClaim(_ context.Context, node *corev1.Node) (*karpv1.NodeClaim, error) {
	nodeClaim := &karpv1.NodeClaim{}
	labels := map[string]string{}
	annotations := map[string]string{}

	if typeLabel, ok := node.Labels[corev1.LabelInstanceTypeStable]; ok {
		if instanceType, err := instanceTypeByName(c.instanceTypes, typeLabel); err == nil {
			typeName := strings.Split(instanceType.Name, ".")

			labels[corev1.LabelInstanceTypeStable] = instanceType.Name
			labels[v1alpha1.LabelInstanceFamily] = typeName[0]
			labels[v1alpha1.LabelInstanceCPUManufacturer] = node.Labels[v1alpha1.LabelInstanceCPUManufacturer]
			labels[v1alpha1.LabelNodeViewer] = fmt.Sprintf("%f", instanceType.Offerings.Cheapest().Price)
			labels[karpv1.CapacityTypeLabelKey] = node.Labels[karpv1.CapacityTypeLabelKey]

			nodeClaim.Status.Capacity = instanceType.Capacity
			nodeClaim.Status.Allocatable = instanceType.Allocatable()
		} else {
			labels[corev1.LabelInstanceTypeStable] = typeLabel
			labels[v1alpha1.LabelInstanceFamily] = "e1"
			labels[v1alpha1.LabelInstanceCPUManufacturer] = "kvm64"
			labels[karpv1.CapacityTypeLabelKey] = karpv1.CapacityTypeOnDemand

			nodeClaim.Status.Capacity = node.Status.Capacity
			nodeClaim.Status.Allocatable = node.Status.Allocatable
		}

		labels[corev1.LabelArchStable] = node.Status.NodeInfo.Architecture
		labels[corev1.LabelOSStable] = node.Status.NodeInfo.OperatingSystem
	}

	labels[corev1.LabelTopologyRegion] = node.Labels[corev1.LabelTopologyRegion]
	labels[corev1.LabelTopologyZone] = node.Labels[corev1.LabelTopologyZone]

	nodeClaim.Name = node.Name
	nodeClaim.Labels = labels
	nodeClaim.Annotations = annotations
	nodeClaim.CreationTimestamp = metav1.Time{Time: node.CreationTimestamp.Time}

	nodeClaim.Status.NodeName = node.Name
	nodeClaim.Status.ProviderID = node.Spec.ProviderID

	return nodeClaim, nil
}
