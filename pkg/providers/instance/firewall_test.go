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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"

	corev1 "k8s.io/api/core/v1"
)

func TestUpdateFirewallRules(t *testing.T) {
	f := newLaunchFixture(t, launchSetup{metadataType: "none"})

	node, err := f.create(f.ctx, "node-1")
	require.NoError(t, err)

	nodeClaim := f.nodeClaim("node-1")
	nodeClaim.Labels = node.Labels
	nodeClaim.Status.ProviderID = node.Spec.ProviderID

	vmid, _, err := provider.ParseProviderID(node.Spec.ProviderID)
	require.NoError(t, err)

	groups := func() []string {
		t.Helper()

		rules, err := f.client.Nodes(node.Labels[corev1.LabelTopologyZone]).Qemu().Firewall().Rules(vmid).List(f.ctx)
		require.NoError(t, err)

		out := make([]string, len(rules))
		for i, r := range rules {
			out[i] = r.Action + "@" + r.IFace
		}

		return out
	}

	tests := []struct {
		name   string
		groups []v1alpha1.SecurityGroups
		expect []string
	}{
		{
			name:   "grow",
			groups: []v1alpha1.SecurityGroups{{Name: "sg-a"}, {Name: "sg-web", Interface: "net0"}, {Name: "sg-ssh"}, {Name: "sg-b"}},
			expect: []string{"sg-a@", "sg-web@net0", "sg-ssh@", "sg-b@"},
		},
		{
			name:   "shrink",
			groups: []v1alpha1.SecurityGroups{{Name: "sg-b"}},
			expect: []string{"sg-b@"},
		},
		{
			name:   "clear",
			groups: nil,
			expect: []string{},
		},
	}

	for _, tt := range tests {
		f.nodeClass.Spec.SecurityGroups = tt.groups

		require.NoError(t, f.provider.UpdateFirewallRules(f.ctx, nodeClaim, f.nodeClass), tt.name)
		assert.Equal(t, tt.expect, groups(), tt.name)
	}
}
