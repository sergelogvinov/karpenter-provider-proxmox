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
	"fmt"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// TestLoaderValidatesCapacityType checks that the loader accepts the capacity types on-demand, spot, reserved
// and empty, and rejects any other value naming the instance type, the value and the capacity types allowed.
func TestLoaderValidatesCapacityType(t *testing.T) {
	t.Parallel()

	for _, ct := range []string{"", "on-demand", "spot", "reserved"} {
		_, err := newProvider(t, "simple", fmt.Sprintf(`[{"name": "c1.2VCPU-4GB", "capacitytype": %q, "capacity": {"cpu": "2", "memory": "4Gi"}}]`, ct))
		require.NoError(t, err, "capacitytype %q", ct)
	}

	for _, ct := range []string{"reseved", "Reserved", "ondemand"} {
		_, err := newProvider(t, "simple", fmt.Sprintf(`[{"name": "c1.2VCPU-4GB", "capacitytype": %q, "capacity": {"cpu": "2", "memory": "4Gi"}}]`, ct))
		require.ErrorContains(t, err, "c1.2VCPU-4GB", "capacitytype %q", ct)
		require.ErrorContains(t, err, fmt.Sprintf("%q", ct), "capacitytype %q", ct)
		require.ErrorContains(t, err, "must be on-demand, spot or reserved", "capacitytype %q", ct)
	}
}

// capacityTypes are instance types of one size, one per capacity type.
const capacityTypes = `[
  {"name": "c1.2VCPU-4GB", "capacity": {"cpu": "2", "memory": "4Gi"}},
  {"name": "s1.2VCPU-4GB", "capacitytype": "spot", "capacity": {"cpu": "2", "memory": "4Gi"}},
  {"name": "r1.2VCPU-4GB", "capacitytype": "reserved", "capacity": {"cpu": "2", "memory": "4Gi"}}
]`

// TestCapacityTypePrice checks that for one size reserved costs a quarter and spot a half of on-demand.
func TestCapacityTypePrice(t *testing.T) {
	t.Parallel()

	p, err := newProvider(t, "simple", capacityTypes)
	require.NoError(t, err)

	onDemand := offerings(t, p, "c1.2VCPU-4GB")
	spot := offerings(t, p, "s1.2VCPU-4GB")
	reserved := offerings(t, p, "r1.2VCPU-4GB")
	require.Len(t, onDemand, 1)
	require.Len(t, spot, 1)
	require.Len(t, reserved, 1)

	require.Equal(t, karpv1.CapacityTypeOnDemand, onDemand[0].CapacityType())
	require.Equal(t, karpv1.CapacityTypeSpot, spot[0].CapacityType())
	require.Equal(t, karpv1.CapacityTypeReserved, reserved[0].CapacityType())

	require.InDelta(t, 0.5*onDemand[0].Price, spot[0].Price, 1e-9)
	require.InDelta(t, 0.25*onDemand[0].Price, reserved[0].Price, 1e-9)
}

// TestReservationIDRequirement checks that a reserved offering requires the reservation ID of its region, zone and
// instance type, and that on-demand and spot offerings require the reservation-id label not to exist.
func TestReservationIDRequirement(t *testing.T) {
	t.Parallel()

	require.Equal(t, v1alpha1.LabelReservationID, cloudprovider.ReservationIDLabel)
	require.True(t, cloudprovider.ReservedCapacityLabels.Has(v1alpha1.LabelReservationID))
	require.True(t, karpv1.WellKnownLabels.Has(v1alpha1.LabelReservationID))

	p, err := newProvider(t, "simple", capacityTypes)
	require.NoError(t, err)

	reserved := offerings(t, p, "r1.2VCPU-4GB")
	require.Len(t, reserved, 1)
	require.Equal(t, v1alpha1.ReservationID(region, zone, "r1.2VCPU-4GB"), reserved[0].ReservationID())
	require.Equal(t, corev1.NodeSelectorOpIn, reserved[0].Requirements.Get(v1alpha1.LabelReservationID).Operator())

	for _, name := range []string{"c1.2VCPU-4GB", "s1.2VCPU-4GB"} {
		o := offerings(t, p, name)
		require.Len(t, o, 1)
		require.True(t, o[0].Requirements.Has(v1alpha1.LabelReservationID), name)
		require.Equal(t, corev1.NodeSelectorOpDoesNotExist, o[0].Requirements.Get(v1alpha1.LabelReservationID).Operator(), name)
	}
}

// TestReservedCPUShareType checks that a CPU share type can be reserved.
func TestReservedCPUShareType(t *testing.T) {
	t.Parallel()

	p, err := newProvider(t, "simple", `[{"name": "e2.2VCPU-4GB", "cpushare": 2, "capacitytype": "reserved", "capacity": {"cpu": "2", "memory": "4Gi"}}]`)
	require.NoError(t, err)

	reserved := offerings(t, p, "e2.2VCPU-4GB")
	require.Len(t, reserved.Available(), 1)
	require.Equal(t, karpv1.CapacityTypeReserved, reserved[0].CapacityType())
	require.Equal(t, v1alpha1.ReservationID(region, zone, "e2.2VCPU-4GB"), reserved[0].ReservationID())

	it, err := p.Get(context.Background(), "e2.2VCPU-4GB")
	require.NoError(t, err)
	require.Equal(t, []string{karpv1.CapacityTypeReserved}, it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values())
}

// TestReservedNUMATemplate checks that a reserved NUMA template makes every NUMA type of it reserved.
func TestReservedNUMATemplate(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), reservedNUMATemplates, nil, zone)
	require.NoError(t, err)

	for _, name := range []string{"n1.30VCPU-126GB", "n1.32VCPU-126GB"} {
		reserved := offerings(t, p, name)
		require.Len(t, reserved.Available(), 1, name)
		require.Equal(t, karpv1.CapacityTypeReserved, reserved[0].CapacityType(), name)
		require.Equal(t, v1alpha1.ReservationID(region, zone, name), reserved[0].ReservationID(), name)

		it, err := p.Get(context.Background(), name)
		require.NoError(t, err)
		require.Equal(t, []string{karpv1.CapacityTypeReserved}, it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values(), name)
	}

	onDemand := offerings(t, p, "n2.14VCPU-63GB")
	require.Equal(t, corev1.NodeSelectorOpDoesNotExist, onDemand[0].Requirements.Get(v1alpha1.LabelReservationID).Operator())
}

// reservedNUMATemplates are numaTemplates with the n1 template reserved.
var reservedNUMATemplates = strings.Replace(numaTemplates, `{"name": "n1", "numa": 1,`, `{"name": "n1", "numa": 1, "capacitytype": "reserved",`, 1)

// TestKeptNUMATypeKeepsReservation checks that when a reserved NUMA template becomes on-demand, its NUMA type keeps
// an unavailable reserved offering with the reservation ID for the reserved NodeClaims of it, so they do not drift.
func TestKeptNUMATypeKeepsReservation(t *testing.T) {
	t.Parallel()

	name := "n1.30VCPU-126GB"

	f, err := newFixture(t, "static", hosts(map[string]string{zone: numaExampleHost}), reservedNUMATemplates, nil, zone)
	require.NoError(t, err)

	nodeClaim := f.addNodeClaim("node-1", name, zone)
	nodeClaim.Labels[karpv1.CapacityTypeLabelKey] = karpv1.CapacityTypeReserved
	nodeClaim.Labels[v1alpha1.LabelReservationID] = v1alpha1.ReservationID(region, zone, name)
	require.NoError(t, f.kube.Update(f.ctx, nodeClaim))

	f.setTypes(numaTemplates)

	f.notDrifted(nodeClaim)

	kept, ok := lo.Find(offerings(t, f.p, name), func(o *cloudprovider.Offering) bool {
		return o.CapacityType() == karpv1.CapacityTypeReserved
	})
	require.True(t, ok)
	require.False(t, kept.Available)
	require.Equal(t, v1alpha1.ReservationID(region, zone, name), kept.ReservationID())
}

// reservedTypes are a reserved and an on-demand type of 4 vCPU and 8GiB, and a reserved type of 1 vCPU and 2GiB.
const reservedTypes = `[
  {"name": "r1.4VCPU-8GB", "capacitytype": "reserved", "capacity": {"cpu": "4", "memory": "8Gi"}},
  {"name": "c1.4VCPU-8GB", "capacity": {"cpu": "4", "memory": "8Gi"}},
  {"name": "r1.1VCPU-2GB", "capacitytype": "reserved", "capacity": {"cpu": "1", "memory": "2Gi"}}
]`

// TestReservationCapacity checks that a reserved offering's reservation capacity is how many more VMs of the type
// fit in the zone: smallHost has 16 vCPUs and 63GiB, room for 4 VMs of 4 vCPU and 8GiB, and for 2 once 8 of its
// CPUs are reserved. An on-demand offering has no reservation capacity.
func TestReservationCapacity(t *testing.T) {
	t.Parallel()

	f, err := newFixture(t, "static", hosts(map[string]string{zone: smallHost}), reservedTypes, nil, zone)
	require.NoError(t, err)

	reserved := offerings(t, f.p, "r1.4VCPU-8GB")
	require.Len(t, reserved, 1)
	require.Equal(t, 4, reserved[0].ReservationCapacity)
	require.True(t, reserved[0].Launchable())

	onDemand := offerings(t, f.p, "c1.4VCPU-8GB")
	require.Len(t, onDemand, 1)
	require.Equal(t, 0, onDemand[0].ReservationCapacity)
	require.True(t, onDemand[0].Launchable())

	f.setHosts(hosts(map[string]string{zone: strings.Replace(smallHost, `"reservedmemory"`, `"reservedcpus": [0, 1, 2, 3, 4, 5, 6, 7], "reservedmemory"`, 1)}))

	reserved = offerings(t, f.p, "r1.4VCPU-8GB")
	require.Len(t, reserved, 1)
	require.Equal(t, 2, reserved[0].ReservationCapacity)
}

// pinnedVMs adds a running VM of 8 vCPUs pinned to each of affinities to the zone.
func pinnedVMs(affinities ...string) func(*fakeapi.Cluster) {
	return func(cluster *fakeapi.Cluster) {
		for i, affinity := range affinities {
			cluster.Node(zone).AddVM(100+i, &qemu.Config{
				Cores: new(8), Memory: &qemu.Memory{Current: new(1024)}, Affinity: affinity,
			}, fakeapi.WithStatus(qemu.VMStatusRunning))
		}
	}
}

// TestReservationCapacityDropsWithVMs checks that the VMs of a zone take from the reservation capacity, and that a
// full zone keeps its reserved offering Available with no reservation capacity, so it is not Launchable.
func TestReservationCapacityDropsWithVMs(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: smallHost}), reservedTypes, pinnedVMs("0-7"), zone)
	require.NoError(t, err)

	reserved := offerings(t, p, "r1.4VCPU-8GB")
	require.Len(t, reserved, 1)
	require.Equal(t, 2, reserved[0].ReservationCapacity)

	p, err = newProviderOn(t, "static", hosts(map[string]string{zone: smallHost}), reservedTypes, pinnedVMs("0-7", "8-15"), zone)
	require.NoError(t, err)

	reserved = offerings(t, p, "r1.4VCPU-8GB")
	require.Len(t, reserved, 1)
	require.Equal(t, 0, reserved[0].ReservationCapacity)
	require.True(t, reserved[0].Available)
	require.False(t, reserved[0].Launchable())

	onDemand := offerings(t, p, "c1.4VCPU-8GB")
	require.Len(t, onDemand, 1)
	require.False(t, onDemand[0].Available)
}

// TestReservationCapacityLimit checks that the reservation capacity stops at the count limit of 32: numaExampleHost
// has 62 vCPUs free, room for more VMs of 1 vCPU and 2GiB.
func TestReservationCapacityLimit(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), reservedTypes, nil, zone)
	require.NoError(t, err)

	reserved := offerings(t, p, "r1.1VCPU-2GB")
	require.Len(t, reserved, 1)
	require.Equal(t, 32, reserved[0].ReservationCapacity)
	require.Equal(t, cloudcapacity.CountLimit, reserved[0].ReservationCapacity)
}

// TestReservedNUMATypeCapacity checks that a reserved NUMA type's reservation capacity is its free slots:
// numaExampleHost has one whole-node slot of each shape, one taken by a VM in the second fixture.
func TestReservedNUMATypeCapacity(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), reservedNUMATemplates, nil, zone)
	require.NoError(t, err)

	for _, name := range []string{"n1.30VCPU-126GB", "n1.32VCPU-126GB"} {
		reserved := offerings(t, p, name)
		require.Len(t, reserved, 1, name)
		require.Equal(t, 1, reserved[0].ReservationCapacity, name)
	}

	onDemand := offerings(t, p, "n2.14VCPU-63GB")
	require.Equal(t, 0, onDemand[0].ReservationCapacity)
}
