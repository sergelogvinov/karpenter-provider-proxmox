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

package instance_test

import (
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// day2 is one day-2 operation on a launched VM.
type day2 struct {
	name string
	call func(f *launchFixture, nodeClaim *karpv1.NodeClaim) error
}

var day2Operations = []day2{
	{name: "UpdateTags", call: func(f *launchFixture, nc *karpv1.NodeClaim) error {
		return f.provider.UpdateTags(f.ctx, nc, f.nodeClass)
	}},
	{name: "UpdateFirewallRules", call: func(f *launchFixture, nc *karpv1.NodeClaim) error {
		return f.provider.UpdateFirewallRules(f.ctx, nc, f.nodeClass)
	}},
	{name: "UpdatePoolMembership", call: func(f *launchFixture, nc *karpv1.NodeClaim) error {
		return f.provider.UpdatePoolMembership(f.ctx, nc, f.nodeClass)
	}},
	{name: "DetachCloudInit", call: func(f *launchFixture, nc *karpv1.NodeClaim) error {
		return f.provider.DetachCloudInit(f.ctx, nc)
	}},
	{name: "Delete", call: func(f *launchFixture, nc *karpv1.NodeClaim) error {
		return f.provider.Delete(f.ctx, nc)
	}},
}

// launched launches node-1 and returns its NodeClaim as Karpenter records
// it: labeled like the Node, with its provider ID.
func (f *launchFixture) launched() (*karpv1.NodeClaim, *corev1.Node) {
	f.t.Helper()

	node, err := f.create(f.ctx, "node-1")
	require.NoError(f.t, err)

	nodeClaim := f.nodeClaim("node-1")
	nodeClaim.Labels = maps.Clone(node.Labels)
	nodeClaim.Annotations = map[string]string{v1alpha1.AnnotationProxmoxNodeClassPool: "karpenter"}
	nodeClaim.Status.ProviderID = node.Spec.ProviderID

	return nodeClaim, node
}

// TestDay2FollowsMigratedVM labels the NodeClaim with a zone the VM no
// longer runs in, as after a migration, and checks every day-2 operation
// still acts on the VM where it runs.
func TestDay2FollowsMigratedVM(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{zones: []string{"pve1", "pve2"}})

	nodeClaim, node := f.launched()
	require.Equal(t, "pve1", node.Labels[corev1.LabelTopologyZone])

	nodeClaim.Labels[corev1.LabelTopologyZone] = "pve2"

	f.nodeClass.Spec.Tags = []string{"migrated"}
	f.nodeClass.Spec.SecurityGroups = []v1alpha1.SecurityGroups{{Name: "sg-migrated"}}
	f.nodeClass.Spec.ResourcePool = ""

	for _, op := range day2Operations[:4] {
		require.NoError(t, op.call(f, nodeClaim), op.name)
	}

	vm := f.client.Nodes("pve1").Qemu()

	cfg, err := vm.Config(f.ctx, firstVMID, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"migrated"}, []string(*cfg.Tags), "UpdateTags")
	assert.Equal(t, "none", cfg.IDE[2].File, "DetachCloudInit ejects the ISO")
	assert.Empty(t, f.isos(), "DetachCloudInit removes the ISO")

	rules, err := vm.Firewall().Rules(firstVMID).List(f.ctx)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "sg-migrated", rules[0].Action, "UpdateFirewallRules")

	pool, err := f.client.Pools().Get(f.ctx, "karpenter")
	require.NoError(t, err)
	assert.Empty(t, pool.Members, "UpdatePoolMembership")

	require.NoError(t, day2Operations[4].call(f, nodeClaim))
	assert.Empty(t, f.guests(), "Delete")
}

// TestDay2RegionFromLabel gives the NodeClaim a provider ID without a
// region, and checks every day-2 operation takes it from the region label.
func TestDay2RegionFromLabel(t *testing.T) {
	t.Parallel()

	for _, op := range day2Operations {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			f := newLaunchFixture(t, launchSetup{})

			nodeClaim, _ := f.launched()
			nodeClaim.Status.ProviderID = fmt.Sprintf("proxmox:///%d", firstVMID)

			require.NoError(t, op.call(f, nodeClaim))
		})
	}
}

// TestDay2VMGone deletes the VM behind the NodeClaim, and checks every
// day-2 operation reports the NodeClaim not found.
func TestDay2VMGone(t *testing.T) {
	t.Parallel()

	for _, op := range day2Operations {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			f := newLaunchFixture(t, launchSetup{})

			nodeClaim, _ := f.launched()

			vm := f.client.Nodes("pve1").Qemu()

			stop, err := vm.Stop(f.ctx, firstVMID, nil)
			require.NoError(t, err)
			require.NoError(t, stop.Wait(f.ctx, nil))

			del, err := vm.Delete(f.ctx, firstVMID, nil)
			require.NoError(t, err)
			require.NoError(t, del.Wait(f.ctx, nil))

			err = op.call(f, nodeClaim)
			assert.True(t, cloudprovider.IsNodeClaimNotFoundError(err), "got %v", err)
		})
	}
}
