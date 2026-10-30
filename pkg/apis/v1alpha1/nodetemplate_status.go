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

	// ReasonTemplateCPUAffinitySet is the ConditionTemplateReady reason when it
	// is false because, without root, every matching VM template is dropped
	// for having affinity set (ADR 0003).
	ReasonTemplateCPUAffinitySet = "TemplateCPUAffinitySet"
	// ReasonTemplateCPUAffinityDropped is the ConditionTemplateReady reason when
	// it is true but, without root, some matching VM templates are dropped
	// for having affinity set (ADR 0003).
	ReasonTemplateCPUAffinityDropped = "TemplateCPUAffinityDropped"

	// ConditionTemplateConsistent is true when the VM templates at the
	// template placements in Zones agree on the hugepages and affinity keys
	// of their VM config, false when any two of them disagree (also
	// two in one zone), and unknown while the keys of some VM template are
	// not known. It is not a Ready dependent: as root the controller sets
	// these keys itself on launch (ADR 0003).
	ConditionTemplateConsistent = "ProxmoxVirtualMachineTemplateConsistent"
)

// VMTemplateInfo records the hugepages and affinity keys of a VM template's
// VM config, which decide, without root, which instance types a
// NodeClass using the template can launch (ADR 0003).
type VMTemplateInfo struct {
	// Hugepages is the hugepages key of the VM config: the page size
	// in MiB ("2" or "1024"), or "any". Missing or empty means the VM
	// template has no hugepages.
	// +optional
	Hugepages string `json:"hugepages,omitempty"`

	// Affinity reports whether the affinity key of the VM config is set.
	Affinity bool `json:"affinity"`
}

// PlacedVMTemplate is the VM template at one template placement and the
// hugepages and affinity keys of its VM config, nil when unknown.
// +k8s:deepcopy-gen=false
type PlacedVMTemplate struct {
	Placement  TemplatePlacement
	VMTemplate *VMTemplateInfo
}

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

	// Template records the hugepages and affinity keys of the VM
	// config that the VM templates at the template placements in Zones
	// share. It is unset while there are no VM templates or the keys of one
	// are unknown, and when the VM templates disagree, which the
	// ProxmoxVirtualMachineTemplateConsistent condition reports.
	// This field is populated by the controller and should not be set manually.
	// +optional
	Template *VMTemplateInfo `json:"template,omitempty"`
}

// ProxmoxCommonTemplate is an interface that both ProxmoxTemplate and ProxmoxUnmanagedTemplate implement
// +k8s:deepcopy-gen=false
type ProxmoxCommonTemplate interface {
	GetStatus() *ProxmoxTemplateStatus
	TemplatePlacements() []TemplatePlacement
	SetTemplatePlacements(placements []TemplatePlacement)
	GetImageID() string
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxTemplate) StatusConditions(opts ...status.ForOption) status.ConditionSet {
	conds := []string{
		ConditionTemplateReady,
	}

	return status.NewReadyConditions(conds...).For(in, opts...)
}

func (in *ProxmoxTemplate) GetStatus() *ProxmoxTemplateStatus {
	status := in.Status

	return &status
}

// TemplatePlacements returns the template placements in Status.Zones,
// skipping malformed entries.
func (in *ProxmoxTemplate) TemplatePlacements() []TemplatePlacement {
	return parseTemplatePlacements(in.Status.Zones, "")
}

// SetTemplatePlacements stores placements in Status.Zones.
func (in *ProxmoxTemplate) SetTemplatePlacements(placements []TemplatePlacement) {
	in.Status.Zones = formatTemplatePlacements(placements)
}

// SetVMTemplates stores the placements of templates in Status.Zones and
// records their hugepages and affinity keys, see setVMTemplates.
func (in *ProxmoxTemplate) SetVMTemplates(templates []PlacedVMTemplate) {
	setVMTemplates(&in.Status, in.StatusConditions(), templates)
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

// TemplatePlacements returns the template placements in Status.Zones,
// skipping malformed entries.
func (in *ProxmoxUnmanagedTemplate) TemplatePlacements() []TemplatePlacement {
	return parseTemplatePlacements(in.Status.Zones, "")
}

// SetTemplatePlacements stores placements in Status.Zones.
func (in *ProxmoxUnmanagedTemplate) SetTemplatePlacements(placements []TemplatePlacement) {
	in.Status.Zones = formatTemplatePlacements(placements)
}

// SetVMTemplates stores the placements of templates in Status.Zones and
// records their hugepages and affinity keys, see setVMTemplates.
func (in *ProxmoxUnmanagedTemplate) SetVMTemplates(templates []PlacedVMTemplate) {
	setVMTemplates(&in.Status, in.StatusConditions(), templates)
}

// setVMTemplates stores the placements of templates in templateStatus.Zones,
// the hugepages and affinity keys they share in templateStatus.Template,
// and whether they share them in the ConditionTemplateConsistent condition
// of conditions. Without VM templates both are cleared; when the keys of
// one are unknown, or the VM templates disagree, Template is cleared
// rather than one of them picked.
func setVMTemplates(templateStatus *ProxmoxTemplateStatus, conditions status.ConditionSet, templates []PlacedVMTemplate) {
	templateStatus.Zones = formatTemplatePlacements(lo.Map(templates, func(t PlacedVMTemplate, _ int) TemplatePlacement {
		return t.Placement
	}))
	templateStatus.Template = nil

	if len(templates) == 0 {
		_ = conditions.Clear(ConditionTemplateConsistent)

		return
	}

	if unknown := lo.Filter(templates, func(t PlacedVMTemplate, _ int) bool { return t.VMTemplate == nil }); len(unknown) > 0 {
		conditions.SetUnknownWithReason(ConditionTemplateConsistent, "VMTemplatesUnknown",
			"hugepages and affinity are not known for VM templates: "+strings.Join(lo.Map(unknown, func(t PlacedVMTemplate, _ int) string {
				return t.Placement.String()
			}), ", "))

		return
	}

	first := *templates[0].VMTemplate

	for _, t := range templates[1:] {
		if *t.VMTemplate != first {
			conditions.SetFalse(ConditionTemplateConsistent, "VMTemplatesDisagree", disagreement(templates))

			return
		}
	}

	templateStatus.Template = &first

	conditions.SetTrue(ConditionTemplateConsistent)
}

// disagreement describes the hugepages and affinity keys of each of
// templates, for the message of a false ConditionTemplateConsistent.
func disagreement(templates []PlacedVMTemplate) string {
	placements := make([]string, 0, len(templates))

	for _, t := range templates {
		hugepages := t.VMTemplate.Hugepages
		if hugepages == "" {
			hugepages = "none"
		}

		placements = append(placements, fmt.Sprintf("%s: hugepages=%s affinity=%t", t.Placement, hugepages, t.VMTemplate.Affinity))
	}

	return "VM templates at these template placements disagree on hugepages or affinity: " + strings.Join(placements, ", ")
}

func (in *ProxmoxUnmanagedTemplate) GetImageID() string {
	return in.Status.ImageID
}

// StatusConditions returns the condition set for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) StatusConditions(opts ...status.ForOption) status.ConditionSet {
	conds := []string{
		ConditionTemplateReady,
	}

	return status.NewReadyConditions(conds...).For(in, opts...)
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) GetConditions() []status.Condition {
	return in.Status.Conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *ProxmoxUnmanagedTemplate) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}
