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

	"github.com/awslabs/operatorpkg/status"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Placement strategy
	PlacementStrategyAvailabilityFirst = "AvailabilityFirst"
	PlacementStrategyBalanced          = "Balanced"

	// Resource names for ProxmoxNodeClass status
	ResourceZones corev1.ResourceName = "zones"
)

// ProxmoxNodeClass is the Schema for the ProxmoxNodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:printcolumn:name="Zones",type="string",JSONPath=".status.resources.zones",description=""
// +kubebuilder:printcolumn:name="Balance",type="string",JSONPath=".spec.placementStrategy.zoneBalance",description=""
// +kubebuilder:printcolumn:name="Template",type="string",JSONPath=".spec.instanceTemplate.name",description=""
// +kubebuilder:printcolumn:name="Metadata",type="string",JSONPath=".spec.metadataOptions.type",description=""
// +kubebuilder:printcolumn:name="Disk",type="string",JSONPath=".spec.bootDevice.size",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:scope=Cluster,categories=karpenter
// +kubebuilder:subresource:status
type ProxmoxNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of ProxmoxNodeClass
	Spec ProxmoxNodeClassSpec `json:"spec,omitempty"`

	// Status defines the observed state of ProxmoxNodeClass
	Status ProxmoxNodeClassStatus `json:"status,omitempty"`
}

// ProxmoxNodeClassSpec defines the desired state of ProxmoxNodeClass
type ProxmoxNodeClassSpec struct {
	// Region is the Proxmox Cloud region where nodes will be created
	// +optional
	Region string `json:"region"`

	// PlacementStrategy defines how nodes should be placed across zones
	// +kubebuilder:default={"zoneBalance":"Balanced"}
	// +optional
	PlacementStrategy *PlacementStrategy `json:"placementStrategy,omitempty"`

	// InstanceTemplate is the template of the VM to create
	// +required
	InstanceTemplate *InstanceTemplate `json:"instanceTemplate"`

	// BootDevice defines the root device for the VM
	// If not specified, a block storage device will be used from the instance template.
	// +kubebuilder:default={"size":"30G"}
	// +optional
	BootDevice *BlockDevice `json:"bootDevice"`

	// Tags to apply to the VMs
	// +optional
	Tags []string `json:"tags,omitempty"`

	// MetadataOptions for the generated launch template of provisioned nodes.
	// +kubebuilder:default={"type":"none"}
	// +optional
	MetadataOptions *MetadataOptions `json:"metadataOptions,omitempty"`

	// SecurityGroups to apply to the VMs
	// +kubebuilder:validation:MaxItems:=10
	// +optional
	SecurityGroups []SecurityGroupsTerm `json:"securityGroups,omitempty"`
}

// PlacementStrategy defines how nodes should be placed across zones
type PlacementStrategy struct {
	// ZoneBalance determines how nodes are distributed across zones
	// Valid values are:
	// - "Balanced" (default) - Nodes are evenly distributed across zones
	// - "AvailabilityFirst" - Prioritize zone availability over even distribution
	// +kubebuilder:default=Balanced
	// +kubebuilder:validation:Enum=Balanced;AvailabilityFirst
	// +optional
	ZoneBalance string `json:"zoneBalance,omitempty"`
}

// BlockDevice defines the block device configuration for the VM
type BlockDevice struct {
	// Size is the size of the block device in `Gi`, `G`, `Ti`, or `T`
	// +kubebuilder:validation:Type:=string
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// Storage is the proxmox storage-id to create the block device
	// +kubebuilder:validation:Type:=string
	// +kubebuilder:validation:MaxLength=30
	// +optional
	Storage string `json:"storage,omitempty"`
}

type InstanceTemplate struct {
	// Type is the type of the instance template
	// +kubebuilder:validation:Enum={template}
	// +required
	Type string `json:"type"`

	// Name is the name of the instance template
	// +required
	Name string `json:"name"`
}

// MetadataOptions contains parameters for specifying the exposure of the
// Instance Metadata Service to provisioned VMs.
type MetadataOptions struct {
	// If specified, the instance metadata will be exposed to the VMs by CDRom
	// or virtual machine template.
	// +kubebuilder:default=none
	// +kubebuilder:validation:Enum:={none,cdrom}
	// +optional
	Type *string `json:"type,omitempty"`
	// Name is the name of the configMap or Secrets that contains the metadata.
	Name *string `json:"name,omitempty"`
}

// SecurityGroupsTerm defines a term to apply security groups
type SecurityGroupsTerm struct {
	// Interface is the network interface to apply the security group
	// +kubebuilder:default=net0
	// +kubebuilder:validation:Pattern:="net[0-9]+"
	// +optional
	Interface string `json:"interface,omitempty"`
	// Name is the security group name in Proxmox.
	// +kubebuilder:validation:MaxLength=30
	// +required
	Name string `json:"name,omitempty"`
}

// ProxmoxNodeClassStatus defines the observed state of ProxmoxNodeClass
type ProxmoxNodeClassStatus struct {
	// LastValidationTime is the last time the nodeclass was validated
	// +optional
	LastValidationTime metav1.Time `json:"lastValidationTime,omitempty"`

	// ValidationError contains the error message from the last validation
	// +optional
	ValidationError string `json:"validationError,omitempty"`

	// Resources is the list of resources that have been provisioned.
	// +optional
	Resources corev1.ResourceList `json:"resources,omitempty"`

	// SelectedZones is a list of nodes that match this node class
	// It depends on instanceTemplate and region.
	// This field is populated by the controller and should not be set manually.
	// +optional
	SelectedZones []string `json:"selectedZones,omitempty"`

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
			Status:             c.Status,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		})
	}

	in.Status.Conditions = metav1Conditions
}

func (in *ProxmoxNodeClass) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
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
