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

package lxcresources

import (
	"fmt"

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"

	"k8s.io/utils/cpuset"
)

// GetResourceFromContainer extracts ContainerResources from a Proxmox Container object.
func GetResourceFromContainer(container *proxmox.Container) (opt *resources.VMResources, err error) {
	if container == nil {
		return nil, fmt.Errorf("container config cannot be nil")
	}

	opt = &resources.VMResources{
		ID:     int(container.VMID),
		CPUs:   container.CPUs,
		CPUSet: cpuset.New(),
		Memory: container.MaxMem,
	}

	return opt, nil
}
