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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateclass/lifecycle"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var (
	placement  = v1alpha1.TemplatePlacement{Region: "region-1", Zone: "zone-1", TemplateID: 1000}
	placement2 = v1alpha1.TemplatePlacement{Region: "region-1", Zone: "zone-2", TemplateID: 1001}
	placementR = v1alpha1.TemplatePlacement{Region: "region-2", Zone: "zone-1", TemplateID: 2000}
)

// fakeProvider returns canned results and records what it was asked to do.
type fakeProvider struct {
	instancetemplate.Provider

	result    instancetemplate.CreateResult
	createErr error
	remaining []v1alpha1.TemplatePlacement
	deleteErr error
	updateErr error
	onCreate  func()
	inventory []instancetemplate.InstanceTemplateInfo

	calls   []string
	deleted []v1alpha1.TemplatePlacement
	images  []string
	updated []v1alpha1.TemplatePlacement
}

// created is a CreateResult whose placements were all rendered by the call
// and are the only zones it targeted.
func created(placements ...v1alpha1.TemplatePlacement) instancetemplate.CreateResult {
	return instancetemplate.CreateResult{Placements: placements, Created: placements, Targets: targets(placements...)}
}

// reused is a CreateResult whose placements all existed before the call and
// are the only zones it targeted.
func reused(placements ...v1alpha1.TemplatePlacement) instancetemplate.CreateResult {
	return instancetemplate.CreateResult{Placements: placements, Targets: targets(placements...)}
}

func targets(placements ...v1alpha1.TemplatePlacement) map[string][]string {
	zones := map[string][]string{}
	for _, p := range placements {
		zones[p.Region] = append(zones[p.Region], p.Zone)
	}

	return zones
}

func (p *fakeProvider) Create(_ context.Context, _ *v1alpha1.ProxmoxTemplate) (instancetemplate.CreateResult, error) {
	p.calls = append(p.calls, "create")

	if p.onCreate != nil {
		p.onCreate()
	}

	return p.result, p.createErr
}

func (p *fakeProvider) Delete(_ context.Context, _ *v1alpha1.ProxmoxTemplate, imageID string, placements []v1alpha1.TemplatePlacement) ([]v1alpha1.TemplatePlacement, error) {
	p.calls = append(p.calls, "delete")
	p.images = append(p.images, imageID)
	p.deleted = append(p.deleted, placements...)

	if p.deleteErr != nil {
		return p.remaining, p.deleteErr
	}

	return nil, nil
}

func (p *fakeProvider) Update(_ context.Context, _ *v1alpha1.ProxmoxTemplate, placements []v1alpha1.TemplatePlacement) error {
	p.calls = append(p.calls, "update")
	p.updated = append(p.updated, placements...)

	return p.updateErr
}

func (p *fakeProvider) ListWithFilter(_ context.Context, filter ...func(*instancetemplate.InstanceTemplateInfo) bool) []instancetemplate.InstanceTemplateInfo {
	return instancetemplate.Filter(p.inventory, filter...)
}

// vmTemplate is the listed VM template at placement p.
func vmTemplate(p v1alpha1.TemplatePlacement, hugepages string, affinity bool) instancetemplate.InstanceTemplateInfo {
	return instancetemplate.InstanceTemplateInfo{
		Name: "talos", Region: p.Region, Zone: p.Zone, TemplateID: p.TemplateID, HasTemplateHash: true,
		Status: instancetemplate.InstanceTemplateStatusAvailable, Hugepages: hugepages, Affinity: affinity,
	}
}

func newTemplate() *v1alpha1.ProxmoxTemplate {
	return &v1alpha1.ProxmoxTemplate{
		Name: "talos",
		Spec: v1alpha1.ProxmoxTemplateSpec{
			SourceImage: &v1alpha1.SourceImage{URL: "https://example.com/talos.raw.xz", ImageName: "talos.raw"},
			StorageIDs:  []string{"local"},
			Description: "v1",
		},
	}
}

// readyTemplate is a template whose VM templates already exist and whose
// in-place fields have been applied.
func readyTemplate() *v1alpha1.ProxmoxTemplate {
	tpl := newTemplate()
	tpl.Finalizers = []string{v1alpha1.TerminationFinalizer}
	tpl.Annotations = map[string]string{v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash: tpl.InPlaceHash()}
	tpl.Status.ImageID = tpl.GetImageID()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement})

	return tpl
}

func newKubeClient(t *testing.T, tpl *v1alpha1.ProxmoxTemplate, funcs interceptor.Funcs) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ProxmoxTemplate{}).
		WithObjects(tpl).
		WithInterceptorFuncs(funcs).
		Build()
}

func reconcile(t *testing.T, kubeClient client.Client, provider *fakeProvider) (time.Duration, error) {
	t.Helper()

	tpl := &v1alpha1.ProxmoxTemplate{}
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "talos"}, tpl))

	res, err := lifecycle.NewController(kubeClient, provider).Reconcile(t.Context(), tpl)

	return res.RequeueAfter, err
}

func get(t *testing.T, kubeClient client.Client) *v1alpha1.ProxmoxTemplate {
	t.Helper()

	tpl := &v1alpha1.ProxmoxTemplate{}
	require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Name: "talos"}, tpl))

	return tpl
}

func TestNewTemplateIsCreatedInOnePass(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{result: created(placement)}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.True(t, controllerutil.ContainsFinalizer(tpl, v1alpha1.TerminationFinalizer))
	assert.Equal(t, []string{"create"}, provider.calls)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, tpl.TemplatePlacements())
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestNothingInstalledLeavesTemplateUnready(t *testing.T) {
	tpl := newTemplate()
	tpl.Finalizers = []string{v1alpha1.TerminationFinalizer}
	tpl.Annotations = map[string]string{v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash: "stale"}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsUnknown())
	assert.Equal(t, []string{"create"}, provider.calls)
	assert.Equal(t, "stale", tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])
	assert.Equal(t, 10*time.Second, requeue)
}

func TestSourceImageChangeReplacesTemplates(t *testing.T) {
	tpl := readyTemplate()
	tpl.Status.ImageID = "talos-1.raw"

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: created(placement)}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"delete", "create"}, provider.calls)
	assert.Equal(t, []string{"talos-1.raw"}, provider.images)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, provider.deleted)
	assert.Equal(t, tpl.GetImageID(), tpl.Status.ImageID)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
}

func TestInPlaceChangeUpdatesOnce(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: reused(placement)}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"create", "update"}, provider.calls)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, provider.updated)
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, []string{"create", "update", "create"}, provider.calls)
}

func TestMissingInPlaceHashIsAdoptedWithoutUpdate(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations = nil

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: reused(placement)}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"create"}, provider.calls)
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])
}

func TestDeletionFinalizesWithoutUpdate(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"
	tpl.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, []string{"delete"}, provider.calls)

	err = kubeClient.Get(t.Context(), client.ObjectKey{Name: "talos"}, &v1alpha1.ProxmoxTemplate{})
	assert.True(t, apierrors.IsNotFound(err), "finalizer removal should let the object go, got %v", err)
}

func TestDeletionFailureKeepsFinalizer(t *testing.T) {
	tpl := readyTemplate()
	tpl.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{deleteErr: errors.New("proxmox unavailable")}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.True(t, controllerutil.ContainsFinalizer(tpl, v1alpha1.TerminationFinalizer))
	assert.Equal(t, 10*time.Second, requeue)
}

func TestFinalizerConflictRequeuesBeforeCreate(t *testing.T) {
	kubeClient := newKubeClient(t, newTemplate(), interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "proxmoxtemplates"}, obj.GetName(), errors.New("modified"))
		},
	})
	provider := &fakeProvider{result: created(placement)}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Empty(t, provider.calls)
	assert.Equal(t, 10*time.Second, requeue)
}

func TestDeletionDuringCreateKeepsPlacements(t *testing.T) {
	tpl := newTemplate()
	tpl.Finalizers = []string{v1alpha1.TerminationFinalizer}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: created(placement)}
	provider.onCreate = func() {
		require.NoError(t, kubeClient.Delete(t.Context(), newTemplate()))
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	require.False(t, tpl.DeletionTimestamp.IsZero())
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, tpl.TemplatePlacements())

	provider.onCreate = nil

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, []string{"create", "delete"}, provider.calls)
}

func TestReplacementSkipsUpdate(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: created(placement)}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"create"}, provider.calls)
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])
}

func TestInPlaceUpdateSkipsCreatedPlacements(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: instancetemplate.CreateResult{
		Placements: []v1alpha1.TemplatePlacement{placement, placement2},
		Created:    []v1alpha1.TemplatePlacement{placement2},
		Targets:    targets(placement, placement2),
	}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, provider.updated)
}

func TestPartialCreateKeepsPlacements(t *testing.T) {
	tpl := newTemplate()
	tpl.Finalizers = []string{v1alpha1.TerminationFinalizer}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{result: created(placement), createErr: errors.New("zone-2: download failed")}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, tpl.TemplatePlacements())
	assert.Equal(t, tpl.GetImageID(), tpl.Status.ImageID)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
	assert.Equal(t, 10*time.Second, requeue)
}

func TestPartialImageReplacementKeepsOldImage(t *testing.T) {
	tpl := readyTemplate()
	tpl.Status.ImageID = "talos-1.raw"
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result:    created(placement, placement2),
		remaining: []v1alpha1.TemplatePlacement{placement2},
		deleteErr: errors.New("zone-2: proxmox unavailable"),
	}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"delete"}, provider.calls)
	assert.Equal(t, "talos-1.raw", tpl.Status.ImageID)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement2}, tpl.TemplatePlacements())
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsFalse())
	assert.Equal(t, 10*time.Second, requeue)
}

func TestPartialFinalizeKeepsRemainingPlacements(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})
	tpl.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		remaining: []v1alpha1.TemplatePlacement{placement2},
		deleteErr: errors.New("zone-2: proxmox unavailable"),
	}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{tpl.GetImageID()}, provider.images)
	assert.True(t, controllerutil.ContainsFinalizer(tpl, v1alpha1.TerminationFinalizer))
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement2}, tpl.TemplatePlacements())
	assert.Equal(t, 10*time.Second, requeue)
}

// staleTemplate is a ready template with VM templates in zone-1 and zone-2
// of region-1 and in zone-1 of region-2.
func staleTemplate() *v1alpha1.ProxmoxTemplate {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2, placementR})

	return tpl
}

func TestUntargetedZoneIsDeleted(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{result: instancetemplate.CreateResult{
		Placements: []v1alpha1.TemplatePlacement{placement, placementR},
		Targets:    map[string][]string{"region-1": {"zone-1"}, "region-2": {"zone-1"}},
	}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []string{"create", "delete"}, provider.calls)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement2}, provider.deleted)
	assert.Equal(t, []string{tpl.GetImageID()}, provider.images)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placementR}, tpl.TemplatePlacements())
}

func TestUntargetedRegionIsDeleted(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{result: reused(placement, placement2)}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placementR}, provider.deleted)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2}, tpl.TemplatePlacements())
}

func TestUnresolvedRegionKeepsPlacements(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{result: instancetemplate.CreateResult{
		Placements: []v1alpha1.TemplatePlacement{placement, placement2},
		Targets:    targets(placement, placement2),
		Unresolved: []string{"region-2"},
	}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.NotContains(t, provider.calls, "delete")
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2, placementR}, tpl.TemplatePlacements())
}

func TestNothingCreatedStillDeletesStale(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{
		result:    instancetemplate.CreateResult{Targets: targets(placement, placement2)},
		createErr: errors.New("downloads failed"),
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placementR}, provider.deleted)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2}, tpl.TemplatePlacements())
}

func TestFailedStaleDeleteStaysInStatus(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{
		result:    reused(placement),
		remaining: []v1alpha1.TemplatePlacement{placementR},
		deleteErr: errors.New("region-2: proxmox unavailable"),
	}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement2, placementR}, provider.deleted)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placementR}, tpl.TemplatePlacements())
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
	assert.Equal(t, 5*time.Minute, requeue)
}

func TestNoTargetsDeletesNothing(t *testing.T) {
	kubeClient := newKubeClient(t, staleTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl := get(t, kubeClient)
	assert.Equal(t, []string{"create"}, provider.calls)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2, placementR}, tpl.TemplatePlacements())
}

func TestFailedZoneKeepsPlacement(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result: instancetemplate.CreateResult{
			Placements: []v1alpha1.TemplatePlacement{placement},
			Targets:    targets(placement, placement2),
		},
		createErr: errors.New("zone-2: download failed"),
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.NotContains(t, provider.calls, "delete")
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2}, tpl.TemplatePlacements())
}

func TestInPlaceUpdateCoversFailedZone(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result: instancetemplate.CreateResult{
			Placements: []v1alpha1.TemplatePlacement{placement},
			Targets:    targets(placement, placement2),
		},
		createErr: errors.New("zone-2: download failed"),
		updateErr: errors.New("zone-2: template not found for update"),
	}

	_, err := reconcile(t, kubeClient, provider)
	require.Error(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2}, provider.updated)
	assert.Equal(t, "stale", tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])

	provider.result, provider.createErr, provider.updateErr = reused(placement, placement2), nil, nil
	provider.updated = nil

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement, placement2}, provider.updated)
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])
}

func TestVMTemplateOptionsAreRecorded(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})

	other := vmTemplate(v1alpha1.TemplatePlacement{Region: "region-1", Zone: "zone-1", TemplateID: 999}, "", true)
	other.Name = "ubuntu"
	provider := &fakeProvider{
		result:    reused(placement, placement2),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "2", false), vmTemplate(placement2, "2", false), other},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, &v1alpha1.VMTemplateInfo{Hugepages: "2"}, tpl.Status.Template)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent).IsTrue())
}

func TestVMTemplateWithoutHugepagesOrAffinityIsRecorded(t *testing.T) {
	tpl := readyTemplate()
	tpl.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: "1024", Affinity: true}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result:    reused(placement),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "", false)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, &v1alpha1.VMTemplateInfo{}, get(t, kubeClient).Status.Template)
}

func TestDisagreeingVMTemplatesRaiseCondition(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placementR})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result:    reused(placement, placementR),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "", false), vmTemplate(placementR, "", true)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)

	consistent := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent)
	assert.True(t, consistent.IsFalse())
	assert.Equal(t, "VMTemplatesDisagree", consistent.Reason)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
}

func TestVMTemplatesWithAffinityAgreeAcrossZones(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placement2})

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result:    reused(placement, placement2),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "1024", true), vmTemplate(placement2, "1024", true)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, &v1alpha1.VMTemplateInfo{Hugepages: "1024", Affinity: true}, tpl.Status.Template)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent).IsTrue())
}

func TestVMTemplateRefreshesWhenItsConfigChanges(t *testing.T) {
	kubeClient := newKubeClient(t, readyTemplate(), interceptor.Funcs{})
	provider := &fakeProvider{
		result:    reused(placement),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "", false)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	provider.inventory[0].Hugepages, provider.inventory[0].Affinity = "2", true

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, &v1alpha1.VMTemplateInfo{Hugepages: "2", Affinity: true}, get(t, kubeClient).Status.Template)
}

// A failed image replacement leaves the outdated VM templates, and Delete did
// not refresh the cache, so their hugepages and affinity are unknown.
func TestFailedImageReplacementLeavesVMTemplateUnknown(t *testing.T) {
	tpl := readyTemplate()
	tpl.Status.ImageID = "talos-1.raw"
	tpl.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: "2"}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		remaining: []v1alpha1.TemplatePlacement{placement},
		deleteErr: errors.New("proxmox unavailable"),
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "2", false)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent).IsUnknown())
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, tpl.TemplatePlacements())
}

// Create did not refresh the cache of a region it did not resolve, so the
// hugepages and affinity of the VM template there are unknown.
func TestUnresolvedRegionLeavesVMTemplateUnknown(t *testing.T) {
	tpl := readyTemplate()
	tpl.SetTemplatePlacements([]v1alpha1.TemplatePlacement{placement, placementR})
	tpl.Status.Template = &v1alpha1.VMTemplateInfo{Hugepages: "2"}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{
		result: instancetemplate.CreateResult{
			Placements: []v1alpha1.TemplatePlacement{placement},
			Targets:    targets(placement),
			Unresolved: []string{"region-2"},
		},
		inventory: []instancetemplate.InstanceTemplateInfo{vmTemplate(placement, "2", false), vmTemplate(placementR, "2", false)},
	}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Nil(t, tpl.Status.Template)

	consistent := tpl.StatusConditions().Get(v1alpha1.ConditionTemplateConsistent)
	assert.True(t, consistent.IsUnknown())
	assert.Contains(t, consistent.Message, "region-2/zone-1/2000")
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
}
