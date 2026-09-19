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

package instance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu/firewall"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

func TestBuildFirewallRules(t *testing.T) {
	t.Parallel()

	assert.Empty(t, buildFirewallRules(nil))

	rules := buildFirewallRules([]v1alpha1.SecurityGroups{
		{Name: "web", Interface: "net0"},
		{Name: "ssh", Interface: "net1"},
	})

	assert.Equal(t, []firewall.Rule{
		{Pos: 0, Enable: 1, Type: firewall.RuleTypeGroup, Action: "web", IFace: "net0"},
		{Pos: 1, Enable: 1, Type: firewall.RuleTypeGroup, Action: "ssh", IFace: "net1"},
	}, rules)
}

func TestFirewallRuleOptions(t *testing.T) {
	t.Parallel()

	opts := firewallRuleOptions(firewall.Rule{
		Pos:    0,
		Enable: 1,
		Type:   firewall.RuleTypeGroup,
		Action: "web",
		IFace:  "net0",
	})

	assert.Equal(t, firewall.RuleTypeGroup, opts.Type)
	assert.Equal(t, "web", opts.Action)
	assert.NotNil(t, opts.Enable)
	assert.True(t, *opts.Enable)
	assert.NotNil(t, opts.IFace)
	assert.Equal(t, "net0", *opts.IFace)
}

func TestFirewallRuleChanged(t *testing.T) {
	t.Parallel()

	base := firewall.Rule{Pos: 0, Enable: 1, Type: firewall.RuleTypeGroup, Action: "web", IFace: "net0"}

	// Server-managed fields that buildFirewallRules never sets (and
	// firewallRuleOptions never sends) must not make an unchanged rule
	// look changed, or every reconcile would rewrite every rule.
	serverManaged := base
	serverManaged.Digest = "abc123"
	serverManaged.IPVersion = 4
	assert.False(t, firewallRuleChanged(serverManaged, base))

	testCases := []struct {
		name string
		new  firewall.Rule
	}{
		{name: "pos", new: firewall.Rule{Pos: 1, Enable: 1, Type: firewall.RuleTypeGroup, Action: "web", IFace: "net0"}},
		{name: "type", new: firewall.Rule{Pos: 0, Enable: 1, Type: firewall.RuleTypeIn, Action: "web", IFace: "net0"}},
		{name: "action", new: firewall.Rule{Pos: 0, Enable: 1, Type: firewall.RuleTypeGroup, Action: "ssh", IFace: "net0"}},
		{name: "enable", new: firewall.Rule{Pos: 0, Enable: 0, Type: firewall.RuleTypeGroup, Action: "web", IFace: "net0"}},
		{name: "iface", new: firewall.Rule{Pos: 0, Enable: 1, Type: firewall.RuleTypeGroup, Action: "web", IFace: "net1"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, firewallRuleChanged(base, tc.new))
		})
	}
}

func TestTagsEqual(t *testing.T) {
	t.Parallel()

	// Proxmox lowercases tags by default, and may return them in a
	// different order than declared; neither should be treated as a
	// change worth writing.
	assert.True(t, tagsEqual([]string{"web", "prod"}, []string{"Prod", "WEB"}))
	assert.True(t, tagsEqual([]string{"a", "b"}, []string{"b", "a"}))
	assert.True(t, tagsEqual(nil, nil))
	assert.True(t, tagsEqual([]string{"web", "web"}, []string{"WEB"}))

	assert.False(t, tagsEqual([]string{"web"}, []string{"web", "prod"}))
	assert.False(t, tagsEqual([]string{"web"}, nil))
}
