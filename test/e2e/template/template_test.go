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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// zoneSettleDelay is how long the test waits after a ProxmoxTemplate/
// ProxmoxUnmanagedTemplate first reports Ready before re-reading and
// checking status.zones. The controller sets Ready only after zones are
// populated (see pkg/controllers/.../status), but re-reads guard against
// a zone list that still changes shortly after - e.g. a fast-following
// reconcile correcting it.
const zoneSettleDelay = 5 * time.Second

// TestProxmoxTemplateReady creates a ProxmoxTemplate (docs/nodetemplateclass.md),
// waits for the controller to provision it - status' aggregate Ready
// condition reporting True - and confirms status.zones lists exactly the
// zones the region currently has available for the template's storage IDs.
//
// It then creates a ProxmoxUnmanagedTemplate that matches purely on the
// tags the ProxmoxTemplate applied to its Proxmox-side VM templates (no
// templateName), and confirms it becomes Ready and discovers the exact
// same set of zones - i.e. the unmanaged lookup finds precisely what the
// managed template provisioned.
//
// Requires E2E_PROXMOX_CONFIG (a cloud-config.yaml, see
// hack/proxmox-config.yaml): without direct Proxmox API access the expected
// zone count isn't knowable, so the test skips when it's unset.
func TestProxmoxTemplateReady(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	proxmox, err := framework.NewProxmoxClient(ctx, f.Config)

	cancel()
	require.NoError(err, "failed to build proxmox client from %s", f.Config.ProxmoxConfig)

	if proxmox == nil {
		t.Skip("E2E_PROXMOX_CONFIG not set - the expected zone count isn't knowable without direct Proxmox API access")
	}

	region := f.Config.Region
	if region == "" {
		regions := proxmox.Pool.List()
		require.NotEmpty(regions, "proxmox cloud config %s has no configured region", f.Config.ProxmoxConfig)

		region = regions[0]
	}

	expectedZones := proxmox.ExpectedTemplateZones(region, f.Config.TemplateStorageIDs)
	require.NotEmpty(expectedZones,
		"region %s has no zone whose storage (from %v) supports both the import and images content types - is E2E_PROXMOX_CONFIG/E2E_TEMPLATE_STORAGE_IDS pointed at the right cluster?",
		region, f.Config.TemplateStorageIDs)

	name := f.Name()

	// A tag unique to this test run, so the ProxmoxUnmanagedTemplate
	// created later matches only the template this test provisions -
	// never some unrelated template already present in the target
	// cluster.
	tags := []string{fmt.Sprintf("tag-%s", name)}

	tmpl := framework.NewProxmoxTemplate(framework.ProxmoxTemplateOptions{
		Name:           name,
		Region:         region,
		SourceImageURL: f.Config.TemplateSourceImageURL,
		ImageName:      f.Config.TemplateImageName,
		StorageIDs:     f.Config.TemplateStorageIDs,
		Bridge:         f.Config.TemplateBridge,
		Tags:           tags,
	})

	f.Logf("creating proxmoxtemplate %s (region=%s storageIDs=%v, expecting zones=%v)", name, region, f.Config.TemplateStorageIDs, expectedZones)

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

	done := f.Step("waiting for proxmoxtemplate %s to become ready", name)
	ctx, cancel = f.Context()
	ready, err := framework.WaitForProxmoxTemplateReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, ready)
	require.NoError(err, "proxmoxtemplate %s never became ready", name)

	f.Logf("proxmoxtemplate %s ready, waiting %s before checking status.zones", name, zoneSettleDelay)
	time.Sleep(zoneSettleDelay)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(ready), ready)

	cancel()

	f.DescribeOnFailure(err == nil, ready)
	require.NoError(err, "failed to refresh proxmoxtemplate %s after settle delay", name)

	f.Logf("proxmoxtemplate %s status.zones=%v", name, ready.Status.Zones)

	f.DescribeOnFailure(len(ready.Status.Zones) == len(expectedZones), ready)
	require.Len(ready.Status.Zones, len(expectedZones),
		"proxmoxtemplate %s reports %d zone(s) (%v), region %s currently has %d matching zone(s) (%v)",
		name, len(ready.Status.Zones), ready.Status.Zones, region, len(expectedZones), expectedZones)

	// ProxmoxUnmanagedTemplate: matches purely on the tags applied above -
	// no templateName - so it must discover exactly the VM template(s)
	// ProxmoxTemplate just provisioned.
	unmanagedName := f.Name()

	unmanagedTmpl := framework.NewProxmoxUnmanagedTemplate(framework.ProxmoxUnmanagedTemplateOptions{
		Name:   unmanagedName,
		Region: region,
		Tags:   tags,
	})

	f.Logf("creating proxmoxunmanagedtemplate %s (region=%s tags=%v)", unmanagedName, region, tags)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, unmanagedTmpl)

	cancel()
	require.NoError(err, "failed to create proxmoxunmanagedtemplate %s", unmanagedName)

	t.Cleanup(func() {
		f.Logf("deleting proxmoxunmanagedtemplate %s", unmanagedName)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, unmanagedTmpl)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			f.Logf("failed to delete proxmoxunmanagedtemplate %s: %v", unmanagedName, err)

			return
		}

		ctx, cancel = f.Context()
		err = framework.WaitForProxmoxUnmanagedTemplateGone(ctx, f.Client.Objects, unmanagedName, f.Config.Timeout)

		cancel()

		if err != nil {
			f.Logf("proxmoxunmanagedtemplate %s did not clean up: %v", unmanagedName, err)
		}
	})

	done = f.Step("waiting for proxmoxunmanagedtemplate %s to become ready", unmanagedName)
	ctx, cancel = f.Context()
	unmanagedReady, err := framework.WaitForProxmoxUnmanagedTemplateReady(ctx, f.Client.Objects, unmanagedName, f.Config.Timeout)

	cancel()
	done()

	f.DescribeOnFailure(err == nil, unmanagedReady)
	require.NoError(err, "proxmoxunmanagedtemplate %s never became ready", unmanagedName)

	f.Logf("proxmoxunmanagedtemplate %s ready, waiting %s before checking status.zones", unmanagedName, zoneSettleDelay)
	time.Sleep(zoneSettleDelay)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(unmanagedReady), unmanagedReady)

	cancel()

	f.DescribeOnFailure(err == nil, unmanagedReady)
	require.NoError(err, "failed to refresh proxmoxunmanagedtemplate %s after settle delay", unmanagedName)

	f.Logf("proxmoxunmanagedtemplate %s status.zones=%v", unmanagedName, unmanagedReady.Status.Zones)

	f.DescribeOnFailure(sameElements(ready.Status.Zones, unmanagedReady.Status.Zones), ready, unmanagedReady)
	require.ElementsMatch(ready.Status.Zones, unmanagedReady.Status.Zones,
		"proxmoxunmanagedtemplate %s zones %v do not match proxmoxtemplate %s zones %v - tags %v should have matched exactly the templates it created",
		unmanagedName, unmanagedReady.Status.Zones, name, ready.Status.Zones, tags)
}

// sameElements reports whether a and b contain the same elements
// regardless of order - the same semantics require.ElementsMatch checks -
// so DescribeOnFailure can decide whether that assertion is about to fail
// without duplicating testify's own comparison and failure reporting.
func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	counts := make(map[string]int, len(a))
	for _, v := range a {
		counts[v]++
	}

	for _, v := range b {
		counts[v]--
	}

	for _, c := range counts {
		if c != 0 {
			return false
		}
	}

	return true
}
