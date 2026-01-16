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
)

// Discover returns CPUTopology based on proxmox node info
// We do not have access to real information,
// so, we will predict architecture based on the provided CPUInfo.
func Discover(machineInfo *proxmox.CPUInfo) (*CPUTopology, error) {
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
