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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	region = "region-1"
	// zone has a pool of 63 1GiB hugepages on its one NUMA node.
	zone = "pve-1"

	nodeSettings = `{"region-1": {"pve-1": {"sockets": 1, "threads": 2, "hugepagesize": 1073741824,
  "nodes": {"0": {"cpus": "0-15", "memsize": 67645734912}}}}}`
	instanceTypesFile = `[
  {"name": "n1", "numa": 1, "capacity": {"ephemeral-storage": "30Gi", "pods": "110"}},
  {"name": "c1.2VCPU-4GB", "capacity": {"cpu": "2", "memory": "4Gi", "ephemeral-storage": "30Gi", "pods": "64"}}
]`

	// nonNUMAType is the static type, numaType the whole slot of the zone.
	nonNUMAType = "c1.2VCPU-4GB"
	numaType    = "n1.16VCPU-63GB"
)

// launches is an instance provider that records the instance types of each launch and launches nothing.
type launches struct {
	instance.Provider

	instanceTypes [][]string
}

var errNoLaunch = errors.New("no launch in this test")

func (l *launches) Create(_ context.Context, _ *karpv1.NodeClaim, _ *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	names := []string{}
	for _, it := range instanceTypes {
		names = append(names, it.Name)
	}

	l.instanceTypes = append(l.instanceTypes, names)

	return nil, errNoLaunch
}

// newCloudProvider is a cloud provider, without root, on zone, with a ready NodeClass "default" whose
// ProxmoxUnmanagedTemplate has VM templates sharing hugepages, and a NodePool "default" on it.
func newCloudProvider(t *testing.T, hugepages string) (*proxmox.CloudProvider, *launches) {
	t.Helper()

	dir := t.TempDir()
	settingsFile := filepath.Join(dir, "nodes.json")
	require.NoError(t, os.WriteFile(settingsFile, []byte(nodeSettings), 0o600))

	typesFile := filepath.Join(dir, "instancetypes.json")
	require.NoError(t, os.WriteFile(typesFile, []byte(instanceTypesFile), 0o600))

	ctx := options.ToContext(t.Context(), &options.Options{
		NodePolicy:            "static",
		CPUOvercommitRatio:    1,
		NodeSettingFilePath:   settingsFile,
		InstanceTypesFilePath: typesFile,
	})

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	cluster.Node(zone).SetResources(16, int64(64<<30))

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{
		Region:      region,
		URL:         "https://127.0.0.1:8006/api2/json",
		TokenID:     "user@pve!token",
		TokenSecret: "secret",
	}})
	require.NoError(t, err)
	pool.Set(region, cluster.Client(t))

	capacity := cloudcapacity.NewProvider(ctx, pool, nil)
	require.NoError(t, capacity.SyncNodeCapacity(ctx))

	scheme := runtime.NewScheme()
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(gv, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{}, &karpv1.NodePool{}, &karpv1.NodePoolList{})
	metav1.AddToGroupVersion(scheme, gv)
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	template := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	template.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: hugepages}

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: template.Name},
		},
	}
	nodeClass.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: 100}})
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassTemplateReady)
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassMetadataOptionsReady)

	nodePool := &karpv1.NodePool{
		Name: "default",
		Spec: karpv1.NodePoolSpec{Template: karpv1.NodeClaimTemplate{Spec: karpv1.NodeClaimTemplateSpec{
			NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.SchemeGroupVersion.Group, Kind: "ProxmoxNodeClass", Name: nodeClass.Name},
		}}},
	}

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(template, nodeClass, nodePool).Build()

	instanceTypes := instancetype.NewDefaultProvider(ctx, capacity, instancetype.Readers{API: kube, Cached: kube})
	require.NoError(t, instanceTypes.UpdateInstanceTypes(ctx))
	require.NoError(t, instanceTypes.UpdateInstanceTypeOfferings(ctx))

	l := &launches{}

	return proxmox.NewCloudProvider(ctx, kube, nil, l, nil, instanceTypes, capacity), l
}

// offeredTypes are the instance types GetInstanceTypes offers in zone for the NodePool.
func offeredTypes(t *testing.T, c *proxmox.CloudProvider) []string {
	t.Helper()

	nodePool := &karpv1.NodePool{}
	nodePool.Name = "default"
	nodePool.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{Kind: "ProxmoxNodeClass", Name: "default"}

	list, err := c.GetInstanceTypes(t.Context(), nodePool)
	require.NoError(t, err)

	offered := []string{}

	for _, it := range list {
		if len(it.Offerings.Available()) > 0 {
			offered = append(offered, it.Name)
		}
	}

	return offered
}

// create runs Create for a small NodeClaim that accepts both instance types.
func create(t *testing.T, c *proxmox.CloudProvider) error {
	t.Helper()

	nodeClaim := &karpv1.NodeClaim{
		Name: "node-1",
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{Kind: "ProxmoxNodeClass", Name: "default"},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{
				Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{nonNUMAType, numaType},
			}},
			Resources: karpv1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
		},
	}

	_, err := c.Create(t.Context(), nodeClaim)

	return err
}

func TestCloudProviderOffersByTemplateHugepages(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		hugepages string
		offered   []string
	}{
		{name: "hugepage template offers every type on one NUMA node", hugepages: "1024", offered: []string{nonNUMAType, numaType}},
		{name: "template without hugepages offers the non-NUMA type", hugepages: "", offered: []string{nonNUMAType}},
		{name: "template of another page size offers nothing", hugepages: "2", offered: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, l := newCloudProvider(t, tc.hugepages)

			require.ElementsMatch(t, tc.offered, offeredTypes(t, c), "GetInstanceTypes")

			err := create(t, c)
			if len(tc.offered) == 0 {
				require.True(t, cloudprovider.IsInsufficientCapacityError(err), "Create: %v", err)
				require.Empty(t, l.instanceTypes, "nothing is launched")

				return
			}

			require.ErrorIs(t, err, errNoLaunch)
			require.Equal(t, [][]string{tc.offered}, l.instanceTypes, "Create launches only the offered types")
		})
	}
}
