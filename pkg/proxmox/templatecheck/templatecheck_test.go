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

package templatecheck_test

import (
	"testing"

	"github.com/awslabs/operatorpkg/status"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/launchrule"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/templatecheck"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	giB = uint64(1) << 30
	miB = uint64(1) << 20

	region = "region-1"
)

var (
	// hugepageZone has the hugepage pool of the test, plainZone has none.
	hugepageZone = templatecheck.ZoneID{Region: region, Zone: "pve-1"}
	plainZone    = templatecheck.ZoneID{Region: region, Zone: "pve-2"}
)

// zones is what the check knows of the zones: each zone's pool, and root, and the zones the NodeClass has
// template placements on, hugepageZone and plainZone when placed is empty.
type zones struct {
	root   bool
	pools  map[templatecheck.ZoneID]zonecapacity.Pool
	placed []templatecheck.ZoneID
}

var (
	// numaType and nonNUMAType are a NUMA type and a non-NUMA type.
	numaType    = launchrule.NUMAType(1)
	nonNUMAType = launchrule.DedicatedType()
)

func (z zones) Root(string) bool { return z.root }

func (z zones) ZonePool(region, zone string) zonecapacity.Pool {
	return z.pools[templatecheck.ZoneID{Region: region, Zone: zone}]
}

// poolZones are hugepageZone, with a pool of pageSize over two NUMA nodes, and plainZone, without root.
func poolZones(pageSize uint64) zones {
	return numaPoolZones(pageSize, 2)
}

// numaPoolZones are hugepageZone, with a pool of pageSize over numaNodes NUMA nodes, and plainZone with
// one NUMA node and no pool, without root; the NodeClass is placed on hugepageZone only.
func numaPoolZones(pageSize uint64, numaNodes int) zones {
	return zones{pools: map[templatecheck.ZoneID]zonecapacity.Pool{
		hugepageZone: {PageSize: pageSize, NUMANodes: numaNodes},
		plainZone:    {NUMANodes: 1},
	}, placed: []templatecheck.ZoneID{hugepageZone}}
}

// hugepages is a template status whose VM templates share hugepages h ("" for none).
func hugepages(h string) *v1alpha1.ProxmoxTemplateStatus {
	return &v1alpha1.ProxmoxTemplateStatus{Template: &v1alpha1.VMTemplateInfo{Hugepages: h}}
}

// unknown is a template status whose status.template is unset because its VM templates disagree.
func unknown() *v1alpha1.ProxmoxTemplateStatus {
	st := &v1alpha1.ProxmoxTemplateStatus{}
	st.Conditions = []status.Condition{{
		Type: v1alpha1.ConditionTemplateConsistent, Status: "False", Reason: "VMTemplatesDisagree", Message: "VM templates disagree on hugepages",
	}}

	return st
}

// check runs the check on a NodeClass with template placements on info's placed zones, whose
// ProxmoxUnmanagedTemplate has status st (nil: the template does not exist), and sets its condition.
func check(t *testing.T, st *v1alpha1.ProxmoxTemplateStatus, info zones) (templatecheck.Result, *status.Condition) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	objects := []client.Object{}

	if st != nil {
		template := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
		template.Status = *st
		objects = append(objects, template)
	}

	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: "talos"},
		},
	}
	placed := info.placed
	if len(placed) == 0 {
		placed = []templatecheck.ZoneID{hugepageZone, plainZone}
	}

	placements := []v1alpha1.TemplatePlacement{}
	for i, z := range placed {
		placements = append(placements, v1alpha1.TemplatePlacement{Region: z.Region, Zone: z.Zone, TemplateID: uint64(100 + i)})
	}

	nodeClass.SetTemplatePlacements(placements)

	r := templatecheck.ForNodeClass(t.Context(), reader, nodeClass, info)
	r.SetCondition(nodeClass)

	cond := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible)
	require.NotNil(t, cond)

	return r, cond
}

func TestHugepageZoneWithoutRoot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		status          *v1alpha1.ProxmoxTemplateStatus
		pageSize        uint64
		nonNUMA, numa   bool
		compatible      bool
		reason, message string
	}{
		{
			name: "matching 1GiB template offers only NUMA types", status: hugepages("1024"), pageSize: giB,
			numa: true, compatible: true, reason: launchrule.ReasonHugepagesSet, message: "non-NUMA types dropped",
		},
		{
			name: "matching 2MiB template offers only NUMA types", status: hugepages("2"), pageSize: 2 * miB,
			numa: true, compatible: true, reason: launchrule.ReasonHugepagesSet, message: "non-NUMA types dropped",
		},
		{
			name: "template without hugepages offers only non-NUMA types", status: hugepages(""), pageSize: giB,
			nonNUMA: true, compatible: true, reason: launchrule.ReasonHugepagesUnset, message: "NUMA types dropped",
		},
		{
			name: "template of another page size offers nothing", status: hugepages("2"), pageSize: giB,
			reason: launchrule.ReasonHugepagesMismatch, message: "hugepages=2 and the zone's hugepage pool has 1024 MiB pages",
		},
		{
			name: "hugepages any is no page size and offers nothing", status: hugepages("any"), pageSize: giB,
			reason: launchrule.ReasonHugepagesMismatch, message: "hugepages=any",
		},
		{
			name: "template whose VM templates disagree offers nothing", status: unknown(), pageSize: giB,
			reason: launchrule.ReasonStatusUnknown, message: "VM templates disagree on hugepages",
		},
		{
			name: "template that cannot be read offers nothing", pageSize: giB,
			reason: launchrule.ReasonStatusUnknown, message: "not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, cond := check(t, tc.status, poolZones(tc.pageSize))

			require.Equal(t, tc.nonNUMA, r.Offers(hugepageZone, nonNUMAType), "non-NUMA types offered")
			require.Equal(t, tc.numa, r.Offers(hugepageZone, numaType), "NUMA types offered")
			require.True(t, r.Offers(plainZone, nonNUMAType), "a zone without a hugepage pool offers everything")
			require.True(t, r.Offers(plainZone, numaType), "a zone without a hugepage pool offers everything")

			require.Equal(t, tc.compatible, cond.IsTrue(), "condition status")
			require.Equal(t, tc.reason, cond.Reason)
			require.Contains(t, cond.Message, "region-1/pve-1")
			require.Contains(t, cond.Message, tc.message)
			require.NotContains(t, cond.Message, "pve-2", "a zone without a hugepage pool is not judged")
		})
	}
}

// TestOneNUMANodeHugepageZoneWithoutRoot checks that in a hugepage zone of one NUMA node a hugepage template
// offers non-NUMA types too, since the pool is the whole zone budget there, while a template of another page
// size offers nothing and a template without hugepages keeps its split.
func TestOneNUMANodeHugepageZoneWithoutRoot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		status          *v1alpha1.ProxmoxTemplateStatus
		pageSize        uint64
		nonNUMA, numa   bool
		compatible      bool
		reason, message string
	}{
		{
			name: "matching 1GiB template offers every type", status: hugepages("1024"), pageSize: giB,
			nonNUMA: true, numa: true, compatible: true, reason: v1alpha1.ConditionNodeClassTemplateCompatible,
		},
		{
			name: "matching 2MiB template offers every type", status: hugepages("2"), pageSize: 2 * miB,
			nonNUMA: true, numa: true, compatible: true, reason: v1alpha1.ConditionNodeClassTemplateCompatible,
		},
		{
			name: "template without hugepages offers only non-NUMA types", status: hugepages(""), pageSize: giB,
			nonNUMA: true, compatible: true, reason: launchrule.ReasonHugepagesUnset, message: "NUMA types dropped",
		},
		{
			name: "template of another page size offers nothing", status: hugepages("2"), pageSize: giB,
			reason: launchrule.ReasonHugepagesMismatch, message: "non-NUMA types dropped, the template has hugepages=2 and the zone's hugepage pool has 1024 MiB pages",
		},
		{
			name: "hugepages any is no page size and offers nothing", status: hugepages("any"), pageSize: giB,
			reason: launchrule.ReasonHugepagesMismatch, message: "non-NUMA types dropped, the template has hugepages=any",
		},
		{
			name: "template whose VM templates disagree offers nothing", status: unknown(), pageSize: giB,
			reason: launchrule.ReasonStatusUnknown, message: "VM templates disagree on hugepages",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, cond := check(t, tc.status, numaPoolZones(tc.pageSize, 1))

			require.Equal(t, tc.nonNUMA, r.Offers(hugepageZone, nonNUMAType), "non-NUMA types offered")
			require.Equal(t, tc.numa, r.Offers(hugepageZone, numaType), "NUMA types offered")
			require.True(t, r.Offers(plainZone, nonNUMAType), "a zone without a hugepage pool offers everything")
			require.True(t, r.Offers(plainZone, numaType), "a zone without a hugepage pool offers everything")

			require.Equal(t, tc.compatible, cond.IsTrue(), "condition status")
			require.Equal(t, tc.reason, cond.Reason)
			require.Contains(t, cond.Message, tc.message)

			if tc.nonNUMA && tc.numa {
				require.Empty(t, r.Drops())
				require.NotContains(t, cond.Message, "pve-1", "no drop is named for the zone")
			}
		})
	}
}

// TestSplitNodeClassesAreCompatible checks that the pair of NodeClasses split as docs/hugepages.md describes,
// one with a hugepage template for NUMA types and one without hugepages, both show a true condition.
func TestSplitNodeClassesAreCompatible(t *testing.T) {
	t.Parallel()

	_, numa := check(t, hugepages("1024"), poolZones(giB))
	_, nonNUMA := check(t, hugepages(""), poolZones(giB))

	require.True(t, numa.IsTrue(), numa.Message)
	require.True(t, nonNUMA.IsTrue(), nonNUMA.Message)
}

// TestUnaffected checks that root zones keep every offering whatever the template, and zones without a hugepage
// pool every offering of a template without hugepages or whose hugepages are not known, and that the
// condition is then true with nothing in its message.
func TestUnaffected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		info     zones
		statuses []*v1alpha1.ProxmoxTemplateStatus
	}{
		{
			name:     "root zone with a hugepage pool",
			info:     zones{root: true, pools: map[templatecheck.ZoneID]zonecapacity.Pool{hugepageZone: {PageSize: giB, NUMANodes: 1}}},
			statuses: []*v1alpha1.ProxmoxTemplateStatus{hugepages(""), hugepages("1024"), hugepages("any"), unknown(), nil},
		},
		{
			name:     "non-root zones without a hugepage pool",
			info:     zones{},
			statuses: []*v1alpha1.ProxmoxTemplateStatus{hugepages(""), unknown(), nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, st := range tc.statuses {
				r, cond := check(t, st, tc.info)
				require.Empty(t, r.Drops())
				require.True(t, cond.IsTrue())
				require.Equal(t, v1alpha1.ConditionNodeClassTemplateCompatible, cond.Reason)
				require.Empty(t, cond.Message)
			}
		})
	}
}

// TestHugepageTemplateInZoneWithoutPool checks that without root a hugepage template offers nothing in a zone
// without a hugepage pool, where its clones would fail to start, and that this is a problem of the template
// (#321).
func TestHugepageTemplateInZoneWithoutPool(t *testing.T) {
	t.Parallel()

	for _, h := range []string{"1024", "2", "any"} {
		t.Run(h, func(t *testing.T) {
			t.Parallel()

			r, _ := check(t, hugepages(h), numaPoolZones(giB, 1))
			require.True(t, r.Offers(plainZone, numaType), "plainZone is not placed, so not checked")

			r, cond := check(t, hugepages(h), zones{placed: []templatecheck.ZoneID{plainZone}})

			require.False(t, r.Offers(plainZone, numaType), "NUMA types offered")
			require.False(t, r.Offers(plainZone, nonNUMAType), "non-NUMA types offered")
			require.False(t, r.Offers(plainZone, launchrule.CPUShareType(2)), "CPU share types offered")

			require.True(t, cond.IsFalse())
			require.Equal(t, launchrule.ReasonHugepagesNoPool, cond.Reason)
			require.Contains(t, cond.Message, "region-1/pve-2: NUMA types dropped, the template has hugepages="+h+" and the zone has no hugepage pool")
			require.Contains(t, cond.Message, "region-1/pve-2: non-NUMA types dropped")
		})
	}
}

// TestConditionIsNotReadyDependent checks that a false condition does not make the NodeClass not ready.
func TestConditionIsNotReadyDependent(t *testing.T) {
	t.Parallel()

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassTemplateReady)
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassMetadataOptionsReady)

	r, cond := check(t, hugepages("2"), poolZones(giB))
	require.True(t, cond.IsFalse())

	r.SetCondition(nodeClass)
	require.True(t, nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible).IsFalse())
	require.True(t, nodeClass.StatusConditions().Root().IsTrue())
}

// TestZoneNotChecked checks that a zone outside the NodeClass's template placements offers every instance type.
func TestZoneNotChecked(t *testing.T) {
	t.Parallel()

	r, _ := check(t, hugepages(""), poolZones(giB))
	require.True(t, r.Offers(templatecheck.ZoneID{Region: region, Zone: "pve-9"}, numaType))
}
