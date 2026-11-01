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

package instance_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/bootstrap"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/cpuset"

	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const (
	testRegion = "region-1"
	// firstVMID is where launches start numbering VMs.
	firstVMID = 1000
)

// launchSetup describes the cluster a launch test runs against. The zero
// value is one zone that fits exactly one launch of the instance type.
type launchSetup struct {
	// zones are the zones, each with its own VM template; default "pve1".
	zones []string
	// noISOStorage lists zones without an ISO-capable storage.
	noISOStorage []string
	// subnet is the template's ipconfig0 subnet; default "10.0.0.0/24".
	subnet string
	// cpus and memory size the instance type; by default it fills a zone.
	cpus   int
	memory string
	// metadataType is the node class's metadata delivery; default "cdrom".
	metadataType string
	// manualTasks leaves every Proxmox task running until the test
	// completes it.
	manualTasks bool
	// nodePolicy is the node CPU policy; default "simple".
	nodePolicy string
	// cpuShare makes the instance type a CPU share type of that many seats, family e<N>.
	cpuShare int
	// numa makes the instance type the NUMA type of a NUMA template of that slot fraction, family n<N>,
	// on zones of two NUMA nodes, CPUs 0-3 and 4-7, of 8GiB each.
	numa int
	// hugePages gives each zone a hugepage pool of 2MiB pages on its NUMA nodes.
	hugePages bool
	// nonRoot makes the region's credentials something other than root@pam, such as an API token.
	nonRoot bool
}

// numaZoneSettings is the node settings file of launchSetup.numa: two NUMA nodes of 4 CPUs and 8GiB.
const numaZoneSettings = `{%q: {"*": {"sockets": 2, "hugepagesize": %d, "nodes": {
	"0": {"cpus": "0-3", "memsize": 8589934592},
	"1": {"cpus": "4-7", "memsize": 8589934592}
}}}}`

// launchFixture is an instance provider launching into a fake Proxmox
// cluster, with real zone capacity, IP pool and template sync behind it.
type launchFixture struct {
	t            *testing.T
	ctx          context.Context //nolint:containedctx
	cluster      *fakeapi.Cluster
	client       *proxmoxrest.Client
	pool         *pxpool.ProxmoxPool
	provider     *instance.DefaultProvider
	capacity     *cloudcapacity.DefaultProvider
	types        *instancetype.DefaultProvider
	options      *options.Options
	setup        launchSetup
	nodeClass    *v1alpha1.ProxmoxNodeClass
	instanceType *cloudprovider.InstanceType
}

func newLaunchFixture(t *testing.T, setup launchSetup) *launchFixture {
	t.Helper()

	if len(setup.zones) == 0 {
		setup.zones = []string{"pve1"}
	}

	if setup.subnet == "" {
		setup.subnet = "10.0.0.0/24"
	}

	if setup.cpus == 0 {
		// A zone has 4 CPUs and 8GiB, minus the 1GiB reserved for the host.
		setup.cpus, setup.memory = 4, "6Gi"
	}

	if setup.metadataType == "" {
		setup.metadataType = "cdrom"
	}

	opts := []fakeapi.ClusterOption{fakeapi.WithNodes(setup.zones...), fakeapi.WithPool("karpenter")}
	if setup.manualTasks {
		opts = append(opts, fakeapi.WithManualTasks())
	}

	cl := fakeapi.NewCluster(t, opts...)

	placements := []string{}

	for i, zone := range setup.zones {
		templateID := 100 * (i + 1)
		disk := fmt.Sprintf("local-lvm:base-%d-disk-0", templateID)
		cloudInitDrive := fmt.Sprintf("local-lvm:vm-%d-cloudinit", templateID)

		node := cl.Node(zone).SetCPUModel("Intel(R) Xeon(R) CPU E5-2690 v4")
		node.AddStorage("local-lvm", "lvmthin",
			fakeapi.WithContentTypes("images"),
			fakeapi.WithCapacity(100<<30, 0, 100<<30),
			fakeapi.WithVolume(storage.Volume{VolID: disk, VMID: templateID}),
			fakeapi.WithVolume(storage.Volume{VolID: cloudInitDrive, VMID: templateID}))

		if !slices.Contains(setup.noISOStorage, zone) {
			node.AddStorage("local", "dir", fakeapi.WithContentTypes("iso"), fakeapi.WithCapacity(100<<30, 0, 100<<30))
		}

		template := true
		node.AddVM(templateID, &qemu.Config{
			Name:     "talos",
			Template: &template,
			SCSI:     map[int]qemu.Drive{0: {File: disk, Size: "2G"}},
			IDE:      map[int]qemu.Drive{0: {File: cloudInitDrive, Media: "cdrom"}},
			Net:      map[int]qemu.Net{0: {Model: "virtio", MACAddr: "BC:24:11:00:00:01", Bridge: "vmbr0"}},
			IPConfig: map[int]qemu.IPConfig{0: {IPv4: setup.subnet, GatewayIPv4: "10.0.0.1"}},
			SMBios1:  &qemu.SMBios1{UUID: "00000000-0000-0000-0000-000000000001"},
		})

		placements = append(placements, fmt.Sprintf("%s/%s/%d", testRegion, zone, templateID))
	}

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{Region: testRegion}})
	require.NoError(t, err)

	client := cl.Client(t)
	pool.Set(testRegion, client)

	family := "c1"
	if setup.cpuShare > 0 {
		family = fmt.Sprintf("e%d", setup.cpuShare)
	}

	instanceTypeName := fmt.Sprintf("%s.%dVCPU-%s", family, setup.cpus, setup.memory)

	typesFile := filepath.Join(t.TempDir(), "instancetypes.json")
	require.NoError(t, os.WriteFile(typesFile, fmt.Appendf(nil,
		`[{"name": %q, "cpushare": %d, "capacity": {"cpu": "%d", "memory": %q}}]`,
		instanceTypeName, setup.cpuShare, setup.cpus, setup.memory), 0o600))

	operatorOptions := &options.Options{ProxmoxVMID: firstVMID, NodePolicy: setup.nodePolicy, InstanceTypesFilePath: typesFile}

	if setup.numa > 0 {
		var pageSize int
		if setup.hugePages {
			pageSize = 2 << 20
		}

		require.NoError(t, os.WriteFile(typesFile, fmt.Appendf(nil,
			`[{"name": "n%d", "numa": %d, "capacity": {"pods": "110"}}]`, setup.numa, setup.numa), 0o600))

		operatorOptions.NodeSettingFilePath = filepath.Join(t.TempDir(), "nodes.json")
		require.NoError(t, os.WriteFile(operatorOptions.NodeSettingFilePath, fmt.Appendf(nil, numaZoneSettings, testRegion, pageSize), 0o600))
	}

	ctx := options.ToContext(t.Context(), operatorOptions)

	privileges := config.Privileges{testRegion: !setup.nonRoot}

	capacity := cloudcapacity.NewProvider(ctx, pool, privileges)
	require.NoError(t, capacity.SyncNodeCapacity(ctx))
	require.NoError(t, capacity.SyncNodeStorageCapacity(ctx))

	templates := instancetemplate.NewDefaultProvider(ctx, pool, capacity)
	require.NoError(t, templates.SyncInstanceTemplates(ctx))

	kube := kubefake.NewClientset(
		&corev1.ConfigMap{
			Name:      "kube-root-ca.crt",
			Namespace: metav1.NamespaceSystem,
			Data:      map[string]string{"ca.crt": "root-ca"},
		},
		&corev1.Secret{
			Name:      "cloud-init",
			Namespace: metav1.NamespaceSystem,
		},
	)
	kube.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.34.0"}

	// The node class's template has the hugepages of the zones' pool, so that without root it offers the NUMA type.
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	vmTemplate := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	vmTemplate.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: lo.Ternary(setup.hugePages, "2", "")}
	templateReader := crfake.NewClientBuilder().WithScheme(scheme).WithObjects(vmTemplate).Build()

	instanceTypes := instancetype.NewDefaultProvider(ctx, capacity,
		instancetype.Readers{API: crfake.NewClientBuilder().Build(), Cached: templateReader})
	require.NoError(t, instanceTypes.UpdateInstanceTypes(ctx))

	p, err := instance.NewProvider(ctx, kube, bootstrap.NewProvider(ctx, kube), pool, capacity,
		nodeipam.NewDefaultProvider(ctx, capacity), templates, instanceTypes, privileges)
	require.NoError(t, err)

	size := resource.MustParse("10G")

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: "talos"},
			BootDevice:          &v1alpha1.BlockDevice{Size: &size},
			Tags:                []string{"karpenter", "web"},
			SecurityGroups:      []v1alpha1.SecurityGroups{{Name: "sg-web", Interface: "net0"}, {Name: "sg-ssh"}},
			ResourcePool:        "karpenter",
			MetadataOptions: &v1alpha1.MetadataOptions{
				Type:         setup.metadataType,
				TemplatesRef: &corev1.SecretReference{Name: "cloud-init", Namespace: metav1.NamespaceSystem},
			},
		},
		Status: v1alpha1.ProxmoxNodeClassStatus{SelectedZones: placements},
	}

	offerings := cloudprovider.Offerings{}
	for _, zone := range setup.zones {
		offerings = append(offerings, &cloudprovider.Offering{
			Available: true,
			Price:     1,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, testRegion),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
			),
		})
	}

	instanceType := &cloudprovider.InstanceType{
		Name: instanceTypeName,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(int64(setup.cpus), resource.DecimalSI),
			corev1.ResourceMemory: resource.MustParse(setup.memory),
		},
		Offerings: offerings,
	}

	if setup.numa > 0 {
		// The NUMA type is generated from the zones' slot shapes, with its offerings.
		require.NoError(t, instanceTypes.UpdateInstanceTypeOfferings(ctx))

		types, err := instanceTypes.List(ctx, nodeClass)
		require.NoError(t, err)
		require.Len(t, types, 1, "both NUMA nodes have the same slot shape")

		instanceType = types[0]
	}

	return &launchFixture{
		t:            t,
		ctx:          ctx,
		cluster:      cl,
		client:       client,
		pool:         pool,
		provider:     p,
		capacity:     capacity,
		types:        instanceTypes,
		options:      operatorOptions,
		setup:        setup,
		nodeClass:    nodeClass,
		instanceType: instanceType,
	}
}

func (f *launchFixture) nodeClaim(name string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		Name: name,
		Spec: karpv1.NodeClaimSpec{
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: f.setup.zones},
			},
		},
	}
}

func (f *launchFixture) create(ctx context.Context, name string) (*corev1.Node, error) {
	return f.provider.Create(ctx, f.nodeClaim(name), f.nodeClass, []*cloudprovider.InstanceType{f.instanceType})
}

// guests returns the VMs launched so far, by zone.
func (f *launchFixture) guests() map[string][]int {
	f.t.Helper()

	vms, err := f.client.Cluster().Resources().List(f.ctx, cluster.ListFilter{Type: cluster.ResourceTypeVM})
	require.NoError(f.t, err)

	guests := map[string][]int{}

	for _, vm := range vms {
		if vm.Template == 0 {
			guests[vm.Node] = append(guests[vm.Node], vm.VMID)
		}
	}

	return guests
}

// isos returns the ISO volumes on every zone's ISO storage.
func (f *launchFixture) isos() []string {
	f.t.Helper()

	isos := []string{}

	for _, zone := range f.setup.zones {
		if slices.Contains(f.setup.noISOStorage, zone) {
			continue
		}

		vols, err := f.client.Nodes(zone).Storage().Content("local").List(f.ctx, nil)
		require.NoError(f.t, err)

		for _, v := range vols {
			isos = append(isos, v.VolID)
		}
	}

	return isos
}

// firstAddress is the address an empty IP pool allocates first from the
// template's subnet.
func (f *launchFixture) firstAddress() string {
	f.t.Helper()

	ipam := nodeipam.NewDefaultProvider(f.ctx, f.capacity)
	require.NoError(f.t, ipam.RegisterSubnet(f.setup.subnet))

	ip, err := ipam.AllocateIP(f.setup.subnet)
	require.NoError(f.t, err)

	_, bits, _ := strings.Cut(f.setup.subnet, "/")

	return ip.String() + "/" + bits
}

// vmConfig returns the config of the VM behind node.
func (f *launchFixture) vmConfig(node *corev1.Node) *qemu.Config {
	f.t.Helper()

	vmid, _, err := provider.ParseProviderID(node.Spec.ProviderID)
	require.NoError(f.t, err)

	cfg, err := f.client.Nodes(node.Labels[corev1.LabelTopologyZone]).Qemu().Config(f.ctx, vmid, nil)
	require.NoError(f.t, err)

	return cfg
}

// requireNothingLeftOver asserts a failed launch left no VM and no ISO,
// then that a fresh launch fits into the zone's capacity again and gets
// the IP pool's first address back.
func (f *launchFixture) requireNothingLeftOver() {
	f.t.Helper()

	assert.Empty(f.t, f.guests(), "no VM is left over")
	assert.Empty(f.t, f.isos(), "no cloud-init ISO is left over")

	f.cluster.ClearFaults()

	node, err := f.create(f.ctx, "node-retry")
	require.NoError(f.t, err, "the zone capacity is free again")

	assert.Equal(f.t, f.firstAddress(), f.vmConfig(node).IPConfig[0].IPv4, "the address is back in the IP pool")
}

func TestCreate(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	assert.Equal(t, "node-1", node.Name)
	assert.Equal(t, provider.GetProviderID(testRegion, firstVMID), node.Spec.ProviderID)
	assert.Equal(t, map[string]string{
		corev1.LabelTopologyRegion:     testRegion,
		corev1.LabelTopologyZone:       "pve1",
		corev1.LabelInstanceTypeStable: f.instanceType.Name,
		karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
		v1alpha1.LabelInstanceFamily:   "c1",
		v1alpha1.LabelInstanceCPUType:  "",
		v1alpha1.LabelInstanceImageID:  node.Labels[v1alpha1.LabelInstanceImageID],
	}, node.Labels)
	assert.NotEmpty(t, node.Labels[v1alpha1.LabelInstanceImageID])
	assert.Equal(t, []corev1.Taint{karpv1.UnregisteredNoExecuteTaint}, node.Spec.Taints)

	cfg := f.vmConfig(node)

	assert.Equal(t, "node-1", cfg.Name)
	assert.Equal(t, 4, *cfg.Cores)
	assert.Equal(t, 6*1024, *cfg.Memory.Current)
	assert.Equal(t, []string{"karpenter", "web"}, []string(*cfg.Tags))
	assert.Equal(t, f.firstAddress(), cfg.IPConfig[0].IPv4)
	assert.Equal(t, fmt.Sprintf("local:iso/vm-%d-custom-cloudinit.iso", firstVMID), cfg.IDE[2].File)

	status, err := f.client.Nodes("pve1").Qemu().Status(f.ctx, firstVMID)
	require.NoError(t, err)
	assert.Equal(t, qemu.VMStatusRunning, status.Status)

	fw := f.client.Nodes("pve1").Qemu().Firewall()

	fwOptions, err := fw.Options(firstVMID).Get(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, fwOptions.Enable)

	rules, err := fw.Rules(firstVMID).List(f.ctx)
	require.NoError(t, err)

	groups := make([]string, len(rules))
	for i, r := range rules {
		groups[i] = r.Action + "@" + r.IFace
	}

	assert.Equal(t, []string{"sg-web@net0", "sg-ssh@"}, groups, "security groups apply in node class order")

	pool, err := f.client.Pools().Get(f.ctx, "karpenter")
	require.NoError(t, err)
	require.Len(t, pool.Members, 1)
	assert.Equal(t, firstVMID, pool.Members[0].VMID)
}

func TestCreateWritesGrantTokensIntoDescription(t *testing.T) {
	for _, tc := range []struct {
		policy string
		pinned bool
	}{
		{policy: "simple"},
		{policy: "static", pinned: true},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			f := newLaunchFixture(t, launchSetup{nodePolicy: tc.policy, cpus: 2, memory: "2Gi"})

			node, err := f.create(f.ctx, "node-1")
			require.NoError(t, err)

			cfg := f.vmConfig(node)
			if !tc.pinned {
				assert.Empty(t, cfg.Affinity)
				assert.NotContains(t, cfg.Description, "affinity=")

				return
			}

			require.NotEmpty(t, cfg.Affinity, "a pinned VM gets its own host CPUs")
			assert.True(t, strings.HasSuffix(cfg.Description, ", affinity="+cfg.Affinity), "description %q carries the affinity token", cfg.Description)
		})
	}
}

// TestCreateCPUShare checks that a CPU share type launches with its token, no affinity and no NUMA
// binding, and that a second node of the class takes the open seat of the first one's group.
func TestCreateCPUShare(t *testing.T) {
	for _, policy := range []string{"simple", "static"} {
		t.Run(policy, func(t *testing.T) {
			// The zone has 4 CPUs and 7GiB: two 4-vCPU nodes fit only by sharing one group.
			f := newLaunchFixture(t, launchSetup{nodePolicy: policy, cpus: 4, memory: "2Gi", cpuShare: 2})

			for _, name := range []string{"node-1", "node-2"} {
				node, err := f.create(f.ctx, name)
				require.NoError(t, err, name)
				assert.Equal(t, "e2", node.Labels[v1alpha1.LabelInstanceFamily])

				cfg := f.vmConfig(node)
				assert.Equal(t, 4, *cfg.Cores)
				assert.Empty(t, cfg.Affinity, "no affinity")
				assert.Empty(t, cfg.NUMA, "no NUMA binding")
				assert.True(t, strings.HasSuffix(cfg.Description, ", cpushare=2"), "description %q carries the cpushare token", cfg.Description)
				assert.NotContains(t, cfg.Description, "affinity=")
			}

			_, err := f.create(f.ctx, "node-3")
			require.Error(t, err, "a third node needs a second group")
		})
	}
}

// TestCreateNUMASlot checks that NUMA types launch into aligned NUMA slots, lowest NUMA node and slot first:
// pinned 1:1 to the slot's CPUs, with one guest NUMA node bound to the slot's host node and its memory.
// Once every slot is taken, the next launch fails for lack of capacity instead of spanning NUMA nodes.
func TestCreateNUMASlot(t *testing.T) {
	for _, tc := range []struct {
		numa     int
		name     string
		memory   int
		affinity []string
	}{
		// Each NUMA node keeps 7.5GiB after its half of the 1GiB host reserve.
		{numa: 1, name: "n1.4VCPU-7GB", memory: 7 * 1024, affinity: []string{"0-3", "4-7"}},
		// Two halves fill a NUMA node.
		{numa: 2, name: "n2.2VCPU-3GB", memory: 3 * 1024, affinity: []string{"0-1", "2-3", "4-5", "6-7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: tc.numa})
			require.Equal(t, tc.name, f.instanceType.Name)

			for i, affinity := range tc.affinity {
				node, err := f.create(f.ctx, fmt.Sprintf("node-%d", i))
				require.NoError(t, err)
				assert.Equal(t, fmt.Sprintf("n%d", tc.numa), node.Labels[v1alpha1.LabelInstanceFamily])

				hostNode := "0"
				if i >= len(tc.affinity)/2 {
					hostNode = "1"
				}

				cfg := f.vmConfig(node)
				assert.Equal(t, affinity, cfg.Affinity)
				assert.True(t, strings.HasSuffix(cfg.Description, ", affinity="+affinity), "description %q carries the affinity token", cfg.Description)
				require.NotNil(t, cfg.NUMAEnabled)
				assert.True(t, *cfg.NUMAEnabled)
				assert.Equal(t, map[int]qemu.NUMA{0: {
					CPUIDs:    []string{fmt.Sprintf("0-%d", 4/tc.numa-1)},
					HostNodes: []string{hostNode},
					Memory:    new(tc.memory),
					Policy:    "bind",
				}}, cfg.NUMA)
				assert.Nil(t, cfg.Hugepages, "the zone has no hugepage pool")
			}

			// The offering was available when the type was listed, but the last slot is gone.
			_, err := f.create(f.ctx, "node-last")
			require.Error(t, err)
			assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "no free slot is a lack of capacity: %v", err)
			assert.Len(t, f.guests()["pve1"], len(tc.affinity), "no VM spans NUMA nodes")
		})
	}
}

// TestCreateNUMASlotHugePages checks that a zone with a hugepage pool launches NUMA VMs with hugepages
// of the pool's page size, sized on the pool.
func TestCreateNUMASlotHugePages(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 1, hugePages: true})
	require.Equal(t, "n1.4VCPU-8GB", f.instanceType.Name)

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	cfg := f.vmConfig(node)
	require.NotNil(t, cfg.Hugepages)
	assert.Equal(t, "2", *cfg.Hugepages)
	assert.Equal(t, new(8*1024), cfg.NUMA[0].Memory)
}

// TestCreateNUMASlotResyncs checks that a capacity sync that never saw the launch tracks a NUMA VM
// as a pinned VM covering its slot.
func TestCreateNUMASlotResyncs(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 2})

	_, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	synced := cloudcapacity.NewProvider(f.ctx, f.pool, nil)
	require.NoError(t, synced.SyncNodeCapacity(f.ctx))

	assert.Equal(t, f.capacity.Slots(testRegion, "pve1"), synced.Slots(testRegion, "pve1"))
	// The VM covers the first half of NUMA node 0, so the whole node and two of its quarters are taken too.
	assert.Equal(t, []zonecapacity.SlotOffer{
		{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 4, Memory: 7 << 30}, Free: 1},
		{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 2, Memory: 3 << 30}, Free: 3},
		{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 1, Memory: 1 << 30}, Free: 6},
	}, synced.Slots(testRegion, "pve1"))
}

// TestCreateNUMASlotAligned checks that a NUMA VM takes a whole free slot, not the free CPUs ordinary
// pinned placement would pick around a VM already pinned inside a slot.
func TestCreateNUMASlotAligned(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 2})

	f.cluster.Node("pve1").AddVM(500, &qemu.Config{Name: "other", Cores: new(1), Affinity: "0", Memory: &qemu.Memory{Current: new(1024)}},
		fakeapi.WithStatus(qemu.VMStatusRunning))
	require.NoError(t, f.capacity.SyncNodeCapacity(f.ctx))

	for _, affinity := range []string{"2-3", "4-5", "6-7"} {
		node, err := f.create(f.ctx, "node-"+affinity)
		require.NoError(t, err)
		assert.Equal(t, affinity, f.vmConfig(node).Affinity)
	}

	_, err := f.create(f.ctx, "node-last")
	require.Error(t, err)
	assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "CPU 1 is free, but no slot is: %v", err)
}

// TestCreateNUMASlotTypeVanished checks that a NUMA type launches into a NUMA slot even when it is gone
// from the generated instance types between listing and launch, here by a reload of the instance types
// file before the next offerings refresh: the slot comes from the type being launched, not the current list.
func TestCreateNUMASlotTypeVanished(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 1, hugePages: true})

	require.NoError(t, f.types.UpdateInstanceTypes(f.ctx))

	types, err := f.types.List(f.ctx, f.nodeClass)
	require.NoError(t, err)
	require.Empty(t, types, "the NUMA type is not generated until the next offerings refresh")

	for _, hostNode := range []string{"0", "1"} {
		node, err := f.create(f.ctx, "node-"+hostNode)
		require.NoError(t, err)

		cfg := f.vmConfig(node)
		require.NotNil(t, cfg.Hugepages, "a VM in a NUMA slot gets hugepages")
		assert.Equal(t, "2", *cfg.Hugepages)
		require.Len(t, cfg.NUMA, 1)
		assert.Equal(t, []string{hostNode}, cfg.NUMA[0].HostNodes)
	}

	_, err = f.create(f.ctx, "node-last")
	require.Error(t, err)
	assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "no free slot is a lack of capacity: %v", err)
}

// TestCreateNUMASlotZoneLeftStatic checks that a NUMA type listed while its zone was static fails for lack
// of capacity once the zone has moved to simple placement, which offers no NUMA slots.
func TestCreateNUMASlotZoneLeftStatic(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 2})

	f.options.NodePolicy = "simple"
	require.NoError(t, f.capacity.SyncNodeCapacity(f.ctx))

	_, err := f.create(f.ctx, "node-1")
	require.Error(t, err)
	assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "a zone without NUMA slots has no room: %v", err)
	assert.Empty(t, f.guests()["pve1"])
}

// TestCreateWithoutRootPinned checks that without root a pinned VM gets no affinity option: its host CPUs
// travel only in the description token, which a capacity sync that never saw the launch reads back, so a
// restart neither counts the VM twice nor loses its CPUs.
func TestCreateWithoutRootPinned(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", cpus: 2, memory: "2Gi", nonRoot: true})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	cfg := f.vmConfig(node)
	assert.Empty(t, cfg.Affinity, "only root@pam may set affinity")

	_, token, found := strings.Cut(cfg.Description, ", affinity=")
	require.True(t, found, "description %q carries the affinity token", cfg.Description)

	pinned, err := cpuset.Parse(token)
	require.NoError(t, err)
	require.Equal(t, 2, pinned.Size())

	synced := cloudcapacity.NewProvider(f.ctx, f.pool, nil)
	require.NoError(t, synced.SyncNodeCapacity(f.ctx))

	// The zone has 4 CPUs: a second 2-vCPU VM takes the two the first left, and then none is left.
	for name, p := range map[string]*cloudcapacity.DefaultProvider{"running": f.capacity, "restarted": synced} {
		reserved, err := p.Reserve(f.ctx, testRegion, "pve1", resources.VMResources{ID: 2000, CPUs: 2, Memory: 1 << 30})
		require.NoError(t, err, name)
		assert.True(t, reserved.CPUSet.Intersection(pinned).IsEmpty(), "%s: the first VM still holds %s, got %s", name, pinned, reserved.CPUSet)

		_, err = p.Reserve(f.ctx, testRegion, "pve1", resources.VMResources{ID: 2001, CPUs: 1, Memory: 1 << 30})
		require.Error(t, err, "%s: no CPU is left", name)
	}
}

// TestCreateWithoutRootNUMASlot checks that without root a NUMA VM gets neither affinity nor hugepages,
// which come from its template, but still one guest NUMA node bound to its slot's host node; a capacity
// sync that never saw the launch tracks it as covering its slot.
func TestCreateWithoutRootNUMASlot(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{nodePolicy: "static", numa: 2, hugePages: true, nonRoot: true})

	for _, tc := range []struct{ affinity, hostNode string }{{"0-1", "0"}, {"2-3", "0"}, {"4-5", "1"}} {
		node, err := f.create(f.ctx, "node-"+tc.affinity)
		require.NoError(t, err)

		cfg := f.vmConfig(node)
		assert.Empty(t, cfg.Affinity, "only root@pam may set affinity")
		assert.Nil(t, cfg.Hugepages, "only root@pam may set hugepages")
		assert.True(t, strings.HasSuffix(cfg.Description, ", affinity="+tc.affinity), "description %q carries the affinity token", cfg.Description)
		require.NotNil(t, cfg.NUMAEnabled)
		assert.True(t, *cfg.NUMAEnabled)
		require.Len(t, cfg.NUMA, 1)
		assert.Equal(t, []string{tc.hostNode}, cfg.NUMA[0].HostNodes)
		assert.Equal(t, "bind", cfg.NUMA[0].Policy)
	}

	synced := cloudcapacity.NewProvider(f.ctx, f.pool, nil)
	require.NoError(t, synced.SyncNodeCapacity(f.ctx))

	assert.Equal(t, f.capacity.Slots(testRegion, "pve1"), synced.Slots(testRegion, "pve1"))
	assert.Equal(t, []zonecapacity.SlotOffer{
		{Shape: zonecapacity.SlotShape{Fraction: 1, CPUs: 4, Memory: 8 << 30}, Free: 0},
		{Shape: zonecapacity.SlotShape{Fraction: 2, CPUs: 2, Memory: 4 << 30}, Free: 1},
		{Shape: zonecapacity.SlotShape{Fraction: 4, CPUs: 1, Memory: 2 << 30}, Free: 2},
	}, synced.Slots(testRegion, "pve1"), "three of the four halves are taken")
}

func TestCreateWithoutCloudInitISO(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{metadataType: "none"})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	cfg := f.vmConfig(node)
	_, attached := cfg.IDE[2]

	assert.False(t, attached, "no ISO is attached")
	assert.Empty(t, f.isos(), "no ISO is uploaded")
	assert.Equal(t, f.firstAddress(), cfg.IPConfig[0].IPv4)
}

// TestCreateRollsBack fails each launch step that has an undo, and checks
// the launch rollback leaves nothing behind.
func TestCreateRollsBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route string
		opts  []fakeapi.FaultOption
	}{
		{name: "next id", route: "GET /cluster/nextid"},
		{name: "clone", route: "POST /nodes/{node}/qemu/{vmid}/clone"},
		{name: "clone task", route: "POST /nodes/{node}/qemu/{vmid}/clone", opts: []fakeapi.FaultOption{fakeapi.AsFailedTask()}},
		{name: "resize", route: "PUT /nodes/{node}/qemu/{vmid}/resize"},
		{name: "configure", route: "PUT /nodes/{node}/qemu/{vmid}/config"},
		{name: "cloud-init drive", route: "PUT /nodes/{node}/qemu/{vmid}/cloudinit"},
		{name: "firewall options", route: "PUT /nodes/{node}/qemu/{vmid}/firewall/options"},
		{name: "firewall rules", route: "POST /nodes/{node}/qemu/{vmid}/firewall/rules"},
		{name: "ISO upload", route: "POST /nodes/{node}/storage/{storage}/upload"},
		{name: "ISO upload task", route: "POST /nodes/{node}/storage/{storage}/upload", opts: []fakeapi.FaultOption{fakeapi.AsFailedTask()}},
		{name: "ISO attach", route: "POST /nodes/{node}/qemu/{vmid}/config"},
		{name: "start", route: "POST /nodes/{node}/qemu/{vmid}/status/{action}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newLaunchFixture(t, launchSetup{})
			f.cluster.FailRequests(tt.route, tt.opts...)

			_, err := f.create(f.ctx, "node-1")
			require.Error(t, err)
			assert.False(t, cloudprovider.IsInsufficientCapacityError(err), "a failed launch is not a lack of capacity: %v", err)

			f.requireNothingLeftOver()
		})
	}
}

func TestCreateRefusesFullZone(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{})

	_, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	_, err = f.create(f.ctx, "node-2")
	require.Error(t, err)
	assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "no zone has room: %v", err)

	assert.Equal(t, map[string][]int{"pve1": {firstVMID}}, f.guests(), "only the first VM exists")
	assert.Len(t, f.isos(), 1, "only the first VM's ISO exists")
}

// TestCreateRefusesFullZoneBesideZoneWithoutTemplate fills the one zone the
// node class has a template in: the other zone is not a candidate, so the
// launch still fails for lack of capacity.
func TestCreateRefusesFullZoneBesideZoneWithoutTemplate(t *testing.T) {
	t.Parallel()

	f := newLaunchFixture(t, launchSetup{zones: []string{"pve1", "pve2"}})
	f.nodeClass.Status.SelectedZones = f.nodeClass.Status.SelectedZones[:1]

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)
	assert.Equal(t, "pve1", node.Labels[corev1.LabelTopologyZone])

	_, err = f.create(f.ctx, "node-2")
	require.Error(t, err)
	assert.True(t, cloudprovider.IsInsufficientCapacityError(err), "no zone with a template has room: %v", err)
}

// TestCreateWithoutCandidateZoneIsNotLackOfCapacity asks for a zone the node
// class has no template in: zone choice never runs, so the failure is a
// mismatch, not a lack of capacity.
func TestCreateWithoutCandidateZoneIsNotLackOfCapacity(t *testing.T) {
	t.Parallel()

	f := newLaunchFixture(t, launchSetup{zones: []string{"pve1", "pve2"}})
	f.nodeClass.Status.SelectedZones = f.nodeClass.Status.SelectedZones[:1]

	nodeClaim := f.nodeClaim("node-1")
	nodeClaim.Spec.Requirements[0].Values = []string{"pve2"}

	_, err := f.provider.Create(f.ctx, nodeClaim, f.nodeClass, []*cloudprovider.InstanceType{f.instanceType})
	require.Error(t, err)
	assert.False(t, cloudprovider.IsInsufficientCapacityError(err), "no zone choice ran: %v", err)
	assert.Empty(t, f.guests())
}

func TestCreateWithoutInstanceTypes(t *testing.T) {
	t.Parallel()

	f := newLaunchFixture(t, launchSetup{})

	_, err := f.provider.Create(f.ctx, f.nodeClaim("node-1"), f.nodeClass, nil)
	require.EqualError(t, err, "failed to create instance: no instance types to try")
}

// TestCreatePlacesByStrategy launches two small VMs into two empty zones:
// balanced spreads them, best fit packs them into the first zone.
func TestCreatePlacesByStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		zoneBalance string
		want        []string
	}{
		{zoneBalance: v1alpha1.PlacementStrategyBalanced, want: []string{"pve1", "pve2"}},
		{zoneBalance: v1alpha1.PlacementStrategyBestFit, want: []string{"pve1", "pve1"}},
	}

	for _, tt := range tests {
		t.Run(tt.zoneBalance, func(t *testing.T) {
			t.Parallel()

			f := newLaunchFixture(t, launchSetup{zones: []string{"pve1", "pve2"}, cpus: 1, memory: "1Gi"})
			f.nodeClass.Spec.PlacementStrategy = &v1alpha1.PlacementStrategy{ZoneBalance: tt.zoneBalance}

			zones := []string{}

			for _, name := range []string{"node-1", "node-2"} {
				node, err := f.create(f.ctx, name)
				require.NoError(t, err)

				zones = append(zones, node.Labels[corev1.LabelTopologyZone])
			}

			assert.Equal(t, tt.want, zones)
		})
	}
}

func TestCreateRefusesExhaustedIPPool(t *testing.T) {
	// A /30 has one address left once the network, gateway, first host
	// and broadcast addresses are reserved.
	f := newLaunchFixture(t, launchSetup{subnet: "10.0.0.0/30", cpus: 1, memory: "1Gi"})

	_, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	_, err = f.create(f.ctx, "node-2")
	require.Error(t, err)

	assert.Equal(t, map[string][]int{"pve1": {firstVMID}}, f.guests(), "only the first VM exists")
	assert.Len(t, f.isos(), 1, "only the first VM's ISO exists")
}

// TestCreateFallsBackToNextZone fails the launch in the first zone, which
// has no ISO-capable storage, so the launch rolls back there before
// succeeding in the second.
func TestCreateFallsBackToNextZone(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{zones: []string{"pve1", "pve2"}, noISOStorage: []string{"pve1"}})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	assert.Equal(t, "pve2", node.Labels[corev1.LabelTopologyZone])
	assert.Equal(t, provider.GetProviderID(testRegion, firstVMID+1), node.Spec.ProviderID, "the first zone's launch was tried first")
	assert.Equal(t, map[string][]int{"pve2": {firstVMID + 1}}, f.guests(), "the first zone is clean")
	assert.Equal(t, f.firstAddress(), f.vmConfig(node).IPConfig[0].IPv4, "the first zone's address went back to the IP pool")
}

// TestCreateRollsBackCanceledLaunch cancels the launch while its ISO upload
// is running, and lets the upload finish after the launch gave up on it:
// the rollback still runs, and removes the ISO the upload left behind.
func TestCreateRollsBackCanceledLaunch(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{manualTasks: true})

	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, err := f.create(ctx, "node-1")
		done <- err
	}()

	var once sync.Once

	for {
		select {
		case err := <-done:
			require.ErrorContains(t, err, context.Canceled.Error())

			assert.Empty(t, f.guests(), "no VM is left over")
			assert.Empty(t, f.isos(), "the late ISO is removed")

			return
		case <-time.After(10 * time.Millisecond):
		}

		for _, upid := range f.cluster.Tasks().Running() {
			if strings.Split(upid, ":")[5] == "imgcopy" {
				once.Do(cancel)
			}

			f.cluster.Tasks().Complete(upid)
		}
	}
}

func TestDelete(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	nodeClaim := f.nodeClaim("node-1")
	nodeClaim.Labels = node.Labels
	nodeClaim.Status.ProviderID = node.Spec.ProviderID

	require.NoError(t, f.provider.Delete(f.ctx, nodeClaim))

	assert.Empty(t, f.guests(), "the VM is gone")

	node, err = f.create(f.ctx, "node-2")
	require.NoError(t, err, "the zone capacity is free again")

	assert.Equal(t, f.firstAddress(), f.vmConfig(node).IPConfig[0].IPv4, "the address is back in the IP pool")
}
