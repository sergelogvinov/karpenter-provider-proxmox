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

// kubernetes pkg/kubelet/cm/cpumanager/topology/

package topology

import (
	"reflect"
	"testing"

	"k8s.io/utils/cpuset"
)

func TestCPUDetailsKeepOnly(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {},
		1: {},
		2: {},
	}

	tests := []struct {
		name string
		cpus cpuset.CPUSet
		want CPUDetails
	}{{
		name: "cpus is in CPUDetails.",
		cpus: cpuset.New(0, 1),
		want: map[int]CPUInfo{
			0: {},
			1: {},
		},
	}, {
		name: "cpus is not in CPUDetails.",
		cpus: cpuset.New(3),
		want: CPUDetails{},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.KeepOnly(tt.cpus)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("KeepOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsNUMANodes(t *testing.T) {

	tests := []struct {
		name    string
		details CPUDetails
		want    cpuset.CPUSet
	}{{
		name: "Get CPUset of NUMANode IDs",
		details: map[int]CPUInfo{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 1},
			3: {NUMANodeID: 1},
		},
		want: cpuset.New(0, 1),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.NUMANodes()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NUMANodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsNUMANodesInSockets(t *testing.T) {

	var details1 CPUDetails
	details1 = map[int]CPUInfo{
		0: {SocketID: 0, NUMANodeID: 0},
		1: {SocketID: 1, NUMANodeID: 0},
		2: {SocketID: 2, NUMANodeID: 1},
		3: {SocketID: 3, NUMANodeID: 1},
	}

	// poorly designed mainboards
	var details2 CPUDetails
	details2 = map[int]CPUInfo{
		0: {SocketID: 0, NUMANodeID: 0},
		1: {SocketID: 0, NUMANodeID: 1},
		2: {SocketID: 1, NUMANodeID: 2},
		3: {SocketID: 1, NUMANodeID: 3},
	}

	tests := []struct {
		name    string
		details CPUDetails
		ids     []int
		want    cpuset.CPUSet
	}{{
		name:    "Socket IDs is in CPUDetails.",
		details: details1,
		ids:     []int{0, 1, 2},
		want:    cpuset.New(0, 1),
	}, {
		name:    "Socket IDs is not in CPUDetails.",
		details: details1,
		ids:     []int{4},
		want:    cpuset.New(),
	}, {
		name:    "Socket IDs is in CPUDetails. (poorly designed mainboards)",
		details: details2,
		ids:     []int{0},
		want:    cpuset.New(0, 1),
	}, {
		name:    "Socket IDs is not in CPUDetails. (poorly designed mainboards)",
		details: details2,
		ids:     []int{3},
		want:    cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.NUMANodesInSockets(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NUMANodesInSockets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsSockets(t *testing.T) {

	tests := []struct {
		name    string
		details CPUDetails
		want    cpuset.CPUSet
	}{{
		name: "Get CPUset of Socket IDs",
		details: map[int]CPUInfo{
			0: {SocketID: 0},
			1: {SocketID: 0},
			2: {SocketID: 1},
			3: {SocketID: 1},
		},
		want: cpuset.New(0, 1),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.Sockets()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Sockets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCPUsInSockets(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {SocketID: 0},
		1: {SocketID: 0},
		2: {SocketID: 1},
		3: {SocketID: 2},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "Socket IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "Socket IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.CPUsInSockets(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CPUsInSockets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsSocketsInNUMANodes(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {NUMANodeID: 0, SocketID: 0},
		1: {NUMANodeID: 0, SocketID: 1},
		2: {NUMANodeID: 1, SocketID: 2},
		3: {NUMANodeID: 2, SocketID: 3},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "NUMANodes IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "NUMANodes IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.SocketsInNUMANodes(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SocketsInNUMANodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCores(t *testing.T) {

	tests := []struct {
		name    string
		details CPUDetails
		want    cpuset.CPUSet
	}{{
		name: "Get CPUset of Cores",
		details: map[int]CPUInfo{
			0: {CoreID: 0},
			1: {CoreID: 0},
			2: {CoreID: 1},
			3: {CoreID: 1},
		},
		want: cpuset.New(0, 1),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.Cores()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Cores() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCoresInNUMANodes(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {NUMANodeID: 0, CoreID: 0},
		1: {NUMANodeID: 0, CoreID: 1},
		2: {NUMANodeID: 1, CoreID: 2},
		3: {NUMANodeID: 2, CoreID: 3},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "NUMANodes IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "NUMANodes IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.CoresInNUMANodes(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CoresInNUMANodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCoresInSockets(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {SocketID: 0, CoreID: 0},
		1: {SocketID: 0, CoreID: 1},
		2: {SocketID: 1, CoreID: 2},
		3: {SocketID: 2, CoreID: 3},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "Socket IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "Socket IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.CoresInSockets(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CoresInSockets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCPUs(t *testing.T) {

	tests := []struct {
		name    string
		details CPUDetails
		want    cpuset.CPUSet
	}{{
		name: "Get CPUset of CPUs",
		details: map[int]CPUInfo{
			0: {},
			1: {},
		},
		want: cpuset.New(0, 1),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.CPUs()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CPUs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCPUsInNUMANodes(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 1},
		3: {NUMANodeID: 2},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "NUMANode IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "NUMANode IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.CPUsInNUMANodes(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CPUsInNUMANodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUDetailsCPUsInCores(t *testing.T) {

	var details CPUDetails
	details = map[int]CPUInfo{
		0: {CoreID: 0},
		1: {CoreID: 0},
		2: {CoreID: 1},
		3: {CoreID: 2},
	}

	tests := []struct {
		name string
		ids  []int
		want cpuset.CPUSet
	}{{
		name: "Core IDs is in CPUDetails.",
		ids:  []int{0, 1},
		want: cpuset.New(0, 1, 2),
	}, {
		name: "Core IDs is not in CPUDetails.",
		ids:  []int{3},
		want: cpuset.New(),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details.CPUsInCores(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CPUsInCores() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUCoreID(t *testing.T) {
	topoDualSocketHT := &CPUTopology{
		NumCPUs:    12,
		NumSockets: 2,
		NumCores:   6,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 1, NUMANodeID: 1},
			6:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			8:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			10: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			11: {CoreID: 5, SocketID: 1, NUMANodeID: 1},
		},
	}

	tests := []struct {
		name    string
		topo    *CPUTopology
		id      int
		want    int
		wantErr bool
	}{{
		name: "Known Core ID",
		topo: topoDualSocketHT,
		id:   2,
		want: 2,
	}, {
		name: "Known Core ID (core sibling).",
		topo: topoDualSocketHT,
		id:   8,
		want: 2,
	}, {
		name:    "Unknown Core ID.",
		topo:    topoDualSocketHT,
		id:      -2,
		want:    -1,
		wantErr: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.topo.CPUCoreID(tt.id)
			gotErr := (err != nil)
			if gotErr != tt.wantErr {
				t.Errorf("CPUCoreID() returned err %v, want %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("CPUCoreID() returned %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUSocketID(t *testing.T) {
	topoDualSocketHT := &CPUTopology{
		NumCPUs:    12,
		NumSockets: 2,
		NumCores:   6,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 1, NUMANodeID: 1},
			6:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			8:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			10: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			11: {CoreID: 5, SocketID: 1, NUMANodeID: 1},
		},
	}

	tests := []struct {
		name    string
		topo    *CPUTopology
		id      int
		want    int
		wantErr bool
	}{{
		name: "Known Core ID",
		topo: topoDualSocketHT,
		id:   3,
		want: 1,
	}, {
		name: "Known Core ID (core sibling).",
		topo: topoDualSocketHT,
		id:   9,
		want: 1,
	}, {
		name:    "Unknown Core ID.",
		topo:    topoDualSocketHT,
		id:      1000,
		want:    -1,
		wantErr: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.topo.CPUSocketID(tt.id)
			gotErr := (err != nil)
			if gotErr != tt.wantErr {
				t.Errorf("CPUSocketID() returned err %v, want %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("CPUSocketID() returned %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUNUMANodeID(t *testing.T) {
	topoDualSocketHT := &CPUTopology{
		NumCPUs:    12,
		NumSockets: 2,
		NumCores:   6,
		CPUDetails: map[int]CPUInfo{
			0:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			2:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			3:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			4:  {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			5:  {CoreID: 5, SocketID: 1, NUMANodeID: 1},
			6:  {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			7:  {CoreID: 1, SocketID: 1, NUMANodeID: 1},
			8:  {CoreID: 2, SocketID: 0, NUMANodeID: 0},
			9:  {CoreID: 3, SocketID: 1, NUMANodeID: 1},
			10: {CoreID: 4, SocketID: 0, NUMANodeID: 0},
			11: {CoreID: 5, SocketID: 1, NUMANodeID: 1},
		},
	}

	tests := []struct {
		name    string
		topo    *CPUTopology
		id      int
		want    int
		wantErr bool
	}{{
		name: "Known Core ID",
		topo: topoDualSocketHT,
		id:   0,
		want: 0,
	}, {
		name: "Known Core ID (core sibling).",
		topo: topoDualSocketHT,
		id:   6,
		want: 0,
	}, {
		name:    "Unknown Core ID.",
		topo:    topoDualSocketHT,
		id:      1000,
		want:    -1,
		wantErr: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.topo.CPUNUMANodeID(tt.id)
			gotErr := (err != nil)
			if gotErr != tt.wantErr {
				t.Errorf("CPUNUMANodeID() returned err %v, want %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("CPUNUMANodeID() returned %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUsInUncoreCaches(t *testing.T) {
	tests := []struct {
		name    string
		details CPUDetails
		ids     []int
		want    cpuset.CPUSet
	}{
		{
			name: "Single Uncore Cache",
			details: map[int]CPUInfo{
				0: {UncoreCacheID: 0},
				1: {UncoreCacheID: 0},
			},
			ids:  []int{0},
			want: cpuset.New(0, 1),
		},
		{
			name: "Multiple Uncore Caches",
			details: map[int]CPUInfo{
				0: {UncoreCacheID: 0},
				1: {UncoreCacheID: 0},
				2: {UncoreCacheID: 1},
			},
			ids:  []int{0, 1},
			want: cpuset.New(0, 1, 2),
		},
		{
			name: "Uncore Cache does not exist",
			details: map[int]CPUInfo{
				0: {UncoreCacheID: 0},
			},
			ids:  []int{1},
			want: cpuset.New(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.CPUsInUncoreCaches(tt.ids...)
			if !reflect.DeepEqual(tt.want, got) {
				t.Errorf("CPUsInUncoreCaches() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestUncoreInNUMANodes(t *testing.T) {
	tests := []struct {
		name    string
		details CPUDetails
		ids     []int
		want    cpuset.CPUSet
	}{
		{
			name: "Single NUMA Node",
			details: map[int]CPUInfo{
				0: {NUMANodeID: 0, UncoreCacheID: 0},
				1: {NUMANodeID: 0, UncoreCacheID: 1},
			},
			ids:  []int{0},
			want: cpuset.New(0, 1),
		},
		{
			name: "Multiple NUMANode",
			details: map[int]CPUInfo{
				0:  {NUMANodeID: 0, UncoreCacheID: 0},
				1:  {NUMANodeID: 0, UncoreCacheID: 0},
				20: {NUMANodeID: 1, UncoreCacheID: 1},
				21: {NUMANodeID: 1, UncoreCacheID: 1},
			},
			ids:  []int{0, 1},
			want: cpuset.New(0, 1),
		},
		{
			name: "Non-Existent NUMANode",
			details: map[int]CPUInfo{
				0: {NUMANodeID: 1, UncoreCacheID: 0},
			},
			ids:  []int{0},
			want: cpuset.New(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.UncoreInNUMANodes(tt.ids...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UncoreInNUMANodes()= %v, want %v", got, tt.want)
			}
		})

	}
}

func TestUncoreCaches(t *testing.T) {
	tests := []struct {
		name    string
		details CPUDetails
		want    cpuset.CPUSet
	}{
		{
			name: "Get CPUSet of UncoreCache IDs",
			details: map[int]CPUInfo{
				0: {UncoreCacheID: 0},
				1: {UncoreCacheID: 1},
				2: {UncoreCacheID: 2},
			},
			want: cpuset.New(0, 1, 2),
		},
		{
			name:    "Empty CPUDetails",
			details: map[int]CPUInfo{},
			want:    cpuset.New(),
		},
		{
			name: "Shared UncoreCache",
			details: map[int]CPUInfo{
				0: {UncoreCacheID: 0},
				1: {UncoreCacheID: 0},
				2: {UncoreCacheID: 0},
			},
			want: cpuset.New(0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.details.UncoreCaches()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UncoreCaches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUsPerUncore(t *testing.T) {
	tests := []struct {
		name string
		topo *CPUTopology
		want int
	}{
		{
			name: "Zero Number of UncoreCache",
			topo: &CPUTopology{
				NumCPUs:        8,
				NumUncoreCache: 0,
			},
			want: 0,
		},
		{
			name: "Normal case",
			topo: &CPUTopology{
				NumCPUs:        16,
				NumUncoreCache: 2,
			},
			want: 8,
		},
		{
			name: "Single shared UncoreCache",
			topo: &CPUTopology{
				NumCPUs:        8,
				NumUncoreCache: 1,
			},
			want: 8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.topo.CPUsPerUncore()
			if got != tt.want {
				t.Errorf("CPUsPerUncore() = %v, want %v", got, tt.want)
			}
		})
	}

}

// func Test_getUncoreCacheID(t *testing.T) {
// 	tests := []struct {
// 		name string
// 		args cadvisorapi.Core
// 		want int
// 	}{
// 		{
// 			name: "Core with uncore cache info",
// 			args: cadvisorapi.Core{
// 				SocketID: 1,
// 				UncoreCaches: []cadvisorapi.Cache{
// 					{Id: 5},
// 					{Id: 6},
// 				},
// 			},
// 			want: 5,
// 		},
// 		{
// 			name: "Core with empty uncore cache info",
// 			args: cadvisorapi.Core{
// 				SocketID:     2,
// 				UncoreCaches: []cadvisorapi.Cache{},
// 			},
// 			want: 2,
// 		},
// 		{
// 			name: "Core with nil uncore cache info",
// 			args: cadvisorapi.Core{
// 				SocketID: 1,
// 			},
// 			want: 1,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			if got := getUncoreCacheID(tt.args); got != tt.want {
// 				t.Errorf("getUncoreCacheID() = %v, want %v", got, tt.want)
// 			}
// 		})
// 	}
// }
