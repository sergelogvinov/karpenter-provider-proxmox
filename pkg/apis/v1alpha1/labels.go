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

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	TerminationFinalizer = apis.Group + "/termination"

	// LabelInstanceFamily is the instance family, e.g. c1, s1, m1, e1
	LabelInstanceFamily = apis.Group + "/instance-family" // c1, s1, m1, e1
	// LabelInstanceCPUType is the CPU type, e.g. host, kvm64, Broadwell, Skylake
	LabelInstanceCPUType = apis.Group + "/instance-cpu-type" // host, kvm64, Broadwell, Skylake
	// LabelInstanceImageID is the image ID
	LabelInstanceImageID = apis.Group + "/instance-image-id" // image ID

	// LabelBootstrapToken is the bootstrap token name used to join the node to the cluster
	LabelBootstrapToken = apis.Group + "/bootstrap-token" // bootstrap token

	// LabelNodeViewer is the label used by github.com/awslabs/eks-node-viewer
	LabelNodeViewer = "eks-node-viewer/instance-price"
)

func init() {
	karpv1.RestrictedLabelDomains = karpv1.RestrictedLabelDomains.Insert(apis.Group)
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Insert(
		LabelInstanceFamily,
		LabelInstanceCPUType,
		LabelInstanceImageID,
	)
}
