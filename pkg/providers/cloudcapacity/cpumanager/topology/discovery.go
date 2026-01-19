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

package topology

import (
	"fmt"
	"strings"

	"github.com/luthermonson/go-proxmox"
	"k8s.io/utils/cpuset"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"
)

// Discover returns CPUTopology based on proxmox node info
// We do not have access to real information,
// so, we will predict architecture based on the provided CPUInfo.
func Discover(n *proxmox.Node) (*CPUTopology, error) {
	if n == nil {
		return nil, fmt.Errorf("cannot discover cpu topology from nil node info")
	}

	machineInfo := n.CPUInfo
	if machineInfo.CPUs == 0 || machineInfo.Cores == 0 || machineInfo.Sockets == 0 {
		return nil, fmt.Errorf("could not detect CPU topology from incomplete machine info: %+v", machineInfo)
	}

	CPUDetails := CPUDetails{}

	nCache := 4
	nCPUsNodes := 1
	if c := machineInfo.Cores / machineInfo.Sockets; c > 16 {
		for _, i := range []int{12, 10, 8, 6, 4} {
			if c%i == 0 {
				nCPUsNodes = i

				break
			}
		}
	}

	numaModes := machineInfo.Cores / nCPUsNodes

	for cpu := range machineInfo.CPUs {
		socketID := int(cpu/(machineInfo.Cores/machineInfo.Sockets)) % machineInfo.Sockets
		numaID := int(cpu/nCPUsNodes) % nCPUsNodes % numaModes

		coreID := cpu
		if coreID >= machineInfo.Cores {
			coreID = cpu - machineInfo.Cores
		}

		cacheID := (coreID / nCache)
		if strings.Contains(machineInfo.Model, "Intel") {
			cacheID = 0
		}

		CPUDetails[cpu] = CPUInfo{
			CoreID:        coreID,
			SocketID:      socketID,
			NUMANodeID:    numaID,
			UncoreCacheID: cacheID,
		}
	}

	return &CPUTopology{
		NumCPUs:        machineInfo.CPUs,
		NumSockets:     machineInfo.Sockets,
		NumCores:       machineInfo.Cores,
		NumNUMANodes:   CPUDetails.NUMANodes().Size(),
		NumUncoreCache: CPUDetails.UncoreCaches().Size(),
		CPUDetails:     CPUDetails,
	}, nil
}

func DiscoverFromSettings(settings settings.NodeSettings) (*CPUTopology, error) {
	if len(settings.NUMANodes) == 0 {
		return nil, fmt.Errorf("could not detect cpu topology from incomplete node settings")
	}

	nCPUs := 0
	nCores := 0
	nSockets := max(1, settings.NumSockets)
	nCache := max(1, settings.NumUncoreCaches)
	nThreads := max(1, settings.NumThreads)

	parsedCPUs := make(map[int]cpuset.CPUSet, len(settings.NUMANodes))
	for i, numa := range settings.NUMANodes {
		cpus, err := cpuset.Parse(numa.CPUs)
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		parsedCPUs[i] = cpus
		nCPUs += cpus.Size()
		nCores += cpus.Size() / max(1, nThreads)
	}

	CPUDetails := CPUDetails{}

	for i, cpus := range parsedCPUs {
		for _, cpu := range cpus.List() {
			coresPerSocket := max(1, nCores/nSockets)
			socketID := int(cpu/coresPerSocket) % nSockets

			coreID := cpu
			if coreID >= nCores {
				coreID = cpu - nCores
			}

			cacheID := (coreID / nCache)

			CPUDetails[cpu] = CPUInfo{
				NUMANodeID:    i,
				SocketID:      socketID,
				CoreID:        coreID,
				UncoreCacheID: cacheID,
			}
		}
	}

	return &CPUTopology{
		NumCPUs:        nCPUs,
		NumSockets:     nSockets,
		NumCores:       nCores,
		NumNUMANodes:   CPUDetails.NUMANodes().Size(),
		NumUncoreCache: CPUDetails.UncoreCaches().Size(),
		CPUDetails:     CPUDetails,
	}, nil
}
