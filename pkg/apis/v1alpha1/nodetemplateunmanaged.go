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
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProxmoxUnmanagedTemplate is the Schema for the ProxmoxUnmanagedTemplate API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:printcolumn:name="Zones",type="string",JSONPath=".status.resources.zones",description=""
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=".spec.templateName",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:scope=Cluster,categories=karpenter
// +kubebuilder:subresource:status
type ProxmoxUnmanagedTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of ProxmoxUnmanagedTemplateSpec
	Spec ProxmoxUnmanagedTemplateSpec `json:"spec,omitempty"`

	// Status defines the observed state of ProxmoxUnmanagedTemplate
	Status ProxmoxTemplateStatus `json:"status,omitempty"`
}

// ProxmoxUnmanagedTemplateSpec defines the desired state of ProxmoxUnmanagedTemplate
type ProxmoxUnmanagedTemplateSpec struct {
	// Region is the Proxmox Cloud region where VM template will be created
	// +kubebuilder:validation:MinLength=1
	// +optional
	Region string `json:"region,omitempty"`

	// TemplateName is the name of the Proxmox template.
	// +kubebuilder:validation:MinLength=1
	// +required
	TemplateName string `json:"templateName"`
}

func (in *ProxmoxUnmanagedTemplate) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

// ProxmoxUnmanagedTemplateList contains a list of ProxmoxUnmanagedTemplate
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
type ProxmoxUnmanagedTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ProxmoxUnmanagedTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProxmoxUnmanagedTemplate{}, &ProxmoxUnmanagedTemplateList{})
}
