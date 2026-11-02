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
// class's template can launch correctly in each zone (ADR 0003), from the
// launch decisions of package launchrule, and reports what it drops on the
// node class.
//
// As root the controller sets or clears the root-only VM options itself on
// launch, so a root zone keeps every offering of the template. Without root
// a clone inherits them from the template.
package templatecheck

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/launchrule"
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

// Reasons of ConditionNodeClassTemplateCompatible. A dropped offering's reason is the launch rule's
// (launchrule.Reason*); the condition is true for an intended split and false otherwise.
const (
	// ReasonOffersNothing is the false condition's reason when both the NUMA types and the non-NUMA types are
	// dropped in a zone by drops that are each an intended split.
	ReasonOffersNothing = "TemplateOffersNothing"
	// ReasonIncompatible is the false condition's reason when the problems have more than one reason.
	ReasonIncompatible = "TemplateIncompatible"
	// ReasonOfferingsDropped is the true condition's reason when intended splits have more than one reason.
	ReasonOfferingsDropped = "OfferingsDropped"
)

// ZoneInfo tells what the check needs of a zone; cloudcapacity.Provider is one.
type ZoneInfo interface {
	// Root reports whether the controller logs in as root@pam in region.
	Root(region string) bool
	// ZonePool is as cloudcapacity.Provider.ZonePool: the zone's hugepage pool, a page size of 0 without
	// one, and the NUMA node count of the zone's topology.
	ZonePool(region, zone string) zonecapacity.Pool
}

// fromTemplate is what the check knows of t: its status.template, or, while it is unset, the message of
// its ConditionTemplateConsistent condition.
func fromTemplate(t v1alpha1.ProxmoxCommonTemplate) launchrule.Template {
	st := t.GetStatus()
	if st.Template != nil {
		return launchrule.KnownTemplate(st.Template.Hugepages)
	}

	because := "the template records no hugepages and affinity in status.template"

	if i := slices.IndexFunc(st.Conditions, func(c status.Condition) bool {
		return c.Type == v1alpha1.ConditionTemplateConsistent && c.Message != ""
	}); i >= 0 {
		because += ": " + st.Conditions[i].Message
	}

	return launchrule.UnknownTemplate(because)
}

// zone is a zone the node class selects, with what the launch rules need of it.
type zone struct {
	id     ZoneID
	launch launchrule.Zone
}

// Drop is the NUMA types or the non-NUMA types a node class does not offer in one zone, and why.
type Drop struct {
	launchrule.Drop

	Zone ZoneID
}

// Result is which instance types a node class offers in the zones it was checked on.
type Result struct {
	// template is the node class's template, and zones the non-root zones it was checked on.
	template launchrule.Template
	zones    []zone
	// drops are the dropped offerings of the NUMA types and of the non-NUMA types, by zone.
	drops []Drop
	// offerNothing are the zones where both are dropped.
	offerNothing []ZoneID
}

// ForNodeClass checks nodeClass's template on the zones of its template placements
// (status.selectedZones). The template is read only when a launch rule can drop an offering, that is in a
// non-root zone; a template that cannot be read is not known.
func ForNodeClass(ctx context.Context, reader client.Reader, nodeClass *v1alpha1.ProxmoxNodeClass, info ZoneInfo) Result {
	zones := nodeClassZones(nodeClass, info)
	if !lo.ContainsBy(zones, func(z zone) bool { return !z.launch.Root }) {
		return Result{}
	}

	t, err := nodeClass.ReadTemplate(ctx, reader)
	if err != nil {
		return check(launchrule.UnknownTemplate(err.Error()), zones)
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

		zones = append(zones, zone{id: id, launch: launchrule.NewZone(info.ZonePool(p.Region, p.Zone), info.Root(p.Region))})
	}

	return zones
}

// check makes the launch decision of the NUMA types and of the non-NUMA types from t in each non-root zone of
// zones.
func check(t launchrule.Template, zones []zone) Result {
	r := Result{template: t}

	for _, z := range zones {
		if z.launch.Root {
			continue
		}

		r.zones = append(r.zones, z)

		nothing := true

		for _, types := range []launchrule.Types{launchrule.NUMATypes, launchrule.NonNUMATypes} {
			d := launchrule.Decide(types.Profile(), t, z.launch)
			if d.Offered {
				nothing = false

				continue
			}

			r.drops = append(r.drops, Drop{Zone: z.id, Drop: d.Drop})
		}

		if nothing {
			r.offerNothing = append(r.offerNothing, z.id)
		}
	}

	slices.SortStableFunc(r.drops, func(a, b Drop) int { return compareZoneIDs(a.Zone, b.Zone) })

	return r
}

// Drops are the dropped offerings, by zone.
func (r Result) Drops() []Drop {
	return slices.Clone(r.drops)
}

// Offers reports whether an instance type of launch profile p is offered in zone. A zone the check did not
// see, or a root zone, offers every type.
func (r Result) Offers(id ZoneID, p launchrule.Profile) bool {
	i := slices.IndexFunc(r.zones, func(z zone) bool { return z.id == id })
	if i < 0 {
		return true
	}

	return launchrule.Decide(p, r.template, r.zones[i].launch).Offered
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
		messages = append(messages, fmt.Sprintf("%s: %s types dropped, %s", d.Zone, d.Types, d.Message))
	}

	message := strings.Join(messages, "; ")

	problems := []string{}

	for _, d := range r.drops {
		if !d.Split {
			problems = append(problems, d.Reason)
		}
	}

	for _, id := range r.offerNothing {
		if !slices.ContainsFunc(r.drops, func(d Drop) bool { return d.Zone == id && !d.Split }) {
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
