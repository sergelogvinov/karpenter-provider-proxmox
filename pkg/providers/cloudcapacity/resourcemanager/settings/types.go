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

package settings

// NodeSettings represents the hardware settings of a node.
type NodeSettings struct {
	// NumSockets is the number of CPU sockets.
	NumSockets int `json:"sockets,omitempty"`
	// NumThreads is the number of threads per core.
	NumThreads int `json:"threads,omitempty"`
	// NumUncoreCaches is the number of uncore caches (cores per CCX).
	// see: https://en.wikipedia.org/wiki/Epyc
	NumUncoreCaches int `json:"uncorecaches,omitempty"`
	// NUMANodes is a map of NUMA node ID to its information.
	NUMANodes NUMANodes `json:"nodes,omitempty"`

	// ReservedCPUs is the list of reserved CPU IDs. For example: [0,4]
	ReservedCPUs []int `json:"reservedcpus,omitempty"`
	// ReservedMemory in bytes.
	ReservedMemory uint64 `json:"reservedmemory,omitempty"`
}

// NUMANodes is a map from NUMA node ID to its information.
type NUMANodes map[int]NUMAInfo

// NUMAInfo represents the CPU and memory information of a NUMA node.
// numactl --hardware
// node 0 cpus: 0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15
// node 0 size: 63411 MB
//
// If CPU support threading, cpus will contain:
// first half of physical cores (0 1 2 3 4 5 6 7)
// second half - threads (8 9 10 11 12 13 14 15).
type NUMAInfo struct {
	CPUs    string `json:"cpus"`
	MemSize uint64 `json:"memsize,omitempty"`
}

// NodeSettingsConfig is a map from region to zone (or "*") to NodeSettings.
type NodeSettingsConfig map[string]map[string]NodeSettings

// json config example
// {
//   "region-1": {
//     "node1": {
//       "reservedcpus": [0,4],
//       "reservedmemory": 1073741824
//     },
//     "node2": {
//       "reservedcpus": [1,5],
//       "reservedmemory": 4294967296
//     }
//   },
//   "region-2": {
//     "node3": {
//       "reservedcpus": [0,1],
//       "reservedmemory": 1073741824
//     }
//   }
// }
