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

package template

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// TestProxmoxUnmanagedTemplateNoMatch creates a ProxmoxUnmanagedTemplate
// whose tags can't match any real Proxmox VM template, and confirms the
// controller reports this as an explicit not-ready state rather than the
// resource just sitting there with no observable signal as to why (see
// pkg/controllers/nodetemplateunmanagedclass/status/instancetemplate.go's
// "TemplatesNotFound" reason).
func TestProxmoxUnmanagedTemplateNoMatch(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	name := f.Name()
	tags := []string{"e2e-no-such-template-" + name}

	tmpl := framework.NewProxmoxUnmanagedTemplate(framework.ProxmoxUnmanagedTemplateOptions{
		Name: name,
		Tags: tags,
	})

	f.Logf("creating proxmoxunmanagedtemplate %s with tags %v that should match no real template", name, tags)

	ctx, cancel := f.Context()
	err := f.Client.Objects.Create(ctx, tmpl)

	cancel()
	require.NoError(err, "failed to create proxmoxunmanagedtemplate %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxunmanagedtemplate %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, tmpl)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete proxmoxunmanagedtemplate %s: %v", name, err)
		}
	})

	done := f.Step("waiting for proxmoxunmanagedtemplate %s to report not ready", name)
	ctx, cancel = f.Context()
	notReady, err := framework.WaitForProxmoxUnmanagedTemplateNotReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, notReady)
	require.NoError(err, "proxmoxunmanagedtemplate %s never reported a not-ready status", name)

	f.DescribeOnFailure(len(notReady.Status.Conditions) > 0, notReady)
	require.NotEmpty(notReady.Status.Conditions, "proxmoxunmanagedtemplate %s has no conditions at all - was it ever reconciled?", name)

	f.DescribeOnFailure(len(notReady.Status.Zones) == 0, notReady)
	require.Empty(notReady.Status.Zones, "proxmoxunmanagedtemplate %s reports zones %v despite matching no template", name, notReady.Status.Zones)
}

// TestProxmoxTemplateInvalidStorageID creates a ProxmoxTemplate whose
// storageIDs name a Proxmox storage that doesn't exist anywhere in the
// target cluster, and confirms the controller reports this as a clear
// not-ready state - status.imageID/zones must stay empty - instead of
// silently retrying forever with no observable signal (see
// pkg/providers/instancetemplate.DefaultProvider.Create, which logs and
// skips any region/zone it can't find matching storage for, leaving
// status.zones nil).
func TestProxmoxTemplateInvalidStorageID(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	name := f.Name()
	storageIDs := []string{"e2e-no-such-storage-" + name}

	tmpl := framework.NewProxmoxTemplate(framework.ProxmoxTemplateOptions{
		Name:           name,
		Region:         f.Config.Region,
		SourceImageURL: f.Config.TemplateSourceImageURL,
		ImageName:      f.Config.TemplateImageName,
		StorageIDs:     storageIDs,
		Bridge:         f.Config.TemplateBridge,
	})

	f.Logf("creating proxmoxtemplate %s with nonexistent storageIDs %v", name, storageIDs)

	ctx, cancel := f.Context()
	err := f.Client.Objects.Create(ctx, tmpl)

	cancel()
	require.NoError(err, "failed to create proxmoxtemplate %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxtemplate %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, tmpl)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete proxmoxtemplate %s: %v", name, err)
		}
	})

	done := f.Step("waiting for proxmoxtemplate %s to report not ready", name)
	ctx, cancel = f.Context()
	notReady, err := framework.WaitForProxmoxTemplateNotReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, notReady)
	require.NoError(err, "proxmoxtemplate %s never reported a not-ready status", name)

	f.DescribeOnFailure(len(notReady.Status.Conditions) > 0, notReady)
	require.NotEmpty(notReady.Status.Conditions, "proxmoxtemplate %s has no conditions at all - was it ever reconciled?", name)

	f.DescribeOnFailure(notReady.Status.ImageID == "", notReady)
	require.Empty(notReady.Status.ImageID, "proxmoxtemplate %s reports imageID %q despite having no valid storage", name, notReady.Status.ImageID)

	f.DescribeOnFailure(len(notReady.Status.Zones) == 0, notReady)
	require.Empty(notReady.Status.Zones, "proxmoxtemplate %s reports zones %v despite having no valid storage", name, notReady.Status.Zones)
}
