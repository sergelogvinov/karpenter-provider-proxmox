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
	"strconv"
	"strings"

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
}

// GetZones returns the zones that are selected for this node class
func (in *ProxmoxNodeClass) GetZones(region string) []string {
	zones := []string{}

	for _, selectedZone := range in.Status.SelectedZones {
		if !strings.HasPrefix(selectedZone, region+"/") {
			continue
		}

		p := strings.SplitN(selectedZone, "/", 3)
		if len(p) == 3 {
			zones = append(zones, p[1])
		}
	}

	return zones
}

// GetTemplateIDs returns the template IDs that are selected for this node class
func (in *ProxmoxNodeClass) GetTemplateIDs(region string) []uint64 {
	ids := []uint64{}

	for _, selectedZone := range in.Status.SelectedZones {
		if !strings.HasPrefix(selectedZone, region+"/") {
			continue
		}

		p := strings.SplitN(selectedZone, "/", 3)
		if len(p) == 3 {
			id, _ := strconv.Atoi(p[2])
			ids = append(ids, uint64(id))
		}
	}

	return ids
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
