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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

// numaExampleHostMoreReserved is numaExampleHost with cores 0 and 1 reserved: NUMA node 0 drops from
// 30 to 28 vCPUs, so its whole slot n1.30VCPU-126GB is gone.
var numaExampleHostMoreReserved = strings.Replace(numaExampleHost, `"reservedcpus": [0, 32]`, `"reservedcpus": [0, 1, 32, 33]`, 1)

// addNodeClaim adds a launched NodeClaim of instance type name in zone, labeled as Karpenter labels it.
func (f *fixture) addNodeClaim(claim, name, zone string) *karpv1.NodeClaim {
	f.t.Helper()

	nodeClaim := &karpv1.NodeClaim{
		Name: claim,
		Labels: map[string]string{
			corev1.LabelInstanceTypeStable: name,
			corev1.LabelTopologyRegion:     region,
			corev1.LabelTopologyZone:       zone,
			karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
			v1alpha1.LabelInstanceFamily:   instancetype.InstanceFamily(name),
		},
	}
	require.NoError(f.t, f.kube.Create(f.ctx, nodeClaim))

	return nodeClaim
}

// notDrifted checks that the instance type of nodeClaim is listed with an offering compatible with its labels,
// the check behind Karpenter's InstanceTypeNotFound drift.
func (f *fixture) notDrifted(nodeClaim *karpv1.NodeClaim) {
	f.t.Helper()

	it, err := f.p.Get(f.ctx, nodeClaim.Labels[corev1.LabelInstanceTypeStable])
	require.NoError(f.t, err, "the instance type of %s is listed", nodeClaim.Name)
	require.True(f.t, it.Offerings.HasCompatible(scheduling.NewLabelRequirements(nodeClaim.Labels)),
		"the instance type of %s has an offering compatible with it", nodeClaim.Name)
}

// TestNUMATypeKeptWhileNodeClaimsUseIt is docs/scheduler-numa.md §6.1: after more CPUs are reserved on the
// only host, its whole slot shape is gone, but its NUMA type stays listed, unavailable, while a NodeClaim
// uses it, and is dropped with the last one.
func TestNUMATypeKeptWhileNodeClaimsUseIt(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
	require.NoError(t, err)

	nodeClaim := f.addNodeClaim("node-1", "n1.30VCPU-126GB", zone)
	f.notDrifted(nodeClaim)

	f.setHosts(hosts(map[string]string{zone: numaExampleHostMoreReserved}))

	require.Contains(t, names(t, f.p), "n1.28VCPU-126GB", "the new slot shape is offered")
	f.notDrifted(nodeClaim)
	require.Equal(t, map[string]bool{zone: false}, zonesOf(t, f.p, "n1.30VCPU-126GB"), "nothing new launches on the kept NUMA type")
	require.Equal(t, 1, f.p.SlotFraction("n1.30VCPU-126GB"))

	it, err := f.p.Get(f.ctx, "n1.30VCPU-126GB")
	require.NoError(t, err)
	require.Equal(t, int64(30), it.Capacity.Cpu().Value())
	require.Equal(t, int64(126<<30), it.Capacity.Memory().Value())
	require.Equal(t, []string{"n1"}, it.Requirements.Get(v1alpha1.LabelInstanceFamily).Values())

	require.NoError(t, f.kube.Delete(f.ctx, nodeClaim))
	require.NoError(t, f.p.UpdateInstanceTypeOfferings(f.ctx))

	require.NotContains(t, names(t, f.p), "n1.30VCPU-126GB", "the kept NUMA type is dropped with its last NodeClaim")
}

// TestNUMATypeKeptInZoneThatLostIt checks that when one of two identical hosts loses a slot shape, the NUMA type
// still offered by the other host gets an unavailable offering in the zone that lost it while a NodeClaim there uses it.
func TestNUMATypeKeptInZoneThatLostIt(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{"pve-1": numaExampleHost, "pve-2": numaExampleHost}),
		numaTemplates, nil, "pve-1", "pve-2")
	require.NoError(t, err)

	nodeClaim := f.addNodeClaim("node-1", "n1.30VCPU-126GB", "pve-2")

	f.setHosts(hosts(map[string]string{"pve-1": numaExampleHost, "pve-2": numaExampleHostMoreReserved}))

	f.notDrifted(nodeClaim)
	require.Equal(t, map[string]bool{"pve-1": true, "pve-2": false}, zonesOf(t, f.p, "n1.30VCPU-126GB"))

	require.NoError(t, f.kube.Delete(f.ctx, nodeClaim))
	require.NoError(t, f.p.UpdateInstanceTypeOfferings(f.ctx))

	require.Equal(t, map[string]bool{"pve-1": true}, zonesOf(t, f.p, "n1.30VCPU-126GB"))
}

// TestNodeClaimsKeepOnlyNUMATypes checks that a NodeClaim of an unknown type outside the NUMA template families
// adds no instance type.
func TestNodeClaimsKeepOnlyNUMATypes(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
	require.NoError(t, err)

	before := names(t, f.p)

	f.addNodeClaim("node-1", "c9.30VCPU-126GB", zone)
	f.addNodeClaim("node-2", "n1", zone)
	require.NoError(t, f.p.UpdateInstanceTypeOfferings(f.ctx))

	require.ElementsMatch(t, before, names(t, f.p))
}

// TestRemovedStaticTypeInNUMAFamilyIsNotKept checks that a NodeClaim of a removed static type in the family of a
// NUMA template keeps no instance type when its name is not one that template generates, so its node drifts.
func TestRemovedStaticTypeInNUMAFamilyIsNotKept(t *testing.T) {
	t.Parallel()

	withStatic := strings.Replace(numaTemplates, "\n]", `,
  {"name": "n1.large", "capacity": {"cpu": "30", "memory": "126Gi"}},
  {"name": "n1.030VCPU-126GB", "capacity": {"cpu": "30", "memory": "126Gi"}}
]`, 1)

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), withStatic, nil, zone)
	require.NoError(t, err)
	require.Subset(t, names(t, f.p), []string{"n1.large", "n1.030VCPU-126GB"})

	f.addNodeClaim("node-1", "n1.large", zone)
	f.addNodeClaim("node-2", "n1.030VCPU-126GB", zone)

	f.setTypes(numaTemplates)

	require.NotContains(t, names(t, f.p), "n1.large")
	require.NotContains(t, names(t, f.p), "n1.030VCPU-126GB")
}

// TestNUMATypeKeptWhenNodeClaimsCannotBeListed checks that when the NodeClaims cannot be listed, the offerings
// are still refreshed, the NUMA types the NodeClaims used on the last listing are kept, and the error is returned.
func TestNUMATypeKeptWhenNodeClaimsCannotBeListed(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
	require.NoError(t, err)

	nodeClaim := f.addNodeClaim("node-1", "n1.30VCPU-126GB", zone)
	require.NoError(t, f.p.UpdateInstanceTypeOfferings(f.ctx))

	f.listErr = errors.New("apiserver unavailable")

	f.syncHosts(hosts(map[string]string{zone: numaExampleHostMoreReserved}))
	require.ErrorContains(t, f.p.UpdateInstanceTypeOfferings(f.ctx), "apiserver unavailable")

	require.Contains(t, names(t, f.p), "n1.28VCPU-126GB", "the offerings are refreshed")
	f.notDrifted(nodeClaim)
}

// TestKeptNUMATypeKeepsNodeClaimCapacityType checks that when the capacity type of a NUMA template changes, its NUMA
// type gets an unavailable offering of the capacity type each NodeClaim of it carries, so those nodes do not drift.
func TestKeptNUMATypeKeepsNodeClaimCapacityType(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
	require.NoError(t, err)

	nodeClaim := f.addNodeClaim("node-1", "n1.30VCPU-126GB", zone)

	f.setTypes(strings.Replace(numaTemplates, `{"name": "n1", "numa": 1,`, `{"name": "n1", "numa": 1, "capacitytype": "spot",`, 1))

	f.notDrifted(nodeClaim)

	it, err := f.p.Get(f.ctx, "n1.30VCPU-126GB")
	require.NoError(t, err)
	require.Equal(t, []string{karpv1.CapacityTypeSpot}, it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values(),
		"nothing new launches on demand")
}
