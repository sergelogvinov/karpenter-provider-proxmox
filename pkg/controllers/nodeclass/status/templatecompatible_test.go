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

package status_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodeclass/status"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/templatecheck"

	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	region = "region-1"
	// hugepageZone has a pool of 1GiB hugepages, plainZone has none.
	hugepageZone = "pve-1"
	plainZone    = "pve-2"
	storageID    = "local-lvm"
)

// fakeCapacity is two zones of region with a storage on both, the first with a hugepage pool.
type fakeCapacity struct {
	cloudcapacity.Provider

	root bool
}

func (f *fakeCapacity) Regions() []string { return []string{region} }

func (f *fakeCapacity) GetStorage(r, name string, _ ...func(*cloudcapacity.NodeStorageCapacityInfo) bool) *cloudcapacity.NodeStorageCapacityInfo {
	if r != region || name != storageID {
		return nil
	}

	return &cloudcapacity.NodeStorageCapacityInfo{Name: name, Region: region, Zones: []string{hugepageZone, plainZone}}
}

func (f *fakeCapacity) Root(string) bool { return f.root }

func (f *fakeCapacity) HugePageSize(r, zone string) uint64 {
	if r == region && zone == hugepageZone {
		return 1 << 30
	}

	return 0
}

// newKubeClient is a cluster with a NodeClass whose ProxmoxUnmanagedTemplate has VM templates on both zones
// sharing vmTemplate.
func newKubeClient(t *testing.T, vmTemplate *v1alpha1.VMTemplateInfo) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	template := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	template.SetTemplatePlacements([]v1alpha1.TemplatePlacement{
		{Region: region, Zone: hugepageZone, TemplateID: 100},
		{Region: region, Zone: plainZone, TemplateID: 101},
	})
	template.Status.Template = vmTemplate

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: "talos"},
			BootDevice:          &v1alpha1.BlockDevice{Storage: storageID},
			MetadataOptions:     &v1alpha1.MetadataOptions{Type: "none"},
		},
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ProxmoxNodeClass{}, &v1alpha1.ProxmoxUnmanagedTemplate{}).
		WithObjects(template, nodeClass).
		Build()
}

// reconcile runs the status controller on the NodeClass and returns it.
func reconcile(t *testing.T, kubeClient client.Client, capacity *fakeCapacity) *v1alpha1.ProxmoxNodeClass {
	t.Helper()

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "default"}, nodeClass))

	_, err := status.NewController(kubeClient, capacity).Reconcile(t.Context(), nodeClass)
	require.NoError(t, err)

	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "default"}, nodeClass))

	return nodeClass
}

// setHugepages changes the hugepages its VM templates share in the template's status.
func setHugepages(t *testing.T, kubeClient client.Client, vmTemplate *v1alpha1.VMTemplateInfo) {
	t.Helper()

	template := &v1alpha1.ProxmoxUnmanagedTemplate{}
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "talos"}, template))
	template.Status.Template = vmTemplate
	require.NoError(t, kubeClient.Status().Update(t.Context(), template))
}

func TestTemplateCompatibleCondition(t *testing.T) {
	t.Parallel()

	kubeClient := newKubeClient(t, &v1alpha1.VMTemplateInfo{Hugepages: "2"})
	capacity := &fakeCapacity{}

	nodeClass := reconcile(t, kubeClient, capacity)
	cond := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible)
	require.True(t, cond.IsFalse())
	require.Equal(t, templatecheck.ReasonHugepagesMismatch, cond.Reason)
	require.Contains(t, cond.Message, region+"/"+hugepageZone)
	require.NotContains(t, cond.Message, plainZone, "a zone without a hugepage pool is not judged")
	require.True(t, nodeClass.StatusConditions().Root().IsTrue(), "the condition is not a Ready dependent")

	// The template now has the pool's page size: only the non-NUMA types stay dropped, the intended split.
	setHugepages(t, kubeClient, &v1alpha1.VMTemplateInfo{Hugepages: "1024"})

	cond = reconcile(t, kubeClient, capacity).StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible)
	require.True(t, cond.IsTrue())
	require.Equal(t, templatecheck.ReasonHugepagesSet, cond.Reason)
	require.Contains(t, cond.Message, "non-NUMA types dropped")
	require.NotContains(t, cond.Message, " NUMA types dropped")

	// As root nothing is dropped.
	capacity.root = true

	cond = reconcile(t, kubeClient, capacity).StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible)
	require.True(t, cond.IsTrue())
	require.Empty(t, cond.Message)
}
