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

// Package launchrule makes the launch decision (ADR 0003): whether an instance type of a launch profile is
// offered from a template in a zone, and if it is, where its memory comes from and which hugepages and guest
// NUMA options its virtual machine is written with.
//
// Offerings and launches read the same decision, so an offered type launches as decided: the template check
// and cloud capacity drop what it drops, and the launch writes the VM options it gives.
package launchrule

import (
	"fmt"
	"strconv"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"
)

// Reasons of a dropped offering.
const (
	// ReasonHugepagesUnset drops NUMA types in a hugepage zone: the template has no hugepages. It is the
	// intended split of a node class for non-NUMA types.
	ReasonHugepagesUnset = "TemplateHugepagesUnset"
	// ReasonHugepagesSet drops non-NUMA types in a hugepage zone of two or more NUMA nodes: the template has
	// hugepages, which a non-NUMA VM would draw from the pool unseen by the per-node view. It is the intended
	// split of a node class for NUMA types.
	ReasonHugepagesSet = "TemplateHugepagesSet"
	// ReasonHugepagesMismatch drops NUMA types in a hugepage zone, and non-NUMA types too in one of one NUMA
	// node: the template's hugepages is not the pool's page size; "any" is no page size and never matches.
	ReasonHugepagesMismatch = "TemplateHugepagesMismatch"
	// ReasonHugepagesNoPool drops every type in a zone without a hugepage pool: the template has hugepages,
	// and its clones would fail to start with no pool to draw from (#321).
	ReasonHugepagesNoPool = "TemplateHugepagesNoPool"
	// ReasonStatusUnknown drops every type in a hugepage zone: the template's hugepages are not known, so a
	// clone's cannot be predicted.
	ReasonStatusUnknown = "TemplateStatusUnknown"
	// ReasonPoolSpansNUMANodes drops, as root, non-NUMA types in a hugepage zone of two or more NUMA nodes.
	ReasonPoolSpansNUMANodes = "HugepagePoolSpansNUMANodes"
)

// Template is what a launch decision knows of the template a VM is cloned from: its hugepages, or why they
// are not known.
type Template struct {
	// Known tells that Hugepages is the template's.
	Known bool
	// Hugepages is the template's hugepages, "" for none.
	Hugepages string
	// UnknownBecause tells why the hugepages are not known.
	UnknownBecause string
}

// KnownTemplate is a template whose hugepages are hugepages, "" for none.
func KnownTemplate(hugepages string) Template {
	return Template{Known: true, Hugepages: hugepages}
}

// UnknownTemplate is a template whose hugepages are not known, because of because.
func UnknownTemplate(because string) Template {
	return Template{UnknownBecause: because}
}

// Zone is what a launch decision knows of a zone.
type Zone struct {
	// Pool is the zone's hugepage pool.
	Pool zonecapacity.Pool
	// Root tells that the controller logs in as root@pam in the zone's region, so it writes hugepages itself.
	Root bool
	// Policy is the node policy.
	Policy zonecapacity.PolicyKind
}

// NewZone is the zone of pool, where the controller logs in as root@pam or not, under the zero node policy. The
// node policy tells only whether a VM is bound; whether a type is offered and its VM options do not depend on it.
func NewZone(pool zonecapacity.Pool, root bool) Zone {
	return Zone{Pool: pool, Root: root}
}

// MemorySource is where a VM's memory comes from in its zone. A VM without hugepages draws system memory, even in
// a hugepage zone; whether one may launch there is for the ADR 0004 follow-up to decide explicitly.
type MemorySource int

const (
	// SystemMemory is the zone's memory outside its hugepage pool.
	SystemMemory MemorySource = iota
	// HugepagePool is the zone's hugepage pool.
	HugepagePool
)

// HugepagesAction is what a launch does with the hugepages a clone inherits from its template.
type HugepagesAction int

const (
	// KeepHugepages leaves the template's hugepages: without root the controller cannot write them.
	KeepHugepages HugepagesAction = iota
	// SetHugepages writes Decision.HugepagesValue.
	SetHugepages
	// DeleteHugepages clears the template's hugepages, if it has any.
	DeleteHugepages
)

// GuestNUMA is where a VM's guest NUMA options come from.
type GuestNUMA int

const (
	// GuestNUMAFromGrant writes the guest NUMA nodes of the zone capacity grant, if any.
	GuestNUMAFromGrant GuestNUMA = iota
	// GuestNUMAOneNode writes one guest NUMA node holding all vCPUs and memory, bound to Decision.HostNode, in
	// place of the template's: Proxmox needs numa for hugepages.
	GuestNUMAOneNode
)

// Drop is why an instance type is not offered.
type Drop struct {
	// Types are the instance types dropped.
	Types   Types
	Reason  string
	Message string
	// Split tells that the drop is the intended split of node classes between NUMA and non-NUMA types, not a
	// problem of the template.
	Split bool
}

// Decision is the launch decision for one instance type, template and zone.
type Decision struct {
	// Offered tells that the type is offered; Drop tells why not.
	Offered bool
	Drop    Drop
	// Bound tells that the VM's memory is bound to a NUMA node: a bound VM rather than an unbound VM.
	Bound bool
	// Memory is the VM's memory source.
	Memory MemorySource
	// Hugepages is what the launch does with the template's hugepages, and HugepagesValue what SetHugepages
	// writes, the pool's page size in MiB.
	Hugepages      HugepagesAction
	HugepagesValue string
	// GuestNUMA is where the guest NUMA options come from, and HostNode the host NUMA node of GuestNUMAOneNode.
	GuestNUMA GuestNUMA
	HostNode  int
}

// Decide is the launch decision for an instance type of profile p, cloned from template t in zone z.
func Decide(p Profile, t Template, z Zone) Decision {
	d := Decision{
		Offered: true,
		Bound:   p.numa() || (p.cpuShare == 0 && z.Policy == zonecapacity.PolicyStatic),
	}

	if z.Root {
		decideAsRoot(&d, p, z)
	} else {
		decideWithoutRoot(&d, p, t, z)
	}

	if !d.Offered {
		d.Drop.Types = p.Types()
	}

	return d
}

// decideAsRoot: the controller writes hugepages itself. A NUMA type in a hugepage zone, and a non-NUMA type in one
// of one NUMA node, get the pool's; a non-NUMA type is not offered in one of two or more NUMA nodes, keeping the
// split and zone capacity pool-only; anywhere else hugepages are cleared.
func decideAsRoot(d *Decision, p Profile, z Zone) {
	pool := z.Pool

	switch {
	case pool.PageSize == 0:
		d.Hugepages = DeleteHugepages
	case p.numa():
		inPool(d, pool)
	case pool.OnOneNUMANode():
		inPool(d, pool)
		oneNode(d, pool)
	case pool.SpansNUMANodes():
		d.Hugepages = DeleteHugepages
		drop(d, ReasonPoolSpansNUMANodes, false,
			fmt.Sprintf("a non-NUMA VM as root, whose zone's hugepage pool spans %d NUMA nodes", pool.NUMANodes))
	default:
		// The NUMA node count is not known.
		d.Hugepages = DeleteHugepages
	}
}

func inPool(d *Decision, pool zonecapacity.Pool) {
	d.Memory = HugepagePool
	d.Hugepages = SetHugepages
	d.HugepagesValue = strconv.FormatUint(pool.PageSizeMiB(), 10)
}

func oneNode(d *Decision, pool zonecapacity.Pool) {
	d.GuestNUMA = GuestNUMAOneNode
	d.HostNode = pool.HostNode
}

// decideWithoutRoot: a clone keeps the template's hugepages. In a hugepage zone, NUMA types need them to be the
// pool's page size, and non-NUMA types need a template without hugepages, or, where the pool is on one NUMA node
// and so the whole zone budget, one of its page size. A template whose hugepages are not known launches neither.
// A zone without a pool launches nothing from a hugepage template.
func decideWithoutRoot(d *Decision, p Profile, t Template, z Zone) {
	pool := z.Pool

	if pool.PageSize == 0 {
		if t.Known && t.Hugepages != "" {
			drop(d, ReasonHugepagesNoPool, false,
				fmt.Sprintf("the template has hugepages=%s and the zone has no hugepage pool", t.Hugepages))
		}

		return
	}

	if !t.Known {
		drop(d, ReasonStatusUnknown, false, "hugepages of the template are not known: "+t.UnknownBecause)

		return
	}

	size := strconv.FormatUint(pool.PageSizeMiB(), 10)

	switch {
	case t.Hugepages == "" && p.numa():
		drop(d, ReasonHugepagesUnset, true,
			fmt.Sprintf("the template has no hugepages and the zone's hugepage pool has %s MiB pages", size))
	case t.Hugepages == "":
	case t.Hugepages == size && p.numa():
		d.Memory = HugepagePool
	case t.Hugepages == size && pool.OnOneNUMANode():
		d.Memory = HugepagePool
		oneNode(d, pool)
	case !p.numa() && pool.NUMANodes != 1:
		drop(d, ReasonHugepagesSet, true,
			fmt.Sprintf("the template has hugepages=%s and a non-NUMA VM would draw from the zone's hugepage pool", t.Hugepages))
	default:
		drop(d, ReasonHugepagesMismatch, false,
			fmt.Sprintf("the template has hugepages=%s and the zone's hugepage pool has %s MiB pages", t.Hugepages, size))
	}
}

func drop(d *Decision, reason string, split bool, message string) {
	d.Offered = false
	d.Drop = Drop{Reason: reason, Split: split, Message: message}
}
