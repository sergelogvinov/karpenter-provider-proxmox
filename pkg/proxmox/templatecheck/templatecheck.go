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

// Package templatecheck decides, without root, which instance types a node
// class's template can launch correctly in each zone (ADR 0003).
//
// As root the controller sets or clears the root-only VM options itself on
// launch, so a root zone keeps every offering. Without root a clone inherits
// them from the template, and each rule drops the kinds of instance type the
// template cannot launch in a zone.
package templatecheck

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ZoneID names one zone of one region.
type ZoneID struct {
	Region string
	Zone   string
}

// String is the zone in the form "region/zone".
func (z ZoneID) String() string {
	return z.Region + "/" + z.Zone
}

func compareZoneIDs(a, b ZoneID) int {
	return cmp.Or(strings.Compare(a.Region, b.Region), strings.Compare(a.Zone, b.Zone))
}

// TypeKind is a kind of instance type a rule can drop.
type TypeKind string

const (
	// NonNUMATypes are the instance types outside NUMA slots: static types and CPU share types.
	NonNUMATypes TypeKind = "non-NUMA"
	// NUMATypes are the NUMA types, launched in a NUMA slot.
	NUMATypes TypeKind = "NUMA"
)

// kinds are every kind of instance type; a zone where all are dropped offers nothing.
var kinds = []TypeKind{NonNUMATypes, NUMATypes}

// Reasons of a dropped offering and of ConditionNodeClassTemplateCompatible.
const (
	// ReasonHugepagesUnset drops NUMA types in a hugepage zone: the template has no hugepages. It is the
	// intended split of a node class for non-NUMA types and leaves the condition true.
	ReasonHugepagesUnset = "TemplateHugepagesUnset"
	// ReasonHugepagesSet drops non-NUMA types in a hugepage zone of two or more NUMA nodes: the template has
	// hugepages, which a non-NUMA VM would draw from the pool unseen by the per-node view. It is the
	// intended split of a node class for NUMA types and leaves the condition true.
	ReasonHugepagesSet = "TemplateHugepagesSet"
	// ReasonHugepagesMismatch drops NUMA types in a hugepage zone, and non-NUMA types too in one of one NUMA
	// node: the template's hugepages is not the pool's page size; "any" is no page size and never matches.
	// The condition is false.
	ReasonHugepagesMismatch = "TemplateHugepagesMismatch"
	// ReasonStatusUnknown drops every type in a hugepage zone: the template's status.template is unset,
	// because its VM templates disagree or are not known yet, or the template cannot be read.
	// The condition is false.
	ReasonStatusUnknown = "TemplateStatusUnknown"
	// ReasonOffersNothing is the false condition's reason when every kind is dropped in a zone by drops
	// that are each an intended split.
	ReasonOffersNothing = "TemplateOffersNothing"
	// ReasonIncompatible is the false condition's reason when the problems have more than one reason.
	ReasonIncompatible = "TemplateIncompatible"
	// ReasonOfferingsDropped is the true condition's reason when intended splits have more than one reason.
	ReasonOfferingsDropped = "OfferingsDropped"
)

// ZoneInfo tells what the rules need of a zone; cloudcapacity.Provider is one.
type ZoneInfo interface {
	// Root reports whether the controller logs in as root@pam in region.
	Root(region string) bool
	// ZonePool is as cloudcapacity.Provider.ZonePool: the zone's hugepage pool, a page size of 0 without
	// one, and the NUMA node count of the zone's topology.
	ZonePool(region, zone string) zonecapacity.Pool
}

// template is what the check knows of a node class's template: whether the root-only options its VM
// templates share are known, and they, or why not.
type template struct {
	known bool
	// vmTemplate is the template's status.template, when known.
	vmTemplate v1alpha1.VMTemplateInfo
	// unknownBecause tells why it is not known.
	unknownBecause string
}

func knownTemplate(info v1alpha1.VMTemplateInfo) template {
	return template{known: true, vmTemplate: info}
}

func unknownTemplate(because string) template {
	return template{unknownBecause: because}
}

// fromTemplate is what the check knows of t: its status.template, or, while it is unset, the message of
// its ConditionTemplateConsistent condition.
func fromTemplate(t v1alpha1.ProxmoxCommonTemplate) template {
	st := t.GetStatus()
	if st.Template != nil {
		return knownTemplate(*st.Template)
	}

	because := "the template records no hugepages and affinity in status.template"

	if i := slices.IndexFunc(st.Conditions, func(c status.Condition) bool {
		return c.Type == v1alpha1.ConditionTemplateConsistent && c.Message != ""
	}); i >= 0 {
		because += ": " + st.Conditions[i].Message
	}

	return unknownTemplate(because)
}

// zone is a zone the node class selects, with what the rules need of it.
type zone struct {
	id   ZoneID
	root bool
	pool zonecapacity.Pool
}

// Drop is a kind of instance type a node class does not offer in one zone, and why.
type Drop struct {
	Zone    ZoneID
	Kind    TypeKind
	Reason  string
	Message string
	// Split tells that the drop is the intended split of node classes between NUMA and non-NUMA types,
	// not a problem of the template.
	Split bool
}

// rule drops the kinds of instance type t cannot launch in a non-root zone.
type rule func(t template, z zone) []Drop

// rules are checked in every non-root zone, in order.
var rules = []rule{
	hugepagesRule,
}

// Result is which instance types a node class offers in the zones it was checked on.
type Result struct {
	drops []Drop
}

// ForNodeClass checks nodeClass's template on the zones of its template placements
// (status.selectedZones). The template is read only when a rule can apply, that is in a non-root
// zone; a template that cannot be read is not known.
func ForNodeClass(ctx context.Context, reader client.Reader, nodeClass *v1alpha1.ProxmoxNodeClass, info ZoneInfo) Result {
	zones := nodeClassZones(nodeClass, info)
	if !lo.ContainsBy(zones, func(z zone) bool { return !z.root }) {
		return Result{}
	}

	t, err := nodeClass.ReadTemplate(ctx, reader)
	if err != nil {
		return check(unknownTemplate(err.Error()), zones)
	}

	return check(fromTemplate(t), zones)
}

// nodeClassZones are the zones of nodeClass's template placements, each once, with info about them.
func nodeClassZones(nodeClass *v1alpha1.ProxmoxNodeClass, info ZoneInfo) []zone {
	zones := []zone{}

	for _, p := range nodeClass.TemplatePlacements("") {
		id := ZoneID{Region: p.Region, Zone: p.Zone}
		if lo.ContainsBy(zones, func(z zone) bool { return z.id == id }) {
			continue
		}

		zones = append(zones, zone{id: id, root: info.Root(p.Region), pool: info.ZonePool(p.Region, p.Zone)})
	}

	return zones
}

// check applies the rules to t in each non-root zone of zones.
func check(t template, zones []zone) Result {
	r := Result{}

	for _, z := range zones {
		if z.root {
			continue
		}

		for _, apply := range rules {
			r.drops = append(r.drops, apply(t, z)...)
		}
	}

	slices.SortStableFunc(r.drops, func(a, b Drop) int { return compareZoneIDs(a.Zone, b.Zone) })

	return r
}

// Drops are the dropped offerings, by zone.
func (r Result) Drops() []Drop {
	return slices.Clone(r.drops)
}

// Offers reports whether instance types of kind are offered in zone. A zone the check did not see
// offers every kind.
func (r Result) Offers(zone ZoneID, kind TypeKind) bool {
	return !slices.ContainsFunc(r.drops, func(d Drop) bool {
		return d.Zone == zone && d.Kind == kind
	})
}

// SetCondition sets ConditionNodeClassTemplateCompatible on nodeClass. It is false when a drop is a problem of the
// template, or a zone offers nothing, with the problems' reason or ReasonIncompatible when they differ;
// otherwise it is true, with the reason of the intended splits, if any. Its message names each dropped
// offering.
func (r Result) SetCondition(nodeClass *v1alpha1.ProxmoxNodeClass) {
	conditions := nodeClass.StatusConditions()

	if len(r.drops) == 0 {
		conditions.SetTrue(v1alpha1.ConditionNodeClassTemplateCompatible)

		return
	}

	messages := make([]string, 0, len(r.drops))
	for _, d := range r.drops {
		messages = append(messages, fmt.Sprintf("%s: %s types dropped, %s", d.Zone, d.Kind, d.Message))
	}

	message := strings.Join(messages, "; ")

	problems := []string{}

	for _, d := range r.drops {
		if !d.Split {
			problems = append(problems, d.Reason)
		}
	}

	for _, id := range lo.Uniq(lo.Map(r.drops, func(d Drop, _ int) ZoneID { return d.Zone })) {
		if lo.EveryBy(kinds, func(k TypeKind) bool { return !r.Offers(id, k) }) &&
			!slices.ContainsFunc(r.drops, func(d Drop) bool { return d.Zone == id && !d.Split }) {
			problems = append(problems, ReasonOffersNothing)
		}
	}

	if len(problems) > 0 {
		conditions.SetFalse(v1alpha1.ConditionNodeClassTemplateCompatible, oneReason(problems, ReasonIncompatible), message)

		return
	}

	reasons := lo.Map(r.drops, func(d Drop, _ int) string { return d.Reason })
	conditions.SetTrueWithReason(v1alpha1.ConditionNodeClassTemplateCompatible, oneReason(reasons, ReasonOfferingsDropped), message)
}

// oneReason is the reason all of reasons share, or mixed.
func oneReason(reasons []string, mixed string) string {
	if len(lo.Uniq(reasons)) == 1 {
		return reasons[0]
	}

	return mixed
}

// NonNUMAInPool reports whether non-NUMA types launched from a template whose hugepages is hugepages draw
// from the zone's hugepage pool: the zone has a pool on one NUMA node, so the pool is the whole zone budget,
// and hugepages is the pool's page size (ADR 0003). hugepagesRule offers non-NUMA types from such a template,
// and without root their launch gets one guest NUMA node bound to host node 0, so the two must agree.
func NonNUMAInPool(pool zonecapacity.Pool, hugepages string) bool {
	return pool.PageSize > 0 && pool.NUMANodes == 1 && hugepages == pageSizeMiB(pool)
}

// pageSizeMiB is pool's page size in MiB, as a template's hugepages gives it.
func pageSizeMiB(pool zonecapacity.Pool) string {
	return strconv.FormatUint(pool.PageSize/zonecapacity.MiB, 10)
}

// hugepagesRule: in a zone with a hugepage pool, NUMA types need the template's hugepages to be the pool's
// page size, and non-NUMA types need a template without hugepages, or one NonNUMAInPool lets them draw from
// the pool. A template whose hugepages is not known launches neither.
func hugepagesRule(t template, z zone) []Drop {
	if z.pool.PageSize == 0 {
		return nil
	}

	pool := pageSizeMiB(z.pool)

	drop := func(kind TypeKind, reason string, split bool, message string) Drop {
		return Drop{Zone: z.id, Kind: kind, Reason: reason, Split: split, Message: message}
	}

	if !t.known {
		message := "hugepages of the template are not known: " + t.unknownBecause

		return []Drop{
			drop(NonNUMATypes, ReasonStatusUnknown, false, message),
			drop(NUMATypes, ReasonStatusUnknown, false, message),
		}
	}

	hugepages := t.vmTemplate.Hugepages
	if hugepages == "" {
		return []Drop{drop(NUMATypes, ReasonHugepagesUnset, true,
			fmt.Sprintf("the template has no hugepages and the zone's hugepage pool has %s MiB pages", pool))}
	}

	if NonNUMAInPool(z.pool, hugepages) {
		return nil
	}

	nonNUMA := drop(NonNUMATypes, ReasonHugepagesSet, true,
		fmt.Sprintf("the template has hugepages=%s and a non-NUMA VM would draw from the zone's hugepage pool", hugepages))

	if hugepages == pool {
		return []Drop{nonNUMA}
	}

	mismatch := fmt.Sprintf("the template has hugepages=%s and the zone's hugepage pool has %s MiB pages", hugepages, pool)
	if z.pool.NUMANodes == 1 {
		nonNUMA = drop(NonNUMATypes, ReasonHugepagesMismatch, false, mismatch)
	}

	return []Drop{nonNUMA, drop(NUMATypes, ReasonHugepagesMismatch, false, mismatch)}
}
