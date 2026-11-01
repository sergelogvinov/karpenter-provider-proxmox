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

package zonesettings

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sergelogvinov/go-proxmox-rest/nodes"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"

	"k8s.io/utils/cpuset"
)

// epycModel matches an EPYC model number: its digits are the series, the core count
// tier, the performance tier and the generation.
var epycModel = regexp.MustCompile(`AMD EPYC\s? (\d)(\d)(\d)(\d)(\w*)\s+`)

// cpuFacts is the physical CPU/memory information the guess is made from.
type cpuFacts struct {
	Model       string
	Sockets     int
	Cores       int
	CPUs        int
	MemoryTotal uint64
}

// cpuRule is what a CPU family says beyond the node status: how many NUMA nodes each
// socket has and how many uncore caches all sockets have, 0 if unknown.
type cpuRule struct {
	numaPerSocket int
	uncoreCaches  int
}

// guessFromStatus guesses NodeSettings from the CPU model in a node
// status (GET /nodes/{node}/status).
func guessFromStatus(st *nodes.Status) (*settings.NodeSettings, error) {
	if st == nil {
		return nil, nil
	}

	if st.CPUInfo == nil || st.Memory == nil {
		return nil, fmt.Errorf("incomplete node status: %+v", st)
	}

	cf := cpuFacts{
		Model:       st.CPUInfo.Model,
		Sockets:     st.CPUInfo.Sockets,
		Cores:       st.CPUInfo.Cores,
		CPUs:        st.CPUInfo.CPUs,
		MemoryTotal: uint64(st.Memory.Total),
	}

	if cf.CPUs == 0 || cf.Cores == 0 || cf.Sockets == 0 || cf.Model == "" {
		return nil, fmt.Errorf("incomplete cpu info: %+v", cf)
	}

	rule, ok := ruleFor(cf)
	if !ok {
		return nil, nil
	}

	return layout(cf, rule)
}

// ruleFor returns the rule of the CPU's family, or false for a CPU it doesn't know.
func ruleFor(cf cpuFacts) (cpuRule, bool) {
	switch {
	case strings.Contains(cf.Model, "AMD EPYC"):
		return epycRule(cf)
	case strings.Contains(cf.Model, "AMD"):
		return cpuRule{numaPerSocket: 1}, true
	case strings.Contains(cf.Model, "Intel"):
		return cpuRule{numaPerSocket: 1, uncoreCaches: cf.Sockets}, true
	}

	return cpuRule{}, false
}

// epycRule reads the CCX size from the core count tier and the NUMA nodes per
// socket (NPS) from the generation. see: https://en.wikipedia.org/wiki/Epyc
func epycRule(cf cpuFacts) (cpuRule, bool) {
	matches := epycModel.FindStringSubmatch(cf.Model)
	if len(matches) != 6 {
		return cpuRule{}, false
	}

	rule := cpuRule{numaPerSocket: 1}

	coresPerCCX := 0

	switch matches[2] {
	case "2", "3":
		coresPerCCX = 4
	case "4":
		coresPerCCX = 6
	case "5", "6", "7", "8":
		coresPerCCX = 8
	}

	if coresPerCCX > 0 {
		rule.uncoreCaches = cf.Cores / coresPerCCX
	}

	switch matches[4] {
	case "1", "2", "4", "5":
		rule.numaPerSocket = 4
	}

	return rule, true
}

// layout splits the cores evenly across the NUMA nodes, each node taking its cores'
// threads from the second half of the CPU list, and the memory evenly too.
func layout(cf cpuFacts, rule cpuRule) (*settings.NodeSettings, error) {
	st := &settings.NodeSettings{
		NumCores:        cf.Cores,
		NumSockets:      cf.Sockets,
		NumThreads:      cf.CPUs / cf.Cores,
		NumUncoreCaches: rule.uncoreCaches,
	}

	nps := cf.Sockets * rule.numaPerSocket
	cpuPerNuma := cf.Cores / nps

	st.NUMANodes = make(map[int]settings.NUMAInfo, nps)
	for i := range nps {
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

		st.NUMANodes[i] = settings.NUMAInfo{
			CPUs:    cpus.String(),
			MemSize: cf.MemoryTotal / uint64(nps),
		}
	}

	return st, nil
}
