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

package cloudresources

import (
	goproxmox "github.com/sergelogvinov/go-proxmox"

	"k8s.io/utils/cpuset"
)

type VMResources struct {
	ID int
	// CPUs is the number of CPUs assigned to the VM.
	CPUs int
	// Memory is the amount of memory in bytes assigned to the VM.
	Memory uint64
	// DiskGBytes is the amount of system disk in gigabytes assigned to the VM.
	DiskGBytes uint64
	// StorageID is the ID of the storage where the VM's disk is located.
	StorageID string

	// CPUSet represents the specific CPUs on the Host assigned to the VM.
	CPUSet cpuset.CPUSet
	// NUMANodes represents the topology on the Host assigned to the VM.
	NUMANodes map[int]goproxmox.NUMANodeState
}
