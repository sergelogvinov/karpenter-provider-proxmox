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

package cpumanager

import (
	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"k8s.io/utils/cpuset"
)

// TakeByTopology picks numCPUs from availableCPUs, packed into as few NUMA nodes
// as possible and aligned to uncore caches where it can.
func TakeByTopology(logger logr.Logger, topo *topology.CPUTopology, availableCPUs cpuset.CPUSet, numCPUs int) (cpuset.CPUSet, error) {
	return takeByTopologyNUMAPacked(logger, topo, availableCPUs, numCPUs, CPUSortingStrategyPacked, true)
}
