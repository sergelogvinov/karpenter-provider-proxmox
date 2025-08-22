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
	"strings"

	"github.com/awslabs/operatorpkg/status"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"

	corev1 "k8s.io/api/core/v1"
)

const (
	ConditionTemplateImageReady = "ProxmoxVirtualMachineTemplateImageReady"

	ConditionTemplateReady = "ProxmoxVirtualMachineTemplateReady"
)

// ProxmoxTemplateStatus defines the observed state of Proxmox Templates
type ProxmoxTemplateStatus struct {
	// Conditions contains signals for health and readiness
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`

	// Resources is the list of resources that have been provisioned.
	// +optional
	Resources corev1.ResourceList `json:"resources,omitempty"`

	// Zones is a list of nodes that VM template was prepared.
	// This field is populated by the controller and should not be set manually.
	// +optional
	Zones []string `json:"zones,omitempty"`

	// ImageID is the ID of the image.
	// +optional
	ImageID string `json:"imageID,omitempty"`
}

// ProxmoxCommonTemplate is an interface that both ProxmoxTemplate and ProxmoxUnmanagedTemplate implement
// +k8s:deepcopy-gen=false
type ProxmoxCommonTemplate interface {
	GetStatus() *ProxmoxTemplateStatus
	GetZones() []string
	GetImageID() string
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxTemplate) StatusConditions() status.ConditionSet {
	conds := []string{
		ConditionTemplateReady,
	}

	return status.NewReadyConditions(conds...).For(in)
}

func (in *ProxmoxTemplate) GetStatus() *ProxmoxTemplateStatus {
	status := in.Status

	return &status
}

func (in *ProxmoxTemplate) GetZones() []string {
	return in.Status.Zones
}

func (in *ProxmoxTemplate) GetImageID() string {
	filenameParts := strings.SplitN(in.Spec.SourceImage.ImageName, ".", 2)
	imageID := fmt.Sprintf(
		"%s-%d.%s", filenameParts[0],
		lo.Must(hashstructure.Hash(in.Spec.SourceImage.URL, hashstructure.FormatV2, nil)),
		filenameParts[1],
	)

	return imageID
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *ProxmoxTemplate) GetConditions() []status.Condition {
	return in.Status.Conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *ProxmoxTemplate) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}

func (in *ProxmoxUnmanagedTemplate) GetStatus() *ProxmoxTemplateStatus {
	status := in.Status

	return &status
}

func (in *ProxmoxUnmanagedTemplate) GetZones() []string {
	return in.Status.Zones
}

func (in *ProxmoxUnmanagedTemplate) GetImageID() string {
	return in.Status.ImageID
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) StatusConditions() status.ConditionSet {
	conds := []string{
		ConditionTemplateReady,
	}

	return status.NewReadyConditions(conds...).For(in)
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) GetConditions() []status.Condition {
	return in.Status.Conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}
