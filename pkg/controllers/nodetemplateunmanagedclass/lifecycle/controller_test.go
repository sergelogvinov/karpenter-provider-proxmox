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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateunmanagedclass/lifecycle"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	talosZone1 = instancetemplate.InstanceTemplateInfo{
		Name: "talos", Region: "region-1", Zone: "zone-1", TemplateID: 1000,
		TemplateTags: []string{"talos", "v1.11"}, Status: instancetemplate.InstanceTemplateStatusAvailable,
	}
	talosZone2 = instancetemplate.InstanceTemplateInfo{
		Name: "talos", Region: "region-1", Zone: "zone-2", TemplateID: 1001,
		TemplateTags: []string{"talos"}, Status: instancetemplate.InstanceTemplateStatusAvailable,
	}
	talosRegion2 = instancetemplate.InstanceTemplateInfo{
		Name: "talos", Region: "region-2", Zone: "zone-1", TemplateID: 2000,
		TemplateTags: []string{"talos", "v1.11"}, Status: instancetemplate.InstanceTemplateStatusAvailable,
	}
	ubuntu = instancetemplate.InstanceTemplateInfo{
		Name: "ubuntu", Region: "region-1", Zone: "zone-1", TemplateID: 1002,
		TemplateTags: []string{"ubuntu"}, Status: instancetemplate.InstanceTemplateStatusAvailable,
	}
)

// fakeProvider serves a fixed VM template inventory.
type fakeProvider struct {
	instancetemplate.Provider

	inventory []instancetemplate.InstanceTemplateInfo
	syncErr   error

	syncs int
}

func (p *fakeProvider) SyncInstanceTemplates(_ context.Context, _ ...string) error {
	p.syncs++

	return p.syncErr
}

func (p *fakeProvider) ListWithFilter(_ context.Context, filter ...func(*instancetemplate.InstanceTemplateInfo) bool) []instancetemplate.InstanceTemplateInfo {
	return instancetemplate.Filter(p.inventory, filter...)
}

func newTemplate(spec v1alpha1.ProxmoxUnmanagedTemplateSpec) *v1alpha1.ProxmoxUnmanagedTemplate {
	return &v1alpha1.ProxmoxUnmanagedTemplate{
		Name: "talos",
		Spec: spec,
	}
}

func newKubeClient(t *testing.T, tpl *v1alpha1.ProxmoxUnmanagedTemplate, funcs interceptor.Funcs) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ProxmoxUnmanagedTemplate{}).
		WithObjects(tpl).
		WithInterceptorFuncs(funcs).
		Build()
}

// root is root in every region the tests use.
var root = config.Privileges{"region-1": true, "region-2": true}

func reconcile(t *testing.T, kubeClient client.Client, provider *fakeProvider) (time.Duration, error) {
	t.Helper()

	return reconcileAs(t, kubeClient, provider, root)
}

func reconcileAs(t *testing.T, kubeClient client.Client, provider *fakeProvider, privileges config.Privileges) (time.Duration, error) {
	t.Helper()

	res, err := lifecycle.NewController(kubeClient, provider, privileges).Reconcile(t.Context(), get(t, kubeClient))

	return res.RequeueAfter, err
}

func get(t *testing.T, kubeClient client.Client) *v1alpha1.ProxmoxUnmanagedTemplate {
	t.Helper()

	tpl := &v1alpha1.ProxmoxUnmanagedTemplate{}
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "talos"}, tpl))

	return tpl
}

func TestMatchingTemplatesBecomePlacements(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{
		Region: "region-1",
		Tags:   []string{"talos", "v1.11"},
	}), interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, talosZone2, talosRegion2, ubuntu}}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement()}, tpl.TemplatePlacements())
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
	assert.Equal(t, int64(1), tpl.Status.Resources.Name(v1alpha1.ResourceZones, "").Value())
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestTemplateNameMatchesAcrossRegions(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{
		TemplateName: "talos",
	}), interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, talosRegion2, ubuntu}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement(), talosRegion2.Placement()}, tpl.TemplatePlacements())
}

func TestNoMatchClearsPlacements(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{Tags: []string{"flatcar"}})
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{talosZone1.Placement()})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, ubuntu}}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Empty(t, tpl.TemplatePlacements())

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, "TemplatesNotFound", ready.Reason)
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestInvalidSpecSkipsInventory(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{Region: "region-1"})
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{talosZone1.Placement()})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1}}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Zero(t, provider.syncs)
	assert.Empty(t, tpl.TemplatePlacements())

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, "TemplateValidation", ready.Reason)
	assert.Zero(t, requeue)
}

func TestSyncFailureKeepsStatus(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"})
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{talosZone1.Placement()})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{syncErr: errors.New("proxmox unavailable")}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement()}, tpl.TemplatePlacements())
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestVMTemplateWithoutHugepagesOrAffinity(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, talosZone2}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, &v1alpha1.VMTemplateInfo{}, tpl.Status.Template)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent).IsTrue())
}

func TestVMTemplateWithHugepagesAndAffinity(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	zone1, zone2 := talosZone1, talosZone2
	zone1.Hugepages, zone1.Affinity = "1024", true
	zone2.Hugepages, zone2.Affinity = "1024", true
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{zone1, zone2}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, &v1alpha1.VMTemplateInfo{Hugepages: "1024", Affinity: true}, tpl.Status.Template)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent).IsTrue())
}

func TestVMTemplateRefreshesWhenItsConfigChanges(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	provider.inventory[0].Hugepages = "2"

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, &v1alpha1.VMTemplateInfo{Hugepages: "2"}, get(t, kubeClient).Status.Template)
}

func TestDisagreeingVMTemplatesRaiseCondition(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"})
	tpl.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: "2"}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})

	zone2 := talosZone2
	zone2.Hugepages = "2"
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, zone2}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)

	consistent := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent)
	assert.True(t, consistent.IsFalse())
	assert.Equal(t, "VMTemplatesDisagree", consistent.Reason)
	assert.Contains(t, consistent.Message, "region-1/zone-1/1000: hugepages=none affinity=false")
	assert.Contains(t, consistent.Message, "region-1/zone-2/1001: hugepages=2 affinity=false")
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue(), "disagreement does not make the template unready")
}

func TestNoMatchClearsVMTemplate(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{Tags: []string{"flatcar"}})
	tpl.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: "2"}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)
	assert.Nil(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent))
}

// A tags-only selector can match several VM templates in one zone; they are
// compared like VM templates in different zones.
func TestDisagreeingVMTemplatesInOneZoneRaiseCondition(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{Tags: []string{"talos"}}), interceptor.Funcs{})

	other := talosZone1
	other.Name, other.TemplateID, other.Affinity = "talos-numa", 1003, true
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, other}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)

	consistent := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent)
	assert.True(t, consistent.IsFalse())
	assert.Contains(t, consistent.Message, "region-1/zone-1/1000: hugepages=none affinity=false")
	assert.Contains(t, consistent.Message, "region-1/zone-1/1003: hugepages=none affinity=true")
}

// Unavailable VM templates are not listed, even when the selector matches them.
func TestUnavailableVMTemplateIsNotMatched(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	disabled := talosZone2
	disabled.Status, disabled.Hugepages = instancetemplate.InstanceTemplateStatusMultipleStorageIDs, "2"
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1, disabled}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement()}, tpl.TemplatePlacements())
	assert.Equal(t, &v1alpha1.VMTemplateInfo{}, tpl.Status.Template)
}

func TestStatusConflictRequeues(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{
		SubResourcePatch: func(_ context.Context, _ client.Client, _ string, obj client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "proxmoxunmanagedtemplates"}, obj.GetName(), errors.New("modified"))
		},
	})
	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{talosZone1}}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, 5*time.Second, requeue)
}

// Without root a VM template with affinity set is dropped: every clone would
// inherit CPUs the controller can neither replace nor clear.
func TestAffinityVMTemplateDroppedWithoutRoot(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{withAffinity(talosZone1), talosZone2}}

	_, err := reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone2.Placement()}, tpl.TemplatePlacements())
	assert.Equal(t, &v1alpha1.VMTemplateInfo{}, tpl.Status.Template)

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.Equal(t, v1alpha1.ReasonTemplateCPUAffinityDropped, ready.Reason)
	assert.Contains(t, ready.Message, "region-1/zone-1/1000")
}

func TestAllVMTemplatesWithAffinityMakeTemplateUnreadyWithoutRoot(t *testing.T) {
	tpl := newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"})
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{talosZone1.Placement()})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})

	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{withAffinity(talosZone1)}}

	requeue, err := reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Empty(t, tpl.TemplatePlacements())
	assert.Nil(t, tpl.Status.Template)

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsFalse())
	assert.Equal(t, v1alpha1.ReasonTemplateCPUAffinitySet, ready.Reason)
	assert.Contains(t, ready.Message, "root")
	assert.Contains(t, ready.Message, "region-1/zone-1/1000")
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestTemplatePlacementReturnsOnceAffinityIsRemovedWithoutRoot(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{withAffinity(talosZone1)}}

	_, err := reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)
	require.Empty(t, get(t, kubeClient).TemplatePlacements())

	provider.inventory[0].Affinity = false

	_, err = reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement()}, tpl.TemplatePlacements())
	assert.Equal(t, &v1alpha1.VMTemplateInfo{}, tpl.Status.Template)

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.Empty(t, ready.Message)
}

func TestTemplateReadyClearsCPUAffinityDroppedOnceAffinityIsRemovedWithoutRoot(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{withAffinity(talosZone1), talosZone2}}

	_, err := reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)
	require.Equal(t, v1alpha1.ReasonTemplateCPUAffinityDropped, get(t, kubeClient).StatusConditions().Get(v1alpha1.ConditionTemplateReady).Reason)

	provider.inventory[0].Affinity = false

	_, err = reconcileAs(t, kubeClient, provider, config.Privileges{})
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosZone1.Placement(), talosZone2.Placement()}, tpl.TemplatePlacements())

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.NotEqual(t, v1alpha1.ReasonTemplateCPUAffinityDropped, ready.Reason)
	assert.Empty(t, ready.Message)
}

// Privilege is per region: only the region without root drops its VM
// template with affinity set.
func TestAffinityVMTemplateDroppedOnlyInRegionWithoutRoot(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(v1alpha1.ProxmoxUnmanagedTemplateSpec{TemplateName: "talos"}), interceptor.Funcs{})

	provider := &fakeProvider{inventory: []instancetemplate.InstanceTemplateInfo{withAffinity(talosZone1), withAffinity(talosRegion2)}}

	_, err := reconcileAs(t, kubeClient, provider, config.Privileges{"region-2": true})
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{talosRegion2.Placement()}, tpl.TemplatePlacements())
	assert.Equal(t, &v1alpha1.VMTemplateInfo{Affinity: true}, tpl.Status.Template)

	ready := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
	assert.True(t, ready.IsTrue())
	assert.Equal(t, v1alpha1.ReasonTemplateCPUAffinityDropped, ready.Reason)
	assert.Contains(t, ready.Message, "region-1/zone-1/1000")
	assert.NotContains(t, ready.Message, "region-2")
}

// withAffinity returns info with the affinity option set in its VM config.
func withAffinity(info instancetemplate.InstanceTemplateInfo) instancetemplate.InstanceTemplateInfo {
	info.Affinity = true

	return info
}
