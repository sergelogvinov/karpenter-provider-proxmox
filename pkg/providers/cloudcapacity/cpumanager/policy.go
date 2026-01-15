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

package cpumanager

import (
	"k8s.io/utils/cpuset"
)

type policyName string

type Policy interface {
	Name() string
	Status() string

	AvailableCPUs() int

	Allocate(numCPUs int) (cpuset.CPUSet, error)
	AllocateOrUpdate(numCPUs int, cpus cpuset.CPUSet) (cpuset.CPUSet, error)

	Release(numCPUs int, cpus cpuset.CPUSet) error
}
