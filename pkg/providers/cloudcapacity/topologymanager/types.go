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

package topologymanager

// vendor/github.com/google/cadvisor/info/v1/machine.go
// vendor/github.com/google/cadvisor/utils/sysinfo/sysinfo.go GetHugePagesInfo

type Node struct {
	Id        int             `json:"node_id"`
	Memory    uint64          `json:"memory"`
	HugePages []HugePagesInfo `json:"hugepages"`
	Cores     []Core          `json:"cores"`
	Distances []uint64        `json:"distances"`
}

type HugePagesInfo struct {
	PageSize uint64 `json:"page_size"`
	NumPages uint64 `json:"num_pages"`
}

type Core struct {
	Id       int   `json:"core_id"`
	Threads  []int `json:"thread_ids"`
	SocketID int   `json:"socket_id"`
}
