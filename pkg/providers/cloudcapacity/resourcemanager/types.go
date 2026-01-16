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

package resourcemanager

// NodeSettings represents the hardware settings of a node.
type NodeSettings struct {
	// ReservedCPUs is the list of reserved CPU IDs. For example: [0,4]
	ReservedCPUs []int `json:"reservedcpus,omitempty"`
	// ReservedMemory in bytes.
	ReservedMemory uint64 `json:"reservedmemory,omitempty"`
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
