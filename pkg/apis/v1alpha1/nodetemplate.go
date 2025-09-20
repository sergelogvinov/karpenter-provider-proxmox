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

// ProxmoxTemplate is the Schema for the ProxmoxTemplate API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:printcolumn:name="Zones",type="string",JSONPath=".status.resources.zones",description=""
// +kubebuilder:printcolumn:name="Machine",type="string",JSONPath=".spec.machine",description=""
// +kubebuilder:printcolumn:name="CPU",type="string",JSONPath=".spec.cpu.type",description=""
// +kubebuilder:printcolumn:name="VGA",type="string",JSONPath=".spec.vga.type",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:scope=Cluster,categories=karpenter
// +kubebuilder:subresource:status
type ProxmoxTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of ProxmoxTemplate
	Spec ProxmoxTemplateSpec `json:"spec,omitempty"`

	// Status defines the observed state of ProxmoxTemplate
	Status ProxmoxTemplateStatus `json:"status,omitempty"`
}

// ProxmoxTemplateSpec defines the desired state of ProxmoxTemplateSpec
type ProxmoxTemplateSpec struct {
	// Region is the Proxmox Cloud region where VM template will be created
	// +kubebuilder:validation:MinLength=1
	// +optional
	Region string `json:"region,omitempty"`

	// Description for the VM template.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Description string `json:"description,omitempty" hash:"ignore"`

	// SourceImage defines the source image for the VM boot disk.
	// +required
	SourceImage *SourceImage `json:"sourceImage"`

	// StorageIDs is a list of storage IDs where the VM template and base image will be stored.
	// Storage should supports images and import content types.
	// +kubebuilder:validation:MinItems=1
	// +required
	StorageIDs []string `json:"storageIDs"`

	// Machine for the VM machine type.
	// +kubebuilder:validation:Enum=pc;q35
	// +kubebuilder:default=q35
	// +optional
	Machine string `json:"machine,omitempty"`

	// QemuGuestAgent enables the QEMU Guest Agent service in the VM template.
	// +optional
	QemuGuestAgent *QemuGuestAgent `json:"agent,omitempty" hash:"ignore"`

	// CPU configuration
	// +kubebuilder:default={"type":"x86-64-v2-AES"}
	// +optional
	CPU *CPU `json:"cpu,omitempty"`

	// VGA configuration
	// +optional
	VGA *VGA `json:"vga,omitempty"`

	// Network defines the network configuration for the VM template
	// +kubebuilder:validation:MinItems=1
	// +required
	Network []Network `json:"network"`

	// PCIDevices is a list of PCI devices to attach to the VM template
	// Supported Mapping devices only
	// +kubebuilder:validation:MaxItems:=5
	// +optional
	PCIDevices []PCIDevice `json:"pciDevices,omitempty"`

	// Tags to apply to the VM template
	// +kubebuilder:validation:MaxItems:=10
	// +optional
	Tags []string `json:"tags,omitempty" hash:"ignore"`
}

type SourceImage struct {
	// URL is the location of the source image.
	// +kubebuilder:validation:Pattern="^https?://[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}(/\\S*)?$"
	// +required
	URL string `json:"url"`

	// ImageName is the prefix name of the destination image.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ImageName string `json:"imageName,omitempty"`

	// Checksum is a hash of the checksum.
	// +kubebuilder:validation:MinLength=32
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Checksum string `json:"checksum,omitempty"`

	// ChecksumType is the algorithm to calculate the checksum of the file.
	// +kubebuilder:validation:Enum=md5;sha1;sha224;sha256;sha384;sha512
	// +optional
	ChecksumType string `json:"checksumType,omitempty"`
}

type QemuGuestAgent struct {
	// Enable QEMU Guest Agent service in the VM template.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Type of QEMU Guest Agent channel.
	// +kubebuilder:validation:Enum=virtio;isa
	// +kubebuilder:default=virtio
	// +optional
	Type string `json:"type,omitempty"`

	// FsFreezeOnBackup enables the file system freeze operation during backup.
	// +optional
	FsFreezeOnBackup *bool `json:"fsFreezeOnBackup,omitempty"`

	// FsTrimClonedDisks enables the discard/TRIM operation on cloned disks.
	// +optional
	FsTrimClonedDisks *bool `json:"fsTrimClonedDisks,omitempty"`
}

// CPU defines the CPU configuration for the VM template
type CPU struct {
	// Emulated CPU type.
	// +kubebuilder:validation:Enum=host;kvm64;x86-64-v2;x86-64-v2-AES;x86-64-v3;x86-64-v4
	// +kubebuilder:default=x86-64-v2-AES
	// +optional
	Type string `json:"type,omitempty"`

	// List of additional CPU flags
	// +kubebuilder:validation:MaxItems=10
	// +optional
	Flags []string `json:"flags,omitempty"`
}

// VGA configuration of the VGA Hardware.
type VGA struct {
	// Emulated VGA type.
	// +kubebuilder:validation:Enum=none;serial0;std;vmware
	// +kubebuilder:default=std
	// +optional
	Type string `json:"type,omitempty"`

	// Sets the VGA memory in MiB.
	// +kubebuilder:validation:Minimum=4
	// +kubebuilder:validation:Maximum=512
	// +optional
	Memory *int `json:"memory,omitempty"`
}

// Network defines the network configuration for the VM template
type Network struct {
	// IPConfig defines the IP configuration for this network interface.
	IPConfig `json:",inline"`

	// Name of the network interface.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Name string `json:"name"`

	// Bridge to attach the network device to.
	// +kubebuilder:validation:MinLength=1
	// +required
	Bridge string `json:"bridge"`

	// Network Card Model.
	// +kubebuilder:validation:Enum=e1000;e1000e;rtl8139;virtio;vmxnet3
	// +kubebuilder:default=virtio
	// +optional
	Model *string `json:"model,omitempty"`

	// Force MTU of network device.
	// +kubebuilder:validation:XValidation:rule="self == 1 || ( self >= 576 && self <= 65520)"
	// +kubebuilder:default=1500
	// +optional
	MTU *uint16 `json:"mtu,omitempty"`

	// VLAN tag to apply to packets on this interface.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	// +optional
	VLAN *uint16 `json:"vlan,omitempty"`

	// Whether this interface should be protected by the firewall.
	// +optional
	Firewall *bool `json:"firewall,omitempty"`
}

// IPConfig defines the IP configuration for a network interface.
type IPConfig struct {
	// Address4 ip address with prefix length
	// +kubebuilder:validation:MinLength=1
	// +optional
	Address4 string `json:"address4,omitempty"`

	// Address6 ip address with prefix length
	// +kubebuilder:validation:MinLength=1
	// +optional
	Address6 string `json:"address6,omitempty"`

	// Gateway4 Address
	// +kubebuilder:validation:MinLength=1
	// +optional
	Gateway4 string `json:"gateway4,omitempty"`

	// Gateway6 Address
	// +kubebuilder:validation:MinLength=1
	// +optional
	Gateway6 string `json:"gateway6,omitempty"`

	// DNS Servers
	// +kubebuilder:validation:MinItems=1
	// +optional
	DNSServers []string `json:"dnsServers"`
}

// PCIDevice defines a PCI device to attach to the VM template
type PCIDevice struct {
	// Mapping is the PCI address of the device to attach.
	// +kubebuilder:validation:MinLength=1
	// +required
	Mapping string `json:"mapping,omitempty"`

	// MDev is the mediated device type to attach.
	// +optional
	MDev string `json:"mdev,omitempty"`

	// PCIE indicates if the device is a PCI Express device.
	// +optional
	PCIe *bool `json:"pcie,omitempty"`

	// XVGA indicates that GPU set to primary display.
	// +optional
	XVga *bool `json:"xvga,omitempty"`
}

func (in *ProxmoxTemplate) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

// ProxmoxTemplateList contains a list of ProxmoxTemplate
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
type ProxmoxTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ProxmoxTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProxmoxTemplate{}, &ProxmoxTemplateList{})
}
