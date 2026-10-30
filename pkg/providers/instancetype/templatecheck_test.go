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

package instancetype_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// hugepageHost is smallHost whose NUMA node memory is a pool of 63 1GiB hugepages: its whole slot has the
// shape of smallHost's.
const hugepageHost = `{"sockets": 1, "threads": 2, "nodes": {"0": {"cpus": "0-15", "memsize": 67645734912}}, "hugepagesize": 1073741824}`

const (
	// hugepageZone has a hugepage pool, plainZone has none.
	hugepageZone = "pve-1"
	plainZone    = "pve-2"
	// nonNUMAType is the static type of numaTemplates, numaType the whole slot of smallHost.
	nonNUMAType = "c1.2VCPU-4GB"
	numaType    = "n1.16VCPU-63GB"
)

// templateCheckFixture is a provider on a zone with a hugepage pool and one without, and a NodeClass whose
// ProxmoxUnmanagedTemplate has placements on both.
func templateCheckFixture(t *testing.T, root bool, vmTemplate *v1alpha1.VMTemplateInfo) (*fixture, *v1alpha1.ProxmoxNodeClass) {
	t.Helper()

	f, err := newFixtureAs(t, config.Privileges{region: root}, "static",
		hosts(map[string]string{hugepageZone: hugepageHost, plainZone: smallHost}), numaTemplates, nil, hugepageZone, plainZone)
	require.NoError(t, err)

	template := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "hugepages"}
	template.Status.Template = vmTemplate
	require.NoError(t, f.kube.Create(f.ctx, template))

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "numa",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: template.Name},
		},
	}
	nodeClass.SetTemplatePlacements([]v1alpha1.TemplatePlacement{
		{Region: region, Zone: hugepageZone, TemplateID: 100},
		{Region: region, Zone: plainZone, TemplateID: 101},
	})

	return f, nodeClass
}

// offeredZones are the zones where nodeClass offers instance type name.
func offeredZones(t *testing.T, f *fixture, nodeClass *v1alpha1.ProxmoxNodeClass, name string) []string {
	t.Helper()

	list, err := f.p.List(context.Background(), nodeClass)
	require.NoError(t, err)

	for _, it := range list {
		if it.Name != name {
			continue
		}

		zones := []string{}
		for _, o := range it.Offerings {
			zones = append(zones, o.Requirements.Get(corev1.LabelTopologyZone).Any())
		}

		return zones
	}

	require.Failf(t, "instance type not listed", "%s", name)

	return nil
}

func TestListFiltersByTemplateHugepages(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		root          bool
		vmTemplate    *v1alpha1.VMTemplateInfo
		nonNUMA, numa []string
	}{
		{
			name:       "matching hugepages offer NUMA types only in the hugepage zone",
			vmTemplate: &v1alpha1.VMTemplateInfo{Hugepages: "1024"},
			nonNUMA:    []string{plainZone}, numa: []string{hugepageZone, plainZone},
		},
		{
			name:       "no hugepages offer non-NUMA types only in the hugepage zone",
			vmTemplate: &v1alpha1.VMTemplateInfo{},
			nonNUMA:    []string{hugepageZone, plainZone}, numa: []string{plainZone},
		},
		{
			name:       "wrong page size offers nothing in the hugepage zone",
			vmTemplate: &v1alpha1.VMTemplateInfo{Hugepages: "2"},
			nonNUMA:    []string{plainZone}, numa: []string{plainZone},
		},
		{
			name:    "unknown template offers nothing in the hugepage zone",
			nonNUMA: []string{plainZone}, numa: []string{plainZone},
		},
		{
			name:       "root offers everything",
			root:       true,
			vmTemplate: &v1alpha1.VMTemplateInfo{},
			nonNUMA:    []string{hugepageZone, plainZone}, numa: []string{hugepageZone, plainZone},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, nodeClass := templateCheckFixture(t, tc.root, tc.vmTemplate)

			require.ElementsMatch(t, tc.nonNUMA, offeredZones(t, f, nodeClass, nonNUMAType), "non-NUMA type")
			require.ElementsMatch(t, tc.numa, offeredZones(t, f, nodeClass, numaType), "NUMA type")
		})
	}
}

// TestListWithoutNodeClass checks that a List without a NodeClass, and the provider's own lookups,
// keep every offering.
func TestListWithoutNodeClass(t *testing.T) {
	t.Parallel()

	f, _ := templateCheckFixture(t, false, &v1alpha1.VMTemplateInfo{})

	list, err := f.p.List(context.Background(), nil)
	require.NoError(t, err)

	for _, it := range list {
		if it.Name == numaType {
			require.Len(t, it.Offerings, 2)
		}
	}

	it, err := f.p.Get(context.Background(), numaType)
	require.NoError(t, err)
	require.Len(t, it.Offerings, 2)
}

// TestListWithoutPlacements checks that a NodeClass without template placements, which launches nowhere,
// lists every instance type with no offerings, even with root.
func TestListWithoutPlacements(t *testing.T) {
	t.Parallel()

	for _, root := range []bool{false, true} {
		f, nodeClass := templateCheckFixture(t, root, &v1alpha1.VMTemplateInfo{})
		nodeClass.SetTemplatePlacements(nil)

		list, err := f.p.List(context.Background(), nodeClass)
		require.NoError(t, err)
		require.NotEmpty(t, list)

		for _, it := range list {
			require.Empty(t, it.Offerings, it.Name)
			require.False(t, it.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(karpv1.CapacityTypeOnDemand), it.Name)
		}
	}
}
