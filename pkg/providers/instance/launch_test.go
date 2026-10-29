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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

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
}

// launchFixture is an instance provider launching into a fake Proxmox
// cluster, with real zone capacity, IP pool and template sync behind it.
type launchFixture struct {
	t            *testing.T
	ctx          context.Context //nolint:containedctx
	cluster      *fakeapi.Cluster
	client       *proxmoxrest.Client
	provider     *instance.DefaultProvider
	capacity     *cloudcapacity.DefaultProvider
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

	ctx := options.ToContext(t.Context(), &options.Options{ProxmoxVMID: firstVMID, NodePolicy: setup.nodePolicy})

	capacity := cloudcapacity.NewProvider(ctx, pool)
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

	p, err := instance.NewProvider(ctx, kube, bootstrap.NewProvider(ctx, kube), pool, capacity,
		nodeipam.NewDefaultProvider(ctx, capacity), templates)
	require.NoError(t, err)

	size := resource.MustParse("10G")

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			BootDevice:     &v1alpha1.BlockDevice{Size: &size},
			Tags:           []string{"karpenter", "web"},
			SecurityGroups: []v1alpha1.SecurityGroups{{Name: "sg-web", Interface: "net0"}, {Name: "sg-ssh"}},
			ResourcePool:   "karpenter",
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
		Name: fmt.Sprintf("c1.%dVCPU-%s", setup.cpus, setup.memory),
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(int64(setup.cpus), resource.DecimalSI),
			corev1.ResourceMemory: resource.MustParse(setup.memory),
		},
		Offerings: offerings,
	}

	return &launchFixture{
		t:            t,
		ctx:          ctx,
		cluster:      cl,
		client:       client,
		provider:     p,
		capacity:     capacity,
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
