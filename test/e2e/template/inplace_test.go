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
	"fmt"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestProxmoxTemplateInPlaceUpdate creates a ProxmoxTemplate, waits for it
// to become Ready, then changes only in-place fields of its spec - tags,
// description and onBoot - and confirms the controller updates the existing
// Proxmox VM templates in place (see
// pkg/controllers/nodetemplateclass/lifecycle's updateInPlace):
//
//   - the in-place update hash annotation catches up with the new spec,
//   - status.imageID and status.zones stay exactly the same, i.e. no VM
//     template was rebuilt or moved to a new vmid,
//   - each VM template's config, read straight from the Proxmox API, carries
//     the new tags and description, and no longer has onboot - a managed
//     option the spec stopped rendering must be deleted, not left behind.
//
// Requires E2E_PROXMOX_CONFIG: the VM template configs are only observable
// through the Proxmox API, so the test skips when it's unset.
func TestProxmoxTemplateInPlaceUpdate(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	proxmox, err := framework.NewProxmoxClient(ctx, f.Config)

	cancel()
	require.NoError(err, "failed to build proxmox client from %s", f.Config.ProxmoxConfig)

	if proxmox == nil {
		t.Skip("E2E_PROXMOX_CONFIG not set - the VM template configs aren't observable without direct Proxmox API access")
	}

	region := f.Config.Region
	if region == "" {
		regions := proxmox.Pool.List()
		require.NotEmpty(regions, "proxmox cloud config %s has no configured region", f.Config.ProxmoxConfig)

		region = regions[0]
	}

	name := f.Name()

	initialTags := []string{fmt.Sprintf("tag-%s", name)}
	updatedTags := []string{fmt.Sprintf("tag-%s-updated", name), fmt.Sprintf("tag-%s-extra", name)}
	updatedDescription := fmt.Sprintf("e2e %s updated in place", name)

	tmpl := framework.NewProxmoxTemplate(framework.ProxmoxTemplateOptions{
		Name:           name,
		Region:         region,
		SourceImageURL: f.Config.TemplateSourceImageURL,
		ImageName:      f.Config.TemplateImageName,
		StorageIDs:     f.Config.TemplateStorageIDs,
		Bridge:         f.Config.TemplateBridge,
		Tags:           initialTags,
		Description:    fmt.Sprintf("e2e %s", name),
		OnBoot:         new(true),
	})

	f.Logf("creating proxmoxtemplate %s (region=%s storageIDs=%v tags=%v)", name, region, f.Config.TemplateStorageIDs, initialTags)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, tmpl)

	cancel()
	require.NoError(err, "failed to create proxmoxtemplate %s", name)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxtemplate %s", name)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, tmpl)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			f.Logf("failed to delete proxmoxtemplate %s: %v", name, err)

			return
		}

		done := f.Step("waiting for proxmoxtemplate %s to be deleted", name)
		ctx, cancel = f.Context()
		err = framework.WaitForProxmoxTemplateGone(ctx, f.Client.Objects, name, f.Config.Timeout)

		cancel()
		done()

		if err != nil {
			f.Logf("proxmoxtemplate %s did not clean up: %v", name, err)
		}
	})

	// The first pass adopts the in-place hash without an update, so waiting
	// for it also means the controller is done with the initial creation.
	done := f.Step("waiting for proxmoxtemplate %s to become ready", name)
	ctx, cancel = f.Context()
	ready, err := framework.WaitForProxmoxTemplateInPlaceUpdated(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, ready)
	require.NoError(err, "proxmoxtemplate %s never became ready", name)

	placements := ready.TemplatePlacements()

	f.DescribeOnFailure(len(placements) > 0, ready)
	require.NotEmpty(placements, "proxmoxtemplate %s is ready but has no VM template placements", name)

	f.Logf("proxmoxtemplate %s ready, imageID=%s placements=%v", name, ready.Status.ImageID, placements)

	for _, placement := range placements {
		cfg := templateConfig(t, f, proxmox, placement)

		require.NotNil(cfg.Tags, "vm template %s has no tags, want %v", placement, initialTags)
		require.ElementsMatch(initialTags, []string(*cfg.Tags), "vm template %s tags", placement)
		require.True(lo.FromPtr(cfg.OnBoot), "vm template %s has no onboot, want true", placement)
	}

	// Change only in-place fields; sourceImage, storageIDs, bios and tpm stay
	// put, so the template hash - and with it every VM template - must too.
	ctx, cancel = f.Context()
	patched := ready.DeepCopy()
	patched.Spec.Tags = updatedTags
	patched.Spec.Description = updatedDescription
	patched.Spec.OnBoot = nil
	err = f.Client.Objects.Patch(ctx, patched, client.MergeFrom(ready))

	cancel()
	require.NoError(err, "failed to patch proxmoxtemplate %s in-place fields", name)

	f.Logf("patched proxmoxtemplate %s: tags=%v description=%q onBoot=nil", name, updatedTags, updatedDescription)

	done = f.Step("waiting for proxmoxtemplate %s to finish its in-place update", name)
	ctx, cancel = f.Context()
	updated, err := framework.WaitForProxmoxTemplateInPlaceUpdated(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, updated)
	require.NoError(err, "proxmoxtemplate %s never finished its in-place update", name)

	f.DescribeOnFailure(updated.Status.ImageID == ready.Status.ImageID, ready, updated)
	require.Equal(ready.Status.ImageID, updated.Status.ImageID, "proxmoxtemplate %s imageID changed on an in-place update", name)

	f.DescribeOnFailure(sameElements(ready.Status.Zones, updated.Status.Zones), ready, updated)
	require.ElementsMatch(ready.Status.Zones, updated.Status.Zones,
		"proxmoxtemplate %s zones changed on an in-place update - the VM templates were rebuilt instead of updated", name)

	for _, placement := range updated.TemplatePlacements() {
		cfg := templateConfig(t, f, proxmox, placement)

		f.Logf("vm template %s: tags=%v description=%q onboot=%v", placement, lo.FromPtr(cfg.Tags), cfg.Description, lo.FromPtr(cfg.OnBoot))

		require.NotNil(cfg.Tags, "vm template %s has no tags, want %v", placement, updatedTags)
		require.ElementsMatch(updatedTags, []string(*cfg.Tags), "vm template %s tags were not updated in place", placement)

		require.True(strings.HasPrefix(cfg.Description, updatedDescription),
			"vm template %s description %q does not start with the updated spec.description %q", placement, cfg.Description, updatedDescription)

		require.Nil(cfg.OnBoot, "vm template %s still has onboot after spec.onBoot was removed", placement)
	}
}

// templateConfig reads the config of the VM template at placement from the
// Proxmox API, failing the test if it can't.
func templateConfig(t *testing.T, f *framework.Framework, proxmox *framework.ProxmoxClient, placement v1alpha1.TemplatePlacement) *qemu.Config {
	t.Helper()

	ctx, cancel := f.Context()
	defer cancel()

	cfg, err := proxmox.TemplateConfig(ctx, placement)
	require.NoError(t, err, "failed to read vm template %s", placement)

	return cfg
}
