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

var placement = v1alpha1.TemplatePlacement{Region: "region-1", Zone: "zone-1", TemplateID: 1000}

// fakeProvider mimics DefaultProvider's status writes: Create records the
// image and its placements, Delete clears the placements.
type fakeProvider struct {
	instancetemplate.Provider

	installed []v1alpha1.TemplatePlacement
	deleteErr error
	onCreate  func()

	calls []string
}

func (p *fakeProvider) Create(_ context.Context, templateClass *v1alpha1.ProxmoxTemplate) error {
	p.calls = append(p.calls, "create")

	if p.onCreate != nil {
		p.onCreate()
	}

	if len(p.installed) > 0 {
		templateClass.Status.ImageID = templateClass.GetImageID()
		templateClass.SetTemplatePlacements(p.installed)
	}

	return nil
}

func (p *fakeProvider) Delete(_ context.Context, templateClass *v1alpha1.ProxmoxTemplate) error {
	p.calls = append(p.calls, "delete")

	if p.deleteErr != nil {
		return p.deleteErr
	}

	templateClass.SetTemplatePlacements(nil)

	return nil
}

func (p *fakeProvider) Update(_ context.Context, _ *v1alpha1.ProxmoxTemplate) error {
	p.calls = append(p.calls, "update")

	return nil
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
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}

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
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"delete", "create"}, provider.calls)
	assert.Equal(t, tpl.GetImageID(), tpl.Status.ImageID)
	assert.True(t, tpl.StatusConditions().Get(v1alpha1.ConditionTemplateReady).IsTrue())
}

func TestInPlaceChangeUpdatesOnce(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] = "stale"

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}

	_, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	tpl = get(t, kubeClient)
	assert.Equal(t, []string{"create", "update"}, provider.calls)
	assert.Equal(t, tpl.InPlaceHash(), tpl.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash])

	_, err = reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Equal(t, []string{"create", "update", "create"}, provider.calls)
}

func TestMissingInPlaceHashIsAdoptedWithoutUpdate(t *testing.T) {
	tpl := readyTemplate()
	tpl.Annotations = nil

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}

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
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}

	requeue, err := reconcile(t, kubeClient, provider)
	require.NoError(t, err)

	assert.Empty(t, provider.calls)
	assert.Equal(t, 10*time.Second, requeue)
}

func TestDeletionDuringCreateKeepsPlacements(t *testing.T) {
	tpl := newTemplate()
	tpl.Finalizers = []string{v1alpha1.TerminationFinalizer}

	kubeClient := newKubeClient(t, tpl, interceptor.Funcs{})
	provider := &fakeProvider{installed: []v1alpha1.TemplatePlacement{placement}}
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
