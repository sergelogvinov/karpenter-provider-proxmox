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

package cloudcapacity

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager"

	corev1 "k8s.io/api/core/v1"
)

type NodeCapacityInfo struct {
	// Name is the name of the node.
	Name string `json:"name"`
	// Region is the region of the node.
	Region string `json:"region"`
	// CPULoad is the CPU load of the node in percentage.
	CPULoad int `json:"cpu_load"`

	// Allocatable is the total amount of resources available to the VMs.
	Allocatable corev1.ResourceList `json:"allocatable"`

	// ResourceManager manages the CPU and memory and other resources of the node.
	ResourceManager resourcemanager.ResourceManager `json:"-"`
}

type NodeStorageCapacityInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// Shared indicates if the storage is shared across nodes.
	Shared bool
	// Type is the type of the storage. (dir/lvm/zfspool)
	Type string
	// Capabilities are the capabilities of the storage.
	Capabilities []string
	// Zones are the zones where the storage is available.
	Zones []string
}

type NodeNetworkIfaceInfo struct {
	// Name is the name of the node.
	Name string
	// Region is the region of the node.
	Region string
	// Ifaces is the network interfaces of the node.
	Ifaces map[string]NetworkIfaceInfo
}

type NetworkIfaceInfo struct {
	Address4 string
	Address6 string
	Gateway4 string
	Gateway6 string
	MTU      uint32
}
