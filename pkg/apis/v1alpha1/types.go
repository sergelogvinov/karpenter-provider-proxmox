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
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProxmoxNodeClass is the Schema for the ProxmoxNodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type ProxmoxNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of ProxmoxNodeClass
	Spec ProxmoxNodeClassSpec `json:"spec,omitempty"`

	// Status defines the observed state of ProxmoxNodeClass
	Status ProxmoxNodeClassStatus `json:"status,omitempty"`
}

// PlacementStrategy defines how nodes should be placed across zones
type PlacementStrategy struct {
	// ZoneBalance determines how nodes are distributed across zones
	// Valid values are:
	// - "Balanced" (default) - Nodes are evenly distributed across zones
	// - "AvailabilityFirst" - Prioritize zone availability over even distribution
	// +optional
	// +kubebuilder:validation:Enum=Balanced;AvailabilityFirst
	// +kubebuilder:default=Balanced
	ZoneBalance string `json:"zoneBalance,omitempty"`
}

// InstanceTypeRequirements defines criteria for automatic instance type selection
type InstanceTypeRequirements struct {
	// MinimumCPU specifies the minimum number of CPUs required
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumCPU int32 `json:"minimumCPU,omitempty"`

	// MinimumMemory specifies the minimum amount of memory in GiB
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumMemory int32 `json:"minimumMemory,omitempty"`
}

// ProxmoxNodeClassSpec defines the desired state of ProxmoxNodeClass
type ProxmoxNodeClassSpec struct {
	// Region is the Proxmox Cloud region where nodes will be created
	// +optional
	Region string `json:"region"`

	// Zone is the availability zone where nodes will be created
	// If not specified, zones will be automatically selected based on placement strategy
	// +optional
	Zone string `json:"zone,omitempty"`

	// InstanceRequirements defines requirements for automatic instance type selection
	// Only used when InstanceProfile is not specified
	// +optional
	InstanceRequirements *InstanceTypeRequirements `json:"instanceRequirements,omitempty"`

	// Template is the name of the template to use for nodes
	// +required
	Template string `json:"template"`

	// PlacementStrategy defines how nodes should be placed across zones and subnets
	// Only used when Zone or Subnet is not specified
	// +optional
	PlacementStrategy *PlacementStrategy `json:"placementStrategy,omitempty"`

	// Tags to apply to the instances
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

// ProxmoxNodeClassStatus defines the observed state of ProxmoxNodeClass
type ProxmoxNodeClassStatus struct {
	// SpecHash is a hash of the ProxmoxNodeClass spec
	// +optional
	SpecHash uint64 `json:"specHash,omitempty"`

	// LastValidationTime is the last time the nodeclass was validated
	// +optional
	LastValidationTime metav1.Time `json:"lastValidationTime,omitempty"`

	// ValidationError contains the error message from the last validation
	// +optional
	ValidationError string `json:"validationError,omitempty"`

	// SelectedInstanceTypes contains the list of instance types that meet the requirements
	// Only populated when using automatic instance type selection
	// +optional
	SelectedInstanceTypes []string `json:"selectedInstanceTypes,omitempty"`

	// Conditions contains signals for health and readiness
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxNodeClass) StatusConditions() status.ConditionSet {
	return status.NewReadyConditions().For(in)
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *ProxmoxNodeClass) GetConditions() []status.Condition {
	conditions := make([]status.Condition, 0, len(in.Status.Conditions))
	for _, c := range in.Status.Conditions {
		conditions = append(conditions, status.Condition{
			Type:               c.Type,
			Status:             c.Status, // Use c.Status directly as it's already a string-like value
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		})
	}
	return conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *ProxmoxNodeClass) SetConditions(conditions []status.Condition) {
	metav1Conditions := make([]metav1.Condition, 0, len(conditions))
	for _, c := range conditions {
		if c.LastTransitionTime.IsZero() {
			continue
		}
		metav1Conditions = append(metav1Conditions, metav1.Condition{
			Type:               c.Type,
			Status:             metav1.ConditionStatus(c.Status),
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		})
	}
	in.Status.Conditions = metav1Conditions
}

// ProxmoxNodeClassList contains a list of ProxmoxNodeClass
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
type ProxmoxNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ProxmoxNodeClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProxmoxNodeClass{}, &ProxmoxNodeClassList{})
}
