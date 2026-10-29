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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// numaTemplates are the NUMA templates of docs/scheduler-numa.md §3, next to a static entry.
const numaTemplates = `[
  {"name": "n1", "numa": 1, "capacity": {"ephemeral-storage": "30Gi", "pods": "110"}},
  {"name": "n2", "numa": 2, "capacity": {"ephemeral-storage": "30Gi", "pods": "110"}},
  {"name": "n4", "numa": 4, "capacity": {"ephemeral-storage": "30Gi", "pods": "110"}},
  {"name": "c1.2VCPU-4GB", "capacity": {"cpu": "2", "memory": "4Gi", "ephemeral-storage": "30Gi", "pods": "64"}}
]`

// numaExampleHost is the host of docs/scheduler-numa.md §9: 2 sockets with one NUMA node each,
// 16 cores / 32 threads and 128GiB per node, CPUs 0 and 32 (SMT siblings) and 4GiB reserved.
const numaExampleHost = `{"sockets": 2, "threads": 2, "uncorecaches": 2,
  "nodes": {"0": {"cpus": "0-15,32-47", "memsize": 137438953472}, "1": {"cpus": "16-31,48-63", "memsize": 137438953472}},
  "reservedcpus": [0, 32], "reservedmemory": 4294967296}`

// smallHost is one NUMA node of 8 cores / 16 threads and 64GiB, 1GiB reserved: its whole node is 16 vCPU / 63GiB,
// the size of a half of numaExampleHost's node 1.
const smallHost = `{"sockets": 1, "threads": 2, "nodes": {"0": {"cpus": "0-15", "memsize": 68719476736}}, "reservedmemory": 1073741824}`

// numaExampleTypes are the NUMA types of docs/scheduler-numa.md §9.
var numaExampleTypes = []string{
	"n1.30VCPU-126GB", "n2.14VCPU-63GB", "n4.6VCPU-31GB",
	"n1.32VCPU-126GB", "n2.16VCPU-63GB", "n4.8VCPU-31GB",
}

// hosts is a node settings file of region with the given host per zone.
func hosts(zones map[string]string) string {
	entries := []string{}
	for name, host := range zones {
		entries = append(entries, fmt.Sprintf("%q: %s", name, host))
	}

	return fmt.Sprintf(`{%q: {%s}}`, region, strings.Join(entries, ", "))
}

// names are the instance types of p by name, with the families and zones of their offerings.
func names(t *testing.T, p *instancetype.DefaultProvider) []string {
	t.Helper()

	list, err := p.List(context.Background(), nil)
	require.NoError(t, err)

	return lo.Map(list, func(it *cloudprovider.InstanceType, _ int) string { return it.Name })
}

// zonesOf are the zones of the offerings of instance type name, and whether each is available.
func zonesOf(t *testing.T, p *instancetype.DefaultProvider, name string) map[string]bool {
	t.Helper()

	zones := map[string]bool{}
	for _, o := range offerings(t, p, name) {
		zones[o.Requirements.Get(corev1.LabelTopologyZone).Any()] = o.Available
	}

	return zones
}

// TestNUMATypesWorkedExample is docs/scheduler-numa.md §9: the templates yield exactly its six types,
// next to the static entry, each with its slot's exact capacity and the template's family.
func TestNUMATypesWorkedExample(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
	require.NoError(t, err)

	require.ElementsMatch(t, append([]string{"c1.2VCPU-4GB"}, numaExampleTypes...), names(t, p))

	it, err := p.Get(context.Background(), "n2.14VCPU-63GB")
	require.NoError(t, err)
	require.Equal(t, int64(14), it.Capacity.Cpu().Value())
	require.Equal(t, int64(63<<30), it.Capacity.Memory().Value())
	require.Equal(t, int64(110), it.Capacity.Pods().Value())
	require.Equal(t, int64(30<<30), it.Capacity.StorageEphemeral().Value())
	require.NotEmpty(t, it.Overhead.KubeReserved, "the overhead is computed from the slot's capacity")
	require.Equal(t, []string{"n2"}, it.Requirements.Get(v1alpha1.LabelInstanceFamily).Values())
	require.Equal(t, map[string]bool{zone: true}, zonesOf(t, p, "n2.14VCPU-63GB"))
}

// TestDefaultNUMATypes checks that the predefined list, used without an instance types file, has the n1 and n2
// NUMA templates: the §9 host offers its whole and half NUMA types, and no quarters.
func TestDefaultNUMATypes(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), "", nil, zone)
	require.NoError(t, err)

	numa := lo.Filter(names(t, p), func(name string, _ int) bool { return strings.HasPrefix(name, "n") })
	require.ElementsMatch(t, []string{"n1.30VCPU-126GB", "n2.14VCPU-63GB", "n1.32VCPU-126GB", "n2.16VCPU-63GB"}, numa)
	require.Contains(t, names(t, p), "c1.2VCPU-4GB")
	require.Equal(t, map[string]bool{zone: true}, zonesOf(t, p, "n1.32VCPU-126GB"))

	it, err := p.Get(context.Background(), "n2.16VCPU-63GB")
	require.NoError(t, err)
	require.NotEmpty(t, it.Overhead.KubeReserved, "the overhead is computed from the slot's capacity")
}

// TestNUMATypesMerge checks that identical slot shapes on different hosts are one type offered in both zones,
// and that a half of a big host never merges with a whole small host of the same size.
func TestNUMATypesMerge(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{"pve-1": numaExampleHost, "pve-2": numaExampleHost, "pve-3": smallHost}),
		numaTemplates, nil, "pve-1", "pve-2", "pve-3")
	require.NoError(t, err)

	require.ElementsMatch(t, append([]string{"c1.2VCPU-4GB", "n1.16VCPU-63GB", "n2.8VCPU-31GB", "n4.4VCPU-15GB"}, numaExampleTypes...), names(t, p))

	for _, name := range numaExampleTypes {
		require.Equal(t, map[string]bool{"pve-1": true, "pve-2": true}, zonesOf(t, p, name), name)
	}

	require.Equal(t, map[string]bool{"pve-3": true}, zonesOf(t, p, "n1.16VCPU-63GB"))
}

// TestNUMATypesCPUShareGroupsLeaveNoRoom is docs/scheduler-numa.md §10: with NUMA node 0 pinned, node 1's
// whole slot is unpinned and has its memory, but a CPU share group of 8 CPUs leaves no room to move off it.
// The type keeps its offering, unavailable, while a half still fits.
func TestNUMATypesCPUShareGroupsLeaveNoRoom(t *testing.T) {
	t.Parallel()

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), numaTemplates, func(cluster *fakeapi.Cluster) {
		cluster.Node(zone).AddVM(100, &qemu.Config{
			Cores: new(30), Memory: &qemu.Memory{Current: new(1024)}, Affinity: "1-15,33-47",
		}, fakeapi.WithStatus(qemu.VMStatusRunning))
		cluster.Node(zone).AddVM(101, &qemu.Config{
			Cores: new(8), Memory: &qemu.Memory{Current: new(1024)}, Description: "Karpenter managed instance, cpushare=4",
		}, fakeapi.WithStatus(qemu.VMStatusRunning))
	}, zone)
	require.NoError(t, err)

	require.Equal(t, map[string]bool{zone: false}, zonesOf(t, p, "n1.32VCPU-126GB"))
	require.Equal(t, map[string]bool{zone: true}, zonesOf(t, p, "n2.16VCPU-63GB"))
}

// TestNUMATypesSharedPlacement checks that zones under shared placement never offer NUMA types.
func TestNUMATypesSharedPlacement(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{"simple", "simple-with-cpu-overcommit"} {
		p, err := newProviderOn(t, policy, hosts(map[string]string{zone: numaExampleHost}), numaTemplates, nil, zone)
		require.NoError(t, err)

		require.Equal(t, []string{"c1.2VCPU-4GB"}, names(t, p), policy)
	}
}

func TestLoaderValidatesNUMA(t *testing.T) {
	t.Parallel()

	for _, fraction := range []int{1, 2, 4} {
		_, err := newProvider(t, "static", fmt.Sprintf(`[{"name": "n", "numa": %d}, {"name": "c1.1VCPU-2GB", "capacity": {"cpu": "1", "memory": "2Gi"}}]`, fraction))
		require.NoError(t, err, "numa %d", fraction)
	}

	for _, fraction := range []int{-1, 3, 5, 8} {
		_, err := newProvider(t, "static", fmt.Sprintf(`[{"name": "n", "numa": %d}]`, fraction))
		require.ErrorContains(t, err, "numa", "numa %d", fraction)
	}

	_, err := newProvider(t, "static", `[{"name": "n", "numa": 2, "cpushare": 2}]`)
	require.ErrorContains(t, err, "NUMA template", "a NUMA template is not a CPU share type")
}

// TestLoaderRejectsDuplicateNUMATemplateNames checks that two NUMA templates cannot share a name,
// whatever their slot fractions, since their types would collide.
func TestLoaderRejectsDuplicateNUMATemplateNames(t *testing.T) {
	t.Parallel()

	for _, second := range []int{1, 2} {
		_, err := newProvider(t, "static", fmt.Sprintf(`[{"name": "n", "numa": 1}, {"name": "n", "numa": %d}]`, second))
		require.ErrorContains(t, err, "duplicate", "numa 1 and %d", second)
	}
}

// TestNUMATypeNameTakenByStaticEntry checks that a NUMA type whose name is already a static entry is skipped:
// the static entry stays as configured and the name is listed once.
func TestNUMATypeNameTakenByStaticEntry(t *testing.T) {
	t.Parallel()

	file := `[
  {"name": "n2", "numa": 2, "capacity": {"pods": "110"}},
  {"name": "n2.14VCPU-63GB", "capacity": {"cpu": "2", "memory": "4Gi", "pods": "64"}}
]`

	p, err := newProviderOn(t, "static", hosts(map[string]string{zone: numaExampleHost}), file, nil, zone)
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"n2.14VCPU-63GB", "n2.16VCPU-63GB"}, names(t, p))

	it, err := p.Get(context.Background(), "n2.14VCPU-63GB")
	require.NoError(t, err)
	require.Equal(t, int64(2), it.Capacity.Cpu().Value(), "the static entry wins")
	require.Zero(t, p.SlotFraction("n2.14VCPU-63GB"), "the static entry launches without a NUMA slot")
	require.Equal(t, 2, p.SlotFraction("n2.16VCPU-63GB"))
}
