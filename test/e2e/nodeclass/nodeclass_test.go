//go:build e2e

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

package nodeclass

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fieldSettleDelay is how long the test waits after an in-place spec
// update before re-reading the ProxmoxNodeClass and checking it's still
// Ready. None of the fields mutated below feed into the Ready computation
// (see pkg/controllers/nodeclass/status), so this is a smoke check that
// the update didn't unexpectedly break anything, not a real convergence
// wait.
const fieldSettleDelay = 5 * time.Second

// TestProxmoxNodeClassInPlaceUpdates creates a ProxmoxNodeClass referencing
// an existing ProxmoxUnmanagedTemplate (E2E_UNMANAGED_TEMPLATE - the suite
// never creates it, see docs/nodeclass.md), waits for it to become Ready,
// then exercises every in-place-updatable field: bootDevice.size, tags,
// securityGroups and resourcePool must not disturb readiness, but
// switching metadataOptions.type to "cdrom" while its templatesRef secret
// doesn't exist yet must flip the resource unready until that secret is
// created.
func TestProxmoxNodeClassInPlaceUpdates(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	_, err := framework.WaitForProxmoxUnmanagedTemplateReady(ctx, f.Client.Objects, f.Config.UnmanagedTemplateName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxunmanagedtemplate %s (E2E_UNMANAGED_TEMPLATE) is not ready - is it already deployed in the target cluster?", f.Config.UnmanagedTemplateName)

	name := f.Name()

	nodeClass := framework.NewProxmoxNodeClass(framework.ProxmoxNodeClassOptions{
		Name:                  name,
		UnmanagedTemplateName: f.Config.UnmanagedTemplateName,
		BootDeviceSize:        "20Gi",
		BootDeviceStorage:     f.Config.NodeClassBootStorage,
	})

	f.Logf("creating proxmoxnodeclass %s (instanceTemplateRef=%s)", name, f.Config.UnmanagedTemplateName)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, nodeClass)

	cancel()
	require.NoError(err, "failed to create proxmoxnodeclass %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxnodeclass %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, nodeClass)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete proxmoxnodeclass %s: %v", name, err)
		}
	})

	done := f.Step("waiting for proxmoxnodeclass %s to become ready", name)
	ctx, cancel = f.Context()
	_, err = framework.WaitForProxmoxNodeClassReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "proxmoxnodeclass %s never became ready", name)

	// Every one of these fields supports in-place update and, unlike
	// instanceTemplateRef, metadataOptions and bootDevice.storage (see
	// pkg/controllers/nodeclass/status/instancetemplate.go, which
	// intersects the template's zones with wherever bootDevice.storage
	// exists), none of them feed into the Ready computation at all - so
	// each change here must leave the resource Ready. bootDevice.size in
	// particular is exercised here specifically because it's inert:
	// bootDevice.storage is covered by
	// TestProxmoxNodeClassInvalidBootDeviceStorage instead.
	mutations := []struct {
		desc  string
		apply func(nc *v1alpha1.ProxmoxNodeClass)
	}{
		{
			desc: "bootDevice.size",
			apply: func(nc *v1alpha1.ProxmoxNodeClass) {
				size := resource.MustParse("40Gi")
				nc.Spec.BootDevice.Size = &size
			},
		},
		{
			desc: "tags",
			apply: func(nc *v1alpha1.ProxmoxNodeClass) {
				nc.Spec.Tags = []string{"e2e", name}
			},
		},
		{
			desc: "securityGroups",
			apply: func(nc *v1alpha1.ProxmoxNodeClass) {
				nc.Spec.SecurityGroups = []v1alpha1.SecurityGroups{
					{Name: "kubernetes", Interface: "net0"},
				}
			},
		},
		{
			desc: "resourcePool",
			apply: func(nc *v1alpha1.ProxmoxNodeClass) {
				nc.Spec.ResourcePool = "e2e-" + name
			},
		},
	}

	for _, m := range mutations {
		ctx, cancel := f.Context()
		err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(nodeClass), nodeClass)

		cancel()
		require.NoError(err, "failed to refresh proxmoxnodeclass %s before changing %s", name, m.desc)

		m.apply(nodeClass)

		f.Logf("updating proxmoxnodeclass %s: %s", name, m.desc)

		ctx, cancel = f.Context()
		err = f.Client.Objects.Update(ctx, nodeClass)

		cancel()
		require.NoError(err, "failed to update proxmoxnodeclass %s (%s)", name, m.desc)

		f.Logf("waiting %s before checking proxmoxnodeclass %s is still ready after changing %s", fieldSettleDelay, name, m.desc)
		time.Sleep(fieldSettleDelay)

		ctx, cancel = f.Context()
		_, err = framework.WaitForProxmoxNodeClassReady(ctx, f.Client.Objects, name, f.Config.Timeout)

		cancel()
		require.NoError(err, "proxmoxnodeclass %s is not ready after changing %s", name, m.desc)
	}

	// metadataOptions: none -> cdrom, pointed at a secret that doesn't
	// exist yet - this must flip the resource unready (see
	// pkg/controllers/nodeclass/status/metadataoptions.go).
	secretName := name + "-metadata"

	ctx, cancel = f.Context()
	err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(nodeClass), nodeClass)

	cancel()
	require.NoError(err, "failed to refresh proxmoxnodeclass %s before changing metadataOptions", name)

	nodeClass.Spec.MetadataOptions = &v1alpha1.MetadataOptions{
		Type: "cdrom",
		TemplatesRef: &corev1.SecretReference{
			Name:      secretName,
			Namespace: f.Config.Namespace,
		},
	}

	f.Logf("updating proxmoxnodeclass %s: metadataOptions.type -> cdrom (secret %s/%s does not exist yet)", name, f.Config.Namespace, secretName)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Update(ctx, nodeClass)

	cancel()
	require.NoError(err, "failed to update proxmoxnodeclass %s (metadataOptions)", name)

	done = f.Step("waiting for proxmoxnodeclass %s to become unready (metadata secret %s/%s missing)", name, f.Config.Namespace, secretName)
	ctx, cancel = f.Context()
	notReadyAfterMetadataChange, err := framework.WaitForProxmoxNodeClassNotReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, notReadyAfterMetadataChange)
	require.NoError(err, "proxmoxnodeclass %s did not become unready after metadataOptions pointed at a missing secret", name)

	secret := &corev1.Secret{
		Name:      secretName,
		Namespace: f.Config.Namespace,
		StringData: map[string]string{
			"user-data": "#cloud-config\n",
		},
	}

	f.Logf("creating secret %s/%s referenced by proxmoxnodeclass %s", f.Config.Namespace, secretName, name)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, secret)

	cancel()
	require.NoError(err, "failed to create secret %s/%s", f.Config.Namespace, secretName)

	t.Cleanup(func() {
		f.Logf("deleting secret %s/%s", f.Config.Namespace, secretName)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, secret)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete secret %s/%s: %v", f.Config.Namespace, secretName, err)
		}
	})

	done = f.Step("waiting for proxmoxnodeclass %s to become ready again", name)
	ctx, cancel = f.Context()
	_, err = framework.WaitForProxmoxNodeClassReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "proxmoxnodeclass %s never became ready again after creating secret %s/%s", name, f.Config.Namespace, secretName)
}

// TestProxmoxNodeClassInvalidBootDeviceStorage creates a ProxmoxNodeClass
// whose bootDevice.storage names a Proxmox storage that doesn't exist
// anywhere in the target cluster, and confirms the controller reports this
// as an explicit not-ready state rather than the resource just sitting
// there with no observable signal as to why: bootDevice.storage feeds
// directly into the available-zones intersection
// pkg/controllers/nodeclass/status/instancetemplate.go computes, so no
// matching storage means no zone can ever satisfy the NodeClass, however
// valid instanceTemplateRef is.
func TestProxmoxNodeClassInvalidBootDeviceStorage(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	_, err := framework.WaitForProxmoxUnmanagedTemplateReady(ctx, f.Client.Objects, f.Config.UnmanagedTemplateName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxunmanagedtemplate %s (E2E_UNMANAGED_TEMPLATE) is not ready - is it already deployed in the target cluster?", f.Config.UnmanagedTemplateName)

	name := f.Name()
	badStorage := "e2e-no-such-storage-" + name

	nodeClass := framework.NewProxmoxNodeClass(framework.ProxmoxNodeClassOptions{
		Name:                  name,
		UnmanagedTemplateName: f.Config.UnmanagedTemplateName,
		BootDeviceSize:        "20Gi",
		BootDeviceStorage:     badStorage,
	})

	f.Logf("creating proxmoxnodeclass %s with nonexistent bootDevice.storage %s", name, badStorage)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, nodeClass)

	cancel()
	require.NoError(err, "failed to create proxmoxnodeclass %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxnodeclass %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, nodeClass)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete proxmoxnodeclass %s: %v", name, err)
		}
	})

	done := f.Step("waiting for proxmoxnodeclass %s to report not ready", name)
	ctx, cancel = f.Context()
	notReady, err := framework.WaitForProxmoxNodeClassNotReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, notReady)
	require.NoError(err, "proxmoxnodeclass %s never reported a not-ready status", name)

	f.DescribeOnFailure(len(notReady.Status.Conditions) > 0, notReady)
	require.NotEmpty(notReady.Status.Conditions, "proxmoxnodeclass %s has no conditions at all - was it ever reconciled?", name)

	f.DescribeOnFailure(len(notReady.Status.SelectedZones) == 0, notReady)
	require.Empty(notReady.Status.SelectedZones,
		"proxmoxnodeclass %s reports selectedZones %v despite bootDevice.storage %s not existing anywhere", name, notReady.Status.SelectedZones, badStorage)
}

// TestProxmoxNodeClassInvalidInstanceTemplateRef creates a ProxmoxNodeClass
// whose instanceTemplateRef.name doesn't match any ProxmoxUnmanagedTemplate
// in the cluster, and confirms the controller reports this as an explicit
// not-ready state (see resolveProxmoxTemplateFromNodeClass in
// pkg/controllers/nodeclass/status/instancetemplate.go, which surfaces the
// lookup's NotFound as reason "TemplatesNotFound").
func TestProxmoxNodeClassInvalidInstanceTemplateRef(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	name := f.Name()
	badRefName := "e2e-no-such-template-" + name

	nodeClass := framework.NewProxmoxNodeClass(framework.ProxmoxNodeClassOptions{
		Name:                  name,
		UnmanagedTemplateName: badRefName,
		BootDeviceSize:        "20Gi",
		BootDeviceStorage:     f.Config.NodeClassBootStorage,
	})

	f.Logf("creating proxmoxnodeclass %s with nonexistent instanceTemplateRef.name %s", name, badRefName)

	ctx, cancel := f.Context()
	err := f.Client.Objects.Create(ctx, nodeClass)

	cancel()
	require.NoError(err, "failed to create proxmoxnodeclass %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxnodeclass %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, nodeClass)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete proxmoxnodeclass %s: %v", name, err)
		}
	})

	done := f.Step("waiting for proxmoxnodeclass %s to report not ready", name)
	ctx, cancel = f.Context()
	notReady, err := framework.WaitForProxmoxNodeClassNotReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, notReady)
	require.NoError(err, "proxmoxnodeclass %s never reported a not-ready status", name)

	f.DescribeOnFailure(len(notReady.Status.Conditions) > 0, notReady)
	require.NotEmpty(notReady.Status.Conditions, "proxmoxnodeclass %s has no conditions at all - was it ever reconciled?", name)

	f.DescribeOnFailure(len(notReady.Status.SelectedZones) == 0, notReady)
	require.Empty(notReady.Status.SelectedZones,
		"proxmoxnodeclass %s reports selectedZones %v despite instanceTemplateRef.name %s not existing", name, notReady.Status.SelectedZones, badRefName)
}
