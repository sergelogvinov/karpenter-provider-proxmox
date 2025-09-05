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

	// Labels that can be selected on and are propagated to the node
	LabelInstanceFamily  = apis.Group + "/instance-family"   // c1, s1, m1, e1
	LabelInstanceCPUType = apis.Group + "/instance-cpu-type" // host, kvm64, Broadwell, Skylake
	LabelInstanceImageID = apis.Group + "/instance-image-id" // image ID

	LabelBootstrapToken = apis.Group + "/bootstrap-token" // bootstrap token

	// github.com/awslabs/eks-node-viewer label so that it shows up.
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
