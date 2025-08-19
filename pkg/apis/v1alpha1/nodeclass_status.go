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

	corev1 "k8s.io/api/core/v1"
)

const (
	ConditionInstanceTemplateReady        = "InstanceTemplateReady"
	ConditionInstanceMetadataOptionsReady = "InstanceMetadataOptionsReady"
)

// ProxmoxNodeClassStatus defines the observed state of ProxmoxNodeClass
type ProxmoxNodeClassStatus struct {
	// Conditions contains signals for health and readiness
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`

	// Resources is the list of resources that have been provisioned.
	// +optional
	Resources corev1.ResourceList `json:"resources,omitempty"`

	// SelectedZones is a list of nodes that match this node class
	// It depends on instanceTemplate and region.
	// This field is populated by the controller and should not be set manually.
	// +optional
	SelectedZones []string `json:"selectedZones,omitempty"`

	// TaskRef is a reference to the task that is being executed.
	// +optional
	// TaskRef *string `json:"taskRef,omitempty"`
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxNodeClass) StatusConditions() status.ConditionSet {
	conds := []string{
		ConditionInstanceTemplateReady,
		ConditionInstanceMetadataOptionsReady,
	}

	return status.NewReadyConditions(conds...).For(in)
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *ProxmoxNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *ProxmoxNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}
