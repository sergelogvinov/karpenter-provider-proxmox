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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PlacementStrategyAvailabilityFirst strategy prioritizes zone availability over even distribution
	PlacementStrategyAvailabilityFirst = "AvailabilityFirst"
	// PlacementStrategyBalanced strategy prioritizes even distribution across zones
	PlacementStrategyBalanced = "Balanced"

	// ResourceZones names for ProxmoxNodeClass status
	ResourceZones corev1.ResourceName = "zones"
)

// ProxmoxNodeClass is the Schema for the ProxmoxNodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:printcolumn:name="Zones",type="string",JSONPath=".status.resources.zones",description=""
// +kubebuilder:printcolumn:name="Balance",type="string",JSONPath=".spec.placementStrategy.zoneBalance",description=""
// +kubebuilder:printcolumn:name="Template",type="string",JSONPath=".spec.instanceTemplateRef.name",description=""
// +kubebuilder:printcolumn:name="Metadata",type="string",JSONPath=".spec.metadataOptions.type",description=""
// +kubebuilder:printcolumn:name="Disk",type="string",JSONPath=".spec.bootDevice.size",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:scope=Cluster,categories=karpenter
// +kubebuilder:subresource:status
type ProxmoxNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"` //nolint:modernize

	// Spec defines the desired state of ProxmoxNodeClass
	Spec ProxmoxNodeClassSpec `json:"spec,omitempty"` //nolint:modernize

	// Status defines the observed state of ProxmoxNodeClass
	Status ProxmoxNodeClassStatus `json:"status,omitempty"` //nolint:modernize
}

// ProxmoxNodeClassSpec defines the desired state of ProxmoxNodeClass
type ProxmoxNodeClassSpec struct {
	// Region is the Proxmox Cloud region where nodes will be created
	// +kubebuilder:validation:MinLength=1
	// +optional
	Region string `json:"region,omitempty"`

	// PlacementStrategy defines how nodes should be placed across zones
	// +kubebuilder:default={"zoneBalance":"Balanced"}
	// +optional
	PlacementStrategy *PlacementStrategy `json:"placementStrategy,omitempty"`

	// InstanceTemplateRef is the template reference for the VM template
	// +required
	InstanceTemplateRef *InstanceTemplateClassReference `json:"instanceTemplateRef,omitempty"`

	// KubeletConfiguration defines kubelet config file
	// +optional
	KubeletConfiguration *KubeletConfiguration `json:"kubeletConfiguration,omitempty"`

	// BootDevice defines the root device for the VM
	// If not specified, a block storage device will be used from the instance template.
	// +kubebuilder:default={"size":"30G"}
	// +optional
	BootDevice *BlockDevice `json:"bootDevice"`

	// Tags to apply to the VMs
	// +kubebuilder:validation:MaxItems:=10
	// +optional
	Tags []string `json:"tags,omitempty" hash:"ignore"`

	// MetadataOptions for the generated launch template of provisioned nodes.
	// +kubebuilder:default={"type":"none"}
	// +optional
	MetadataOptions *MetadataOptions `json:"metadataOptions,omitempty" hash:"ignore"`

	// SecurityGroups to apply to the VMs
	// +kubebuilder:validation:MaxItems:=10
	// +optional
	SecurityGroups []SecurityGroups `json:"securityGroups,omitempty" hash:"ignore"`

	// ResourcePool is the Proxmox resource pool name where VMs will be placed.
	// If specified, cloned VMs will be added to this pool during creation.
	// The pool must already exist in Proxmox.
	// Supports nested pools up to 3 levels (e.g., "parent/child/grandchild").
	// Note: PVE 9+ requires pool names to start with a letter; PVE 8 allows
	// names starting with digits but this is deprecated.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][a-zA-Z0-9._-]*(/[a-zA-Z0-9][a-zA-Z0-9._-]*){0,2}$`
	// +optional
	ResourcePool string `json:"resourcePool,omitempty" hash:"ignore"`
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

// KubeletConfiguration defines args to be used when configuring kubelet on provisioned nodes.
// They are a subset of the upstream types, recognizing not all options may be supported.
// Wherever possible, the types and names should reflect the upstream kubelet types.
// https://pkg.go.dev/k8s.io/kubelet/config/v1beta1#KubeletConfiguration
// https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/apis/config/types.go
type KubeletConfiguration struct {
	// CPUManagerPolicy is the name of the policy to use.
	// +kubebuilder:validation:Enum:={none,static}
	// +optional
	CPUManagerPolicy string `json:"cpuManagerPolicy,omitempty"`

	// CPUCFSQuota enables CPU CFS quota enforcement for containers that specify CPU limits.
	// +optional
	CPUCFSQuota *bool `json:"cpuCFSQuota,omitempty"`

	// cpuCFSQuotaPeriod sets the CPU CFS quota period value, `cpu.cfs_period_us`.
	// The value must be between 1 ms and 1 second, inclusive.
	// +optional
	CPUCFSQuotaPeriod *metav1.Duration `json:"cpuCFSQuotaPeriod,omitempty"`

	// TopologyManagerPolicy is the name of the topology manager policy to use.
	// Valid values include:
	//
	// - `restricted`: kubelet only allows pods with optimal NUMA node alignment for requested resources;
	// - `best-effort`: kubelet will favor pods with NUMA alignment of CPU and device resources;
	// - `none`: kubelet has no knowledge of NUMA alignment of a pod's CPU and device resources.
	// - `single-numa-node`: kubelet only allows pods with a single NUMA alignment
	//   of CPU and device resources.
	//
	// +kubebuilder:validation:Enum:={restricted,best-effort,none,single-numa-node}
	// +optional
	TopologyManagerPolicy string `json:"topologyManagerPolicy,omitempty"`

	// TopologyManagerScope represents the scope of topology hint generation
	// that topology manager requests and hint providers generate.
	// Valid values include:
	//
	// - `container`: topology policy is applied on a per-container basis.
	// - `pod`: topology policy is applied on a per-pod basis.
	//
	// +kubebuilder:validation:Enum:={container,pod}
	// +optional
	TopologyManagerScope string `json:"topologyManagerScope,omitempty"`

	// ImageGCHighThresholdPercent is the percent of disk usage after which image
	// garbage collection is always run. The percent is calculated by dividing this
	// field value by 100, so this field must be between 0 and 100, inclusive.
	// When specified, the value must be greater than ImageGCLowThresholdPercent.
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCHighThresholdPercent *int32 `json:"imageGCHighThresholdPercent,omitempty"`

	// ImageGCLowThresholdPercent is the percent of disk usage before which image
	// garbage collection is never run. Lowest disk usage to garbage collect to.
	// The percent is calculated by dividing this field value by 100,
	// so the field value must be between 0 and 100, inclusive.
	// When specified, the value must be less than imageGCHighThresholdPercent
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCLowThresholdPercent *int32 `json:"imageGCLowThresholdPercent,omitempty"`

	// ShutdownGracePeriod specifies the total duration that the node should delay the
	// shutdown and total grace period for pod termination during a node shutdown.
	// +optional
	ShutdownGracePeriod *metav1.Duration `json:"shutdownGracePeriod,omitempty"`

	// A comma separated whitelist of unsafe sysctls or sysctl patterns (ending in `*`).
	// Unsafe sysctl groups are `kernel.shm*`, `kernel.msg*`, `kernel.sem`, `fs.mqueue.*`,
	// and `net.*`. For example: "`kernel.msg*,net.ipv4.route.min_pmtu`"
	// +optional
	AllowedUnsafeSysctls []string `json:"allowedUnsafeSysctls,omitempty"`

	// ClusterDNS is a list of IP addresses for a cluster DNS server. If set,
	// kubelet will configure all containers to use this for DNS resolution
	// instead of the host's DNS servers.
	// +kubebuilder:validation:MaxItems:=3
	// +optional
	ClusterDNS []string `json:"clusterDNS,omitempty"`

	// MaxPods is an override for the maximum number of pods that can run on
	// a worker node instance.
	// +kubebuilder:validation:Minimum:=10
	// +kubebuilder:validation:Maximum:=250
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`

	// FailSwapOn tells the Kubelet to fail to start if swap is enabled on the node.
	// +optional
	FailSwapOn *bool `json:"failSwapOn,omitempty"`
}

// BlockDevice defines the block device configuration for the VM
type BlockDevice struct {
	// Size is the size of the block device in `Gi`, `G`, `Ti`, or `T`
	// +kubebuilder:validation:Type:=string
	// +kubebuilder:validation:Pattern=`^\d+(T|G|Ti|Gi)$`
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// Storage is the proxmox storage-id to create the block device
	// +kubebuilder:validation:Type:=string
	// +kubebuilder:validation:MaxLength=30
	// +optional
	Storage string `json:"storage,omitempty"`
}

type InstanceTemplateClassReference struct {
	// Kind of the referent; More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds"
	// +kubebuilder:validation:Enum:={ProxmoxTemplate,ProxmoxUnmanagedTemplate}
	// +required
	Kind string `json:"kind"`
	// Name of the referent; More info: http://kubernetes.io/docs/user-guide/identifiers#names
	// +kubebuilder:validation:MinLength=1
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
	Type string `json:"type,omitempty"`

	// templatesRef is a reference to the secret that contains cloud-init metadata templates.
	// Secret must contain the following keys, each key is optional:
	// - `user-data` - Userdata for cloud-init
	// - `meta-data` - Metadata for cloud-init
	// - `network-config` - Network configuration for cloud-init
	// +optional
	TemplatesRef *corev1.SecretReference `json:"templatesRef,omitempty"`

	// valuesRef is a reference to the secret that contains cloud-init custom template values.
	// +optional
	ValuesRef *corev1.SecretReference `json:"valuesRef,omitempty"`
}

// SecurityGroups defines a term to apply security groups
type SecurityGroups struct {
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

type inPlaceUpdateFields struct {
	SecurityGroups []SecurityGroups `json:"securityGroups,omitempty"`
	Tags           []string         `json:"tags,omitempty"`
	ResourcePool   string           `json:"resourcePool,omitempty"`
}

func (in *ProxmoxNodeClass) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

func (in *ProxmoxNodeClass) InPlaceHash() string {
	hashStruct := &inPlaceUpdateFields{
		SecurityGroups: in.Spec.SecurityGroups,
		Tags:           in.Spec.Tags,
		ResourcePool:   in.Spec.ResourcePool,
	}

	return fmt.Sprint(lo.Must(hashstructure.Hash(hashStruct, hashstructure.FormatV2, &hashstructure.HashOptions{
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
	metav1.ListMeta `json:"metadata,omitempty"` //nolint:modernize

	Items []ProxmoxNodeClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProxmoxNodeClass{}, &ProxmoxNodeClassList{})
}
