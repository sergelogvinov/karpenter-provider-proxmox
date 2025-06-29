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

package v1alpha1

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	// Labels that can be selected on and are propagated to the node
	LabelInstanceFamily          = apis.Group + "/instance-family"           // c1, s1, m1, e1
	LabelInstanceCPUManufacturer = apis.Group + "/instance-cpu-manufacturer" // host, kvm64, Broadwell, Skylake
	LabelInstanceCPU             = apis.Group + "/instance-cpu"              // 1, 2, 4, 8
	LabelInstanceMemory          = apis.Group + "/instance-memory"           // 1Gi, 2Gi, 4Gi, 8Gi

	// github.com/awslabs/eks-node-viewer label so that it shows up.
	LabelNodeViewer = "eks-node-viewer/instance-price"

	// Internal labels that are propagated to the node
	ProxmoxLabelKey   = apis.Group + "/node"
	ProxmoxLabelValue = "owned"
)

func init() {
	v1.RestrictedLabelDomains = v1.RestrictedLabelDomains.Insert(apis.Group)
	v1.WellKnownLabels = v1.WellKnownLabels.Insert(
		LabelInstanceFamily,
		LabelInstanceCPUManufacturer,
		LabelInstanceCPU,
		LabelInstanceMemory,
	)
}
