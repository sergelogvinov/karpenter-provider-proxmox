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
)

type NodeCapacityInfo struct {
	// Name is the name of the node.
	Name string `json:"name"`
	// Region is the region of the node.
	Region string `json:"region"`
	// CPULoad is the CPU load of the node in percentage.
	CPULoad int `json:"cpu_load"`

	// Capacity tracks the CPU and memory the node gives to VMs.
	Capacity *resourcemanager.ZoneCapacity `json:"-"`

	// vms is the set of the node's VMs in any state, as of the sync that built Capacity.
	vms vmIDs
	// nodeCapacityGuest marks a node with a node-capacity guest, in any state, as of that sync.
	nodeCapacityGuest bool
	// omitted marks a node missing from the last sync, kept only to hold its pending
	// reservations until it returns; it is not available for placement.
	omitted bool
}

// vmIDs is a set of VM IDs.
type vmIDs map[int]bool

// has reports whether VM id is in the set.
func (s vmIDs) has(id int) bool {
	return s[id]
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
