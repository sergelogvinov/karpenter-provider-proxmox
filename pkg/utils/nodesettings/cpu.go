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

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager/settings"

	"k8s.io/utils/cpuset"
)

func GetNodeSettingByNode(n *proxmox.Node) (*settings.NodeSettings, error) {
	if n == nil {
		return nil, nil
	}

	if n.CPUInfo.CPUs == 0 || n.CPUInfo.Cores == 0 || n.CPUInfo.Sockets == 0 || n.CPUInfo.Model == "" {
		return nil, fmt.Errorf("incomplete cpu info: %+v", n.CPUInfo)
	}

	switch {
	case strings.Contains(n.CPUInfo.Model, "AMD EPYC"):
		return nodeSettingsAMDEPYC(n)
	case strings.Contains(n.CPUInfo.Model, "AMD"):
		return nodeSettingsAMD(n)
	case strings.Contains(n.CPUInfo.Model, "Intel"):
		return nodeSettingsIntel(n)
	}

	return nil, nil
}

//nolint:dupl
func nodeSettingsAMDEPYC(n *proxmox.Node) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumSockets: n.CPUInfo.Sockets,
		NumThreads: n.CPUInfo.CPUs / n.CPUInfo.Cores,
	}

	matches := regexp.MustCompile(`AMD EPYC\s? (\d)(\d)(\d)(\d)(\w*)\s+`).FindStringSubmatch(n.CPUInfo.Model)
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
		st.NumUncoreCaches = (n.CPUInfo.Cores / n.CPUInfo.Sockets) / coresPerCCX
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

	nps = n.CPUInfo.Sockets * nps

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := n.CPUInfo.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + n.CPUInfo.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: n.Memory.Total / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}

//nolint:dupl
func nodeSettingsAMD(n *proxmox.Node) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumSockets: n.CPUInfo.Sockets,
		NumThreads: n.CPUInfo.CPUs / n.CPUInfo.Cores,
	}

	nps := n.CPUInfo.Sockets

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := n.CPUInfo.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + n.CPUInfo.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: n.Memory.Total / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}

//nolint:dupl
func nodeSettingsIntel(n *proxmox.Node) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumSockets:      n.CPUInfo.Sockets,
		NumThreads:      n.CPUInfo.CPUs / n.CPUInfo.Cores,
		NumUncoreCaches: n.CPUInfo.Sockets,
	}

	nps := n.CPUInfo.Sockets

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
		cpuPerNuma := n.CPUInfo.Cores / nps

		startCPU := i * cpuPerNuma
		endCPU := startCPU + cpuPerNuma
		cpuList := []string{fmt.Sprintf("%d-%d", startCPU, endCPU-1)}

		if st.NumThreads > 1 {
			threadStartCPU := startCPU + n.CPUInfo.Cores
			threadEndCPU := threadStartCPU + cpuPerNuma
			cpuList = append(cpuList, fmt.Sprintf("%d-%d", threadStartCPU, threadEndCPU-1))
		}

		cpus, err := cpuset.Parse(strings.Join(cpuList, ","))
		if err != nil {
			return nil, fmt.Errorf("parsing cpus for numa node %d: %w", i, err)
		}

		info := settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: n.Memory.Total / uint64(nps),
		}

		st.NUMANodes[i] = info
	}

	return st, nil
}
