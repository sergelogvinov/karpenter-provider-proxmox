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

package launchrule

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Profile is an instance type's launch profile: whether it is a NUMA type, with its slot fraction, a CPU share
// type, with its seats, or a dedicated type, and its hugepage request. The zero value is a dedicated type
// without a hugepage request.
type Profile struct {
	slotFraction int
	cpuShare     int
	hugepages    uint64
}

// NUMAType is the launch profile of a NUMA type of slotFraction.
func NUMAType(slotFraction int) Profile {
	return Profile{slotFraction: slotFraction}
}

// CPUShareType is the launch profile of a CPU share type of seats.
func CPUShareType(seats int) Profile {
	return Profile{cpuShare: seats}
}

// DedicatedType is the launch profile of a type with dedicated CPUs, neither a NUMA type nor a CPU share type.
func DedicatedType() Profile {
	return Profile{}
}

// ProfileOf is the launch profile of an instance types file entry: numa is its slot fraction, or its NUMA
// template's, cpuShare its seats, and a hugepages-<size> resource in capacity its hugepage request.
func ProfileOf(numa, cpuShare int, capacity corev1.ResourceList) Profile {
	p := Profile{slotFraction: numa, cpuShare: cpuShare}

	for name := range capacity {
		size, ok := strings.CutPrefix(string(name), corev1.ResourceHugePagesPrefix)
		if !ok {
			continue
		}

		if q, err := resource.ParseQuantity(size); err == nil && q.Value() > 0 {
			p.hugepages = uint64(q.Value())
		}
	}

	return p
}

// WithHugepageRequest is p requesting hugepages of pageSize bytes; 0 requests none.
func (p Profile) WithHugepageRequest(pageSize uint64) Profile {
	p.hugepages = pageSize

	return p
}

// SlotFraction is the slot fraction of a NUMA type; 0 for any other type.
func (p Profile) SlotFraction() int {
	return p.slotFraction
}

// CPUShare is the seats of a CPU share type; 0 for any other type.
func (p Profile) CPUShare() int {
	return p.cpuShare
}

// HugepageRequest is the requested page size in bytes; 0 without a hugepage request. No launch rule reads it
// yet (ADR 0004).
func (p Profile) HugepageRequest() uint64 {
	return p.hugepages
}

// Types are the instance types p is one of.
func (p Profile) Types() Types {
	if p.numa() {
		return NUMATypes
	}

	return NonNUMATypes
}

func (p Profile) numa() bool {
	return p.slotFraction != 0
}

// Types are the instance types a launch decision tells apart in whether it offers them: the NUMA types or the
// non-NUMA types. Two launch profiles of the same Types are offered alike in every zone.
type Types int

const (
	// NUMATypes are the NUMA types.
	NUMATypes Types = iota + 1
	// NonNUMATypes are the non-NUMA types: CPU share types and dedicated types.
	NonNUMATypes
)

// String names t as a dropped offering reports them: "NUMA" or "non-NUMA".
func (t Types) String() string {
	switch t {
	case NUMATypes:
		return "NUMA"
	case NonNUMATypes:
		return "non-NUMA"
	default:
		return ""
	}
}

// Profile is a launch profile of one of t, offered as each of t is.
func (t Types) Profile() Profile {
	if t == NUMATypes {
		return NUMAType(1)
	}

	return DedicatedType()
}
