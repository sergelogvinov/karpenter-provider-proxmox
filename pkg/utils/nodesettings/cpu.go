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

package nodesettings

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sergelogvinov/go-proxmox-rest/nodes"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"

	"k8s.io/utils/cpuset"
)

// cpuFacts is the physical CPU/memory information needed to derive
// NodeSettings, extracted from whichever Proxmox client returned it.
type cpuFacts struct {
	Model       string
	Sockets     int
	Cores       int
	CPUs        int
	MemoryTotal uint64
}

// GetNodeSettingByStatus derives NodeSettings from a go-proxmox-rest node
// status (GET /nodes/{node}/status).
func GetNodeSettingByStatus(st *nodes.Status) (*settings.NodeSettings, error) {
	if st == nil {
		return nil, nil
	}

	if st.CPUInfo == nil || st.Memory == nil {
		return nil, fmt.Errorf("incomplete node status: %+v", st)
	}

	return nodeSettingsFromFacts(cpuFacts{
		Model:       st.CPUInfo.Model,
		Sockets:     st.CPUInfo.Sockets,
		Cores:       st.CPUInfo.Cores,
		CPUs:        st.CPUInfo.CPUs,
		MemoryTotal: uint64(st.Memory.Total),
	})
}

func nodeSettingsFromFacts(cf cpuFacts) (*settings.NodeSettings, error) {
	if cf.CPUs == 0 || cf.Cores == 0 || cf.Sockets == 0 || cf.Model == "" {
		return nil, fmt.Errorf("incomplete cpu info: %+v", cf)
	}

	switch {
	case strings.Contains(cf.Model, "AMD EPYC"):
		return nodeSettingsAMDEPYC(cf)
	case strings.Contains(cf.Model, "AMD"):
		return nodeSettingsAMD(cf)
	case strings.Contains(cf.Model, "Intel"):
		return nodeSettingsIntel(cf)
	}

	return nil, nil
}

//nolint:dupl
func nodeSettingsAMDEPYC(cf cpuFacts) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumCores:   cf.Cores,
		NumSockets: cf.Sockets,
		NumThreads: cf.CPUs / cf.Cores,
	}

	matches := regexp.MustCompile(`AMD EPYC\s? (\d)(\d)(\d)(\d)(\w*)\s+`).FindStringSubmatch(cf.Model)
	if len(matches) != 6 {
		return nil, nil
	}

	coresPerCCX := 0

	switch matches[2] {
	case "2":
		coresPerCCX = 4
	case "3":
		coresPerCCX = 4
	case "4":
		coresPerCCX = 6
	case "5", "6":
		coresPerCCX = 8
	case "7", "8":
		coresPerCCX = 8
	}

	if coresPerCCX > 0 {
		st.NumUncoreCaches = (cf.Cores / cf.Sockets) / coresPerCCX
	}

	nps := 1

	switch matches[4] {
	case "1":
		nps = 4
	case "2":
		nps = 4
	case "4":
		nps = 4
	case "5":
		nps = 4
	}

	nps = cf.Sockets * nps

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := cf.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + cf.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: cf.MemoryTotal / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}

//nolint:dupl
func nodeSettingsAMD(cf cpuFacts) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumCores:   cf.Cores,
		NumSockets: cf.Sockets,
		NumThreads: cf.CPUs / cf.Cores,
	}

	nps := cf.Sockets

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := cf.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + cf.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: cf.MemoryTotal / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}

//nolint:dupl
func nodeSettingsIntel(cf cpuFacts) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumCores:        cf.Cores,
		NumSockets:      cf.Sockets,
		NumThreads:      cf.CPUs / cf.Cores,
		NumUncoreCaches: cf.Sockets,
	}

	nps := cf.Sockets

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := cf.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + cf.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: cf.MemoryTotal / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}
