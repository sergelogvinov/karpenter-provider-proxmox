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

package proxmox_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/bootstrap"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/launchrule"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	region = "region-1"
	zone   = "pve-1"

	templateID = 100

	// unknownHugepages stands for a template whose status.template is unset, so its hugepages are not known; its
	// VM template has none.
	unknownHugepages = "unknown"

	instanceTypesFile = `[
  {"name": "n1", "numa": 1, "capacity": {"ephemeral-storage": "30Gi", "pods": "110"}},
  {"name": "c1.2VCPU-4GB", "capacity": {"cpu": "2", "memory": "4Gi", "ephemeral-storage": "30Gi", "pods": "64"}},
  {"name": "e2.2VCPU-4GB", "cpushare": 2, "capacity": {"cpu": "2", "memory": "4Gi", "ephemeral-storage": "30Gi", "pods": "64"}}
]`
)

// The kinds of instance type of instanceTypesFile: its NUMA types, its static type and its CPU share type.
const (
	numaKind     = "NUMA"
	staticKind   = "static"
	cpuShareKind = "CPU share"
)

func kindOf(name string) string {
	switch {
	case strings.HasPrefix(name, "n1."):
		return numaKind
	case strings.HasPrefix(name, "e2."):
		return cpuShareKind
	default:
		return staticKind
	}
}

// The zones of the test, by their node settings: 16 CPUs and 63GiB on one or two NUMA nodes, whose memory is a
// pool of 1GiB hugepages or none.
var zoneSettings = map[string]string{
	"no pool": `{"sockets": 1, "threads": 2, "nodes": {"0": {"cpus": "0-15", "memsize": 67645734912}}}`,
	"pool on one NUMA node": `{"sockets": 1, "threads": 2, "hugepagesize": 1073741824,
	  "nodes": {"0": {"cpus": "0-15", "memsize": 67645734912}}}`,
	"pool on two NUMA nodes": `{"sockets": 2, "threads": 2, "hugepagesize": 1073741824,
	  "nodes": {"0": {"cpus": "0-7", "memsize": 33822867456}, "1": {"cpus": "8-15", "memsize": 33822867456}}}`,
}

var nodePolicies = []string{"static", "simple", "simple-with-cpu-overcommit"}

// launchFixture is a cloud provider launching into a fake Proxmox zone, with real zone capacity, instance
// types, template check and launches behind it, and a NodePool "default" on a NodeClass "default" whose
// ProxmoxUnmanagedTemplate has one VM template with hugepages.
type launchFixture struct {
	ctx       context.Context //nolint:containedctx
	c         *proxmox.CloudProvider
	client    *proxmoxrest.Client
	capacity  *cloudcapacity.DefaultProvider
	types     *instancetype.DefaultProvider
	hugepages string
	unknown   bool
	root      bool
	policy    string
}

func newLaunchFixture(t *testing.T, settings, hugepages string, root bool, policy string) *launchFixture {
	t.Helper()

	unknown := hugepages == unknownHugepages
	if unknown {
		hugepages = ""
	}

	dir := t.TempDir()

	settingsFile := filepath.Join(dir, "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, fmt.Appendf(nil, `{%q: {%q: %s}}`, region, zone, settings), 0o600))

	typesFile := filepath.Join(dir, "instancetypes.json")
	require.NoError(t, os.WriteFile(typesFile, []byte(instanceTypesFile), 0o600))

	ctx := options.ToContext(t.Context(), &options.Options{
		ProxmoxVMID:           1000,
		NodePolicy:            policy,
		CPUOvercommitRatio:    1,
		NodeSettingFilePath:   settingsFile,
		InstanceTypesFilePath: typesFile,
	})

	cl := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	node := cl.Node(zone).SetResources(16, int64(64<<30))

	disk := fmt.Sprintf("local-lvm:base-%d-disk-0", templateID)
	cloudInitDrive := fmt.Sprintf("local-lvm:vm-%d-cloudinit", templateID)

	node.AddStorage("local-lvm", "lvmthin",
		fakeapi.WithContentTypes("images"),
		fakeapi.WithCapacity(100<<30, 0, 100<<30),
		fakeapi.WithVolume(storage.Volume{VolID: disk, VMID: templateID}),
		fakeapi.WithVolume(storage.Volume{VolID: cloudInitDrive, VMID: templateID}))
	node.AddStorage("local", "dir", fakeapi.WithContentTypes("iso"), fakeapi.WithCapacity(100<<30, 0, 100<<30))

	vmTemplate := &qemu.Config{
		Name:     "talos",
		Template: new(true),
		Sockets:  new(1),
		SCSI:     map[int]qemu.Drive{0: {File: disk, Size: "2G"}},
		IDE:      map[int]qemu.Drive{0: {File: cloudInitDrive, Media: "cdrom"}},
		Net:      map[int]qemu.Net{0: {Model: "virtio", MACAddr: "BC:24:11:00:00:01", Bridge: "vmbr0"}},
		IPConfig: map[int]qemu.IPConfig{0: {IPv4: "10.0.0.0/24", GatewayIPv4: "10.0.0.1"}},
		SMBios1:  &qemu.SMBios1{UUID: "00000000-0000-0000-0000-000000000001"},
	}
	if hugepages != "" {
		vmTemplate.Hugepages = new(hugepages)
		vmTemplate.NUMAEnabled = new(true)
	}

	node.AddVM(templateID, vmTemplate)

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{Region: region}})
	require.NoError(t, err)

	client := cl.Client(t)
	pool.Set(region, client)

	privileges := config.Privileges{region: root}

	capacity := cloudcapacity.NewProvider(ctx, pool, privileges)
	require.NoError(t, capacity.SyncNodeCapacity(ctx))
	require.NoError(t, capacity.SyncNodeStorageCapacity(ctx))

	templates := instancetemplate.NewDefaultProvider(ctx, pool, capacity)
	require.NoError(t, templates.SyncInstanceTemplates(ctx))

	clientset := kubefake.NewClientset(
		&corev1.ConfigMap{Name: "kube-root-ca.crt", Namespace: metav1.NamespaceSystem, Data: map[string]string{"ca.crt": "root-ca"}},
		&corev1.Secret{Name: "cloud-init", Namespace: metav1.NamespaceSystem},
	)
	clientset.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.34.0"}

	scheme := runtime.NewScheme()
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(gv, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{}, &karpv1.NodePool{}, &karpv1.NodePoolList{})
	metav1.AddToGroupVersion(scheme, gv)
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	template := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	if !unknown {
		template.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: hugepages}
	}

	size := resource.MustParse("10G")
	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: template.Name},
			BootDevice:          &v1alpha1.BlockDevice{Size: &size},
			MetadataOptions: &v1alpha1.MetadataOptions{
				Type:         "cdrom",
				TemplatesRef: &corev1.SecretReference{Name: "cloud-init", Namespace: metav1.NamespaceSystem},
			},
		},
	}
	nodeClass.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: templateID}})
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassTemplateReady)
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassMetadataOptionsReady)

	nodePool := &karpv1.NodePool{
		Name: "default",
		Spec: karpv1.NodePoolSpec{Template: karpv1.NodeClaimTemplate{Spec: karpv1.NodeClaimTemplateSpec{
			NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.SchemeGroupVersion.Group, Kind: "ProxmoxNodeClass", Name: nodeClass.Name},
		}}},
	}

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(template, nodeClass, nodePool).Build()

	types := instancetype.NewDefaultProvider(ctx, capacity, instancetype.Readers{API: kube, Cached: kube})
	require.NoError(t, types.UpdateInstanceTypes(ctx))
	require.NoError(t, types.UpdateInstanceTypeOfferings(ctx))

	instances, err := instance.NewProvider(ctx, clientset, bootstrap.NewProvider(ctx, clientset), pool, capacity,
		nodeipam.NewDefaultProvider(ctx, capacity), templates, types, privileges)
	require.NoError(t, err)

	return &launchFixture{
		ctx:       ctx,
		c:         proxmox.NewCloudProvider(ctx, kube, nil, instances, templates, types, capacity),
		client:    client,
		capacity:  capacity,
		types:     types,
		hugepages: hugepages,
		unknown:   unknown,
		root:      root,
		policy:    policy,
	}
}

// offeredTypes are the instance types GetInstanceTypes offers for the NodePool.
func (f *launchFixture) offeredTypes(t *testing.T) []string {
	t.Helper()

	nodePool := &karpv1.NodePool{}
	nodePool.Name = "default"
	nodePool.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{Kind: "ProxmoxNodeClass", Name: "default"}

	list, err := f.c.GetInstanceTypes(f.ctx, nodePool)
	require.NoError(t, err)

	offered := []string{}

	for _, it := range list {
		if len(it.Offerings.Available()) > 0 {
			offered = append(offered, it.Name)
		}
	}

	return offered
}

// launch runs Create for a NodeClaim of instance type name, and returns the config its VM is written with.
func (f *launchFixture) launch(t *testing.T, name string) *qemu.Config {
	t.Helper()

	nodeClaim := &karpv1.NodeClaim{
		Name: "node-1",
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Kind: "ProxmoxNodeClass", Name: "default"},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{
				Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{name},
			}},
		},
	}

	nc, err := f.c.Create(f.ctx, nodeClaim)
	require.NoError(t, err, "Create %s", name)

	vmid, _, err := provider.ParseProviderID(nc.Status.ProviderID)
	require.NoError(t, err)

	cfg, err := f.client.Nodes(zone).Qemu().Config(f.ctx, vmid, nil)
	require.NoError(t, err)

	return cfg
}

// requireLaunchedAsDecided checks that cfg, the config of a VM of instance type name, has the VM options of the
// type's launch decision, and that the VM can start: hugepages only from the zone's pool, with numa, its guest
// NUMA nodes on host NUMA nodes the zone has.
func (f *launchFixture) requireLaunchedAsDecided(t *testing.T, name string, cfg *qemu.Config) {
	t.Helper()

	pool := f.capacity.ZonePool(region, zone)

	template := launchrule.KnownTemplate(f.hugepages)
	if f.unknown {
		template = launchrule.UnknownTemplate("status.template is unset")
	}

	d := launchrule.Decide(f.types.Profile(name), template,
		launchrule.Zone{Pool: pool, Root: f.root})
	require.True(t, d.Offered, "%s is offered by its decision: %s", name, d.Drop.Message)

	switch d.Hugepages {
	case launchrule.SetHugepages:
		require.Equal(t, d.HugepagesValue, deref(cfg.Hugepages), "%s: hugepages set", name)
	case launchrule.KeepHugepages:
		require.Equal(t, f.hugepages, deref(cfg.Hugepages), "%s: the template's hugepages", name)
	case launchrule.DeleteHugepages:
		require.Nil(t, cfg.Hugepages, "%s: hugepages cleared", name)
	}

	if d.GuestNUMA == launchrule.GuestNUMAOneNode {
		require.Equal(t, 1, deref(cfg.Sockets), "%s: one socket", name)
		require.Len(t, cfg.NUMA, 1, "%s: one guest NUMA node", name)
		require.Equal(t, []string{strconv.Itoa(d.HostNode)}, cfg.NUMA[0].HostNodes, "%s", name)
		require.Equal(t, "bind", cfg.NUMA[0].Policy, "%s", name)
	}

	if cfg.Hugepages != nil {
		require.Positive(t, pool.PageSize, "%s: hugepages in a zone without a pool", name)
		require.Equal(t, strconv.FormatUint(pool.PageSize/zonecapacity.MiB, 10), *cfg.Hugepages, "%s: the pool's page size", name)
		require.True(t, deref(cfg.NUMAEnabled), "%s: Proxmox refuses hugepages without numa", name)
	}

	for _, n := range cfg.NUMA {
		for _, h := range n.HostNodes {
			host, err := strconv.Atoi(h)
			require.NoError(t, err)
			require.Less(t, host, pool.NUMANodes, "%s: guest NUMA node on a host NUMA node the zone has", name)
		}
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

// TestOfferedTypesLaunchAsDecided runs a template, a zone, root or not and each node policy from the offerings of
// GetInstanceTypes to the config each offered type's VM is written with.
func TestOfferedTypesLaunchAsDecided(t *testing.T) {
	t.Parallel()

	all := []string{numaKind, staticKind, cpuShareKind}
	nonNUMA := []string{staticKind, cpuShareKind}

	for _, tc := range []struct {
		zone      string
		hugepages string
		root      bool
		// offered are the kinds offered under the static node policy. The other policies offer no NUMA type,
		// and simple-with-cpu-overcommit no CPU share type.
		offered []string
	}{
		{zone: "no pool", hugepages: "", offered: all},
		{zone: "no pool", hugepages: "1024", offered: nil},
		{zone: "no pool", hugepages: "2", offered: nil},
		{zone: "pool on one NUMA node", hugepages: "", offered: nonNUMA},
		{zone: "pool on one NUMA node", hugepages: "1024", offered: all},
		{zone: "pool on one NUMA node", hugepages: "2", offered: nil},
		{zone: "pool on two NUMA nodes", hugepages: "", offered: nonNUMA},
		{zone: "pool on two NUMA nodes", hugepages: "1024", offered: []string{numaKind}},
		{zone: "pool on two NUMA nodes", hugepages: "2", offered: nil},
		{zone: "no pool", hugepages: "any", offered: nil},
		{zone: "pool on one NUMA node", hugepages: "any", offered: nil},
		{zone: "pool on two NUMA nodes", hugepages: "any", offered: nil},
		{zone: "no pool", hugepages: unknownHugepages, offered: all},
		{zone: "pool on one NUMA node", hugepages: unknownHugepages, offered: nil},
		{zone: "pool on two NUMA nodes", hugepages: unknownHugepages, offered: nil},

		{zone: "no pool", hugepages: "", root: true, offered: all},
		{zone: "no pool", hugepages: "1024", root: true, offered: all},
		{zone: "no pool", hugepages: "2", root: true, offered: all},
		{zone: "no pool", hugepages: "any", root: true, offered: all},
		{zone: "no pool", hugepages: unknownHugepages, root: true, offered: all},
		{zone: "pool on one NUMA node", hugepages: "", root: true, offered: all},
		{zone: "pool on one NUMA node", hugepages: "2", root: true, offered: all},
		{zone: "pool on one NUMA node", hugepages: "any", root: true, offered: all},
		{zone: "pool on one NUMA node", hugepages: unknownHugepages, root: true, offered: all},
		{zone: "pool on two NUMA nodes", hugepages: "", root: true, offered: []string{numaKind}},
		{zone: "pool on two NUMA nodes", hugepages: "1024", root: true, offered: []string{numaKind}},
		{zone: "pool on two NUMA nodes", hugepages: "2", root: true, offered: []string{numaKind}},
		{zone: "pool on two NUMA nodes", hugepages: "any", root: true, offered: []string{numaKind}},
		{zone: "pool on two NUMA nodes", hugepages: unknownHugepages, root: true, offered: []string{numaKind}},
	} {
		for _, policy := range nodePolicies {
			t.Run(fmt.Sprintf("%s/hugepages=%q/root=%t/%s", tc.zone, tc.hugepages, tc.root, policy), func(t *testing.T) {
				t.Parallel()

				want := slices.DeleteFunc(slices.Clone(tc.offered), func(kind string) bool {
					return (policy != "static" && kind == numaKind) || (policy == "simple-with-cpu-overcommit" && kind == cpuShareKind)
				})

				offered := newLaunchFixture(t, zoneSettings[tc.zone], tc.hugepages, tc.root, policy).offeredTypes(t)

				kinds := []string{}
				for _, name := range offered {
					kinds = append(kinds, kindOf(name))
				}

				require.ElementsMatch(t, want, kinds, "kinds offered: %v", offered)

				for _, name := range offered {
					// Each launch has a zone of its own, so that one launch does not take another's room.
					f := newLaunchFixture(t, zoneSettings[tc.zone], tc.hugepages, tc.root, policy)
					f.requireLaunchedAsDecided(t, name, f.launch(t, name))
				}
			})
		}
	}
}

// TestNothingOfferedIsInsufficientCapacity checks that Create launches nothing for a template that offers
// nothing in the zone.
func TestNothingOfferedIsInsufficientCapacity(t *testing.T) {
	t.Parallel()

	f := newLaunchFixture(t, zoneSettings["pool on one NUMA node"], "2", false, "static")

	_, err := f.c.Create(f.ctx, &karpv1.NodeClaim{
		Name: "node-1",
		Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Kind: "ProxmoxNodeClass", Name: "default"}},
	})
	require.True(t, cloudprovider.IsInsufficientCapacityError(err), "Create: %v", err)

	vms, err := f.client.Cluster().Resources().List(t.Context(), cluster.ListFilter{Type: cluster.ResourceTypeVM})
	require.NoError(t, err)
	require.Len(t, vms, 1, "only the VM template")
}
