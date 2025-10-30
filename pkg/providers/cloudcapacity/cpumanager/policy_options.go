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
	"fmt"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
)

// StaticPolicyOptions holds the parsed value of the policy options, ready to be consumed internally.
type StaticPolicyOptions struct {
	// flag to enable extra allocation restrictions to avoid
	// different containers to possibly end up on the same core.
	// we consider "core" and "physical CPU" synonim here, leaning
	// towards the terminoloy k8s hints. We acknowledge this is confusing.
	//
	// looking at https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/,
	// any possible naming scheme will lead to ambiguity to some extent.
	// We picked "pcpu" because it the established docs hints at vCPU already.
	FullPhysicalCPUsOnly bool
	// Flag to evenly distribute CPUs across NUMA nodes in cases where more
	// than one NUMA node is required to satisfy the allocation.
	DistributeCPUsAcrossNUMA bool
	// Flag to ensure CPUs are considered aligned at socket boundary rather than
	// NUMA boundary
	AlignBySocket bool
	// flag to enable extra allocation restrictions to spread
	// cpus (HT) on different physical core.
	// This is a preferred policy so do not throw error if they have to packed in one physical core.
	DistributeCPUsAcrossCores bool
	// Flag to remove reserved cores from the list of available cores
	StrictCPUReservation bool
	// Flag that makes best-effort to align CPUs to a uncorecache boundary
	// As long as there are CPUs available, pods will be admitted if the condition is not met.
	PreferAlignByUncoreCacheOption bool
}

// ValidateStaticPolicyOptions ensures that the requested policy options are compatible with the machine on which the CPUManager is running.
func ValidateStaticPolicyOptions(opts StaticPolicyOptions, topology *topology.CPUTopology) error {
	if opts.AlignBySocket {
		// Not compatible with topology when number of sockets are more than number of NUMA nodes.
		if topology.NumSockets > topology.NumNUMANodes {
			return fmt.Errorf("Align by socket is not compatible with hardware where number of sockets are more than number of NUMA")
		}
	}
	return nil
}
