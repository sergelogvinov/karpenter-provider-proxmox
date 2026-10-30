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

package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeCloudCapacity serves one storage available in zone-1 of region-1.
type fakeCloudCapacity struct {
	cloudcapacity.Provider
}

func (fakeCloudCapacity) Regions() []string { return []string{"region-1"} }

func (fakeCloudCapacity) GetStorage(region string, storage string, _ ...func(*cloudcapacity.NodeStorageCapacityInfo) bool) *cloudcapacity.NodeStorageCapacityInfo {
	return &cloudcapacity.NodeStorageCapacityInfo{Name: storage, Region: region, Zones: []string{"zone-1"}}
}

func newNodeClass() *v1alpha1.ProxmoxNodeClass {
	return &v1alpha1.ProxmoxNodeClass{
		Name: "default",
		Spec: v1alpha1.ProxmoxNodeClassSpec{
			InstanceTemplateRef: &v1alpha1.InstanceTemplateClassReference{Kind: "ProxmoxUnmanagedTemplate", Name: "talos"},
			BootDevice:          &v1alpha1.BlockDevice{Storage: "local-lvm"},
		},
	}
}

func newKubeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ProxmoxUnmanagedTemplate{}, &v1alpha1.ProxmoxNodeClass{}).
		WithObjects(objs...).
		Build()
}

func reconcileInstanceTemplate(t *testing.T, kubeClient client.Client, nodeClass *v1alpha1.ProxmoxNodeClass) {
	t.Helper()

	_, err := (&InstanceTemplate{kubeClient: kubeClient, cloudCapacityProvider: fakeCloudCapacity{}}).Reconcile(t.Context(), nodeClass)
	require.NoError(t, err)
}

// Without root a ProxmoxUnmanagedTemplate whose VM templates all have affinity
// set has no template placements; the NodeClass says why.
func TestTemplateCPUAffinitySetShownOnNodeClass(t *testing.T) {
	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	message := "affinity is root-only, so VM templates with it set are not used: region-1/zone-1/1000"
	tpl.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, v1alpha1.ReasonTemplateCPUAffinitySet, message)

	nodeClass := newNodeClass()
	reconcileInstanceTemplate(t, newKubeClient(t, tpl), nodeClass)

	ready := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, v1alpha1.ReasonTemplateCPUAffinitySet, ready.Reason)
	assert.Equal(t, message, ready.Message)
	assert.Empty(t, nodeClass.TemplatePlacements("region-1"))
}

func TestNoTemplatePlacementsIsTemplatesNotFound(t *testing.T) {
	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	tpl.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, "TemplatesNotFound", "no VM template matches the spec")

	nodeClass := newNodeClass()
	reconcileInstanceTemplate(t, newKubeClient(t, tpl), nodeClass)

	ready := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, "TemplatesNotFound", ready.Reason)
}

func TestTemplateCPUAffinityDroppedKeepsNodeClassReady(t *testing.T) {
	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: "region-1", Zone: "zone-1", TemplateID: 1001}})
	tpl.StatusConditions().SetTrueWithReason(v1alpha1.ConditionTemplateReady, v1alpha1.ReasonTemplateCPUAffinityDropped,
		"affinity is root-only, so VM templates with it set are not used: region-1/zone-2/1000")

	nodeClass := newNodeClass()
	reconcileInstanceTemplate(t, newKubeClient(t, tpl), nodeClass)

	ready := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.Equal(t, []v1alpha1.TemplatePlacement{{Region: "region-1", Zone: "zone-1", TemplateID: 1001}}, nodeClass.TemplatePlacements("region-1"))
}

// A NodeClass in a region whose VM templates are all dropped for affinity says
// why, while the template stays ready on its other regions.
func TestTemplateCPUAffinityDroppedInNodeClassRegionShownOnNodeClass(t *testing.T) {
	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: "region-2", Zone: "zone-1", TemplateID: 1001}})

	message := "affinity is root-only, so VM templates with it set are not used: region-1/zone-1/1000"
	tpl.StatusConditions().SetTrueWithReason(v1alpha1.ConditionTemplateReady, v1alpha1.ReasonTemplateCPUAffinityDropped, message)

	nodeClass := newNodeClass()
	nodeClass.Spec.Region = "region-1"
	reconcileInstanceTemplate(t, newKubeClient(t, tpl), nodeClass)

	ready := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, v1alpha1.ReasonTemplateCPUAffinitySet, ready.Reason)
	assert.Equal(t, message, ready.Message)
}

// Once affinity is removed and the template has template placements again,
// the NodeClass is ready again.
func TestNodeClassReadyOnceTemplateCPUAffinityIsRemoved(t *testing.T) {
	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{Name: "talos"}
	tpl.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, v1alpha1.ReasonTemplateCPUAffinitySet,
		"affinity is root-only, so VM templates with it set are not used: region-1/zone-1/1000")

	kubeClient := newKubeClient(t, tpl)
	nodeClass := newNodeClass()
	reconcileInstanceTemplate(t, kubeClient, nodeClass)
	require.True(t, nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady).IsFalse())

	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: "region-1", Zone: "zone-1", TemplateID: 1000}})
	tpl.StatusConditions().SetTrue(v1alpha1.ConditionTemplateReady)
	require.NoError(t, kubeClient.Status().Update(t.Context(), tpl))

	reconcileInstanceTemplate(t, kubeClient, nodeClass)

	ready := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.Equal(t, []v1alpha1.TemplatePlacement{{Region: "region-1", Zone: "zone-1", TemplateID: 1000}}, nodeClass.TemplatePlacements("region-1"))
}
