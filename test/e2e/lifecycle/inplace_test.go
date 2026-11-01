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

package lifecycle

import (
	"fmt"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestNodeClaimInPlaceUpdate changes the tags of a ProxmoxNodeClass whose
// VMs are already running, and confirms the controller updates those VMs
// in place (see pkg/controllers/nodeclaim/inplaceupdate) instead of
// replacing them:
//
//   - the NodeClaims' in-place update hash annotation catches up with the
//     new spec,
//   - every NodeClaim keeps its providerID, i.e. no VM was replaced - tags
//     are hash:"ignore" in the NodeClass spec, so they must not cause drift,
//   - each VM's config, read straight from the Proxmox API, carries the new
//     tags.
//
// The suite never edits E2E_NODE_CLASS itself, which other nodes may use:
// it creates its own ProxmoxNodeClass with a copy of that one's spec, and
// changes only the copy.
//
// Requires E2E_PROXMOX_CONFIG: the VM configs are only observable through
// the Proxmox API, so the test skips - before launching any VM - when it's
// unset.
func TestNodeClaimInPlaceUpdate(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	proxmox, err := framework.NewProxmoxClient(ctx, f.Config)

	cancel()
	require.NoError(err, "failed to build proxmox client from %s", f.Config.ProxmoxConfig)

	if proxmox == nil {
		t.Skip("E2E_PROXMOX_CONFIG not set - the VM configs aren't observable without direct Proxmox API access")
	}

	ctx, cancel = f.Context()
	_, err = framework.WaitForProxmoxUnmanagedTemplateReady(ctx, f.Client.Objects, f.Config.UnmanagedTemplateName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxunmanagedtemplate %s (E2E_UNMANAGED_TEMPLATE) is not ready - is it already deployed in the target cluster?", f.Config.UnmanagedTemplateName)

	ctx, cancel = f.Context()
	baseNodeClass, err := framework.WaitForProxmoxNodeClassReady(ctx, f.Client.Objects, f.Config.NodeClassName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxnodeclass %s (E2E_NODE_CLASS) is not ready - is it already deployed in the target cluster?", f.Config.NodeClassName)

	name := f.Name()

	// Proxmox lowercases tags by default, and f.Name() is lowercase already,
	// so these compare as-is against what the Proxmox API returns.
	initialTags := []string{fmt.Sprintf("tag-%s", name)}
	updatedTags := []string{fmt.Sprintf("tag-%s-updated", name), fmt.Sprintf("tag-%s-extra", name)}

	nodeClass := &v1alpha1.ProxmoxNodeClass{
		Name: name,
		Spec: *baseNodeClass.Spec.DeepCopy(),
	}
	nodeClass.Spec.Tags = initialTags

	f.Logf("creating proxmoxnodeclass %s (copy of %s, tags=%v)", name, f.Config.NodeClassName, initialTags)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, nodeClass)

	cancel()
	require.NoError(err, "failed to create proxmoxnodeclass %s", name)

	// Registered before the NodePool's cleanup so it runs after it: the
	// NodeClass must outlive every node the NodePool provisioned from it.
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

	nodePool := framework.NewNodePool(framework.NodePoolOptions{
		Name:             name,
		NodeClassName:    name,
		InstanceTypes:    f.Config.InstanceTypes,
		InstanceFamilies: f.Config.InstanceFamilies,
	})

	f.Logf("creating nodepool %s (nodeClass=%s, instanceTypes=%v, instanceFamilies=%v)", name, name, f.Config.InstanceTypes, f.Config.InstanceFamilies)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, nodePool)

	cancel()
	require.NoError(err, "failed to create nodepool %s", name)

	t.Cleanup(func() {
		done := f.Step("deleting nodepool %s (draining every node it provisioned)", name)
		ctx, cancel := f.ContextTimeout(f.Config.NodeTimeout)
		err := framework.WaitForNodePoolGone(ctx, f.Client.Objects, name, f.Config.NodeTimeout)

		cancel()
		done()

		if err != nil {
			t.Errorf("nodepool %s did not clean up: %v", name, err)
		}
	})

	done = f.Step("waiting for nodepool %s to become ready", name)
	ctx, cancel = f.Context()
	_, err = framework.WaitForNodePoolReady(ctx, f.Client.Objects, name, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "nodepool %s never became ready", name)

	sts := framework.NewWorkloadStatefulSet(framework.StatefulSetOptions{
		Name:         name,
		Namespace:    f.Config.Namespace,
		Replicas:     1,
		NodeSelector: map[string]string{framework.NodePoolTestLabelKey: name},
	})

	f.Logf("creating statefulset %s/%s (replicas=1)", f.Config.Namespace, name)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, sts)

	cancel()
	require.NoError(err, "failed to create statefulset %s/%s", f.Config.Namespace, name)

	t.Cleanup(func() {
		done := f.Step("deleting statefulset %s/%s (waiting for its pods to terminate)", f.Config.Namespace, name)
		ctx, cancel := f.Context()
		err := framework.WaitForStatefulSetGone(ctx, f.Client.Objects, f.Config.Namespace, name, f.Config.Timeout)

		cancel()
		done()

		if err != nil {
			t.Errorf("statefulset %s/%s did not clean up: %v", f.Config.Namespace, name, err)
		}
	})

	done = f.Step("waiting for statefulset %s/%s to reach 1 ready replica (a new Proxmox VM must boot and join)", f.Config.Namespace, name)
	ctx, cancel = f.ContextTimeout(f.Config.NodeTimeout)
	_, err = framework.WaitForStatefulSetReplicasReady(ctx, f.Client.Objects, f.Config.Namespace, name, 1, f.Config.NodeTimeout)

	cancel()
	done()
	require.NoError(err, "statefulset %s/%s never reached 1 ready replica", f.Config.Namespace, name)

	done = f.Step("waiting for the nodeclaims of nodepool %s to match proxmoxnodeclass %s", name, name)
	ctx, cancel = f.Context()
	launched, err := framework.WaitForNodeClaimsInPlaceUpdated(ctx, f.Client.Objects, name, name, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "nodeclaims of nodepool %s never matched proxmoxnodeclass %s", name, name)

	providerIDs := lo.Map(launched, func(nc karpv1.NodeClaim, _ int) string { return nc.Status.ProviderID })

	f.Logf("nodepool %s launched %d nodeclaim(s): %v", name, len(launched), providerIDs)

	for _, providerID := range providerIDs {
		tags := vmTags(t, f, proxmox, providerID)
		require.ElementsMatch(initialTags, tags, "vm %s tags", providerID)
	}

	ctx, cancel = f.Context()
	err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(nodeClass), nodeClass)

	cancel()
	require.NoError(err, "failed to refresh proxmoxnodeclass %s before changing its tags", name)

	patched := nodeClass.DeepCopy()
	patched.Spec.Tags = updatedTags

	ctx, cancel = f.Context()
	err = f.Client.Objects.Patch(ctx, patched, client.MergeFrom(nodeClass))

	cancel()
	require.NoError(err, "failed to patch proxmoxnodeclass %s tags", name)

	f.Logf("patched proxmoxnodeclass %s: tags=%v", name, updatedTags)

	done = f.Step("waiting for the nodeclaims of nodepool %s to finish their in-place update", name)
	ctx, cancel = f.Context()
	updated, err := framework.WaitForNodeClaimsInPlaceUpdated(ctx, f.Client.Objects, name, name, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "nodeclaims of nodepool %s never finished their in-place update", name)

	require.ElementsMatch(providerIDs, lo.Map(updated, func(nc karpv1.NodeClaim, _ int) string { return nc.Status.ProviderID }),
		"nodepool %s nodeclaims changed on a tags-only update - the VMs were replaced instead of updated in place", name)

	for _, providerID := range providerIDs {
		tags := vmTags(t, f, proxmox, providerID)

		f.Logf("vm %s: tags=%v", providerID, tags)

		require.ElementsMatch(updatedTags, tags, "vm %s tags were not updated in place", providerID)
	}
}

// vmTags reads the tags of the VM behind providerID from the Proxmox API,
// lowercased the way Proxmox stores them by default, failing the test if it
// can't.
func vmTags(t *testing.T, f *framework.Framework, proxmox *framework.ProxmoxClient, providerID string) []string {
	t.Helper()

	ctx, cancel := f.Context()
	defer cancel()

	cfg, err := proxmox.VMConfig(ctx, providerID)
	require.NoError(t, err, "failed to read vm %s", providerID)

	return lo.Map(lo.FromPtr(cfg.Tags), func(tag string, _ int) string { return strings.ToLower(tag) })
}
