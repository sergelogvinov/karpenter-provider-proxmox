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

// StaticPolicyOptions holds the parsed value of the policy options, ready to be consumed internally.
type StaticPolicyOptions struct {
	// looking at https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/,
	// any possible naming scheme will lead to ambiguity to some extent.
	// We picked "pcpu" because it the established docs hints at vCPU already.
	FullPhysicalCPUsOnly bool
	// Flag to evenly distribute CPUs across NUMA nodes in cases where more
	// than one NUMA node is required to satisfy the allocation.
	DistributeCPUsAcrossNUMA bool
	// flag to enable extra allocation restrictions to spread
	// cpus (HT) on different physical core.
	// This is a preferred policy so do not throw error if they have to packed in one physical core.
	DistributeCPUsAcrossCores bool
	// Flag that makes best-effort to align CPUs to a uncorecache boundary
	// As long as there are CPUs available, pods will be admitted if the condition is not met.
	PreferAlignByUncoreCacheOption bool
}
