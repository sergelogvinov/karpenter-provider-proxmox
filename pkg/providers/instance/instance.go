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
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu/firewall"
	"github.com/sergelogvinov/go-proxmox-rest/pools"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/bootstrap"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error)
	Get(ctx context.Context, providerID string) (*corev1.Node, error)
	Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error

	UpdateFirewallRules(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error
	UpdateTags(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error
	UpdatePoolMembership(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error

	DetachCloudInit(ctx context.Context, nodeClaim *karpv1.NodeClaim) error
}

type DefaultProvider struct {
	kubernetesInterface         kubernetes.Interface
	kubernetesBootstrapProvider bootstrap.Provider
	cluster                     *pxpool.ProxmoxPool
	cloudCapacityProvider       cloudcapacity.Provider
	nodeIpamProvider            nodeipam.Provider
	instanceTemplateProvider    instancetemplate.Provider
}

func NewProvider(
	ctx context.Context,
	kubernetesInterface kubernetes.Interface,
	kubernetesBootstrapProvider bootstrap.Provider,
	cluster *pxpool.ProxmoxPool,
	cloudCapacityProvider cloudcapacity.Provider,
	nodeIpamController nodeipam.Provider,
	instanceTemplateProvider instancetemplate.Provider,
) (*DefaultProvider, error) {
	return &DefaultProvider{
		kubernetesInterface:         kubernetesInterface,
		kubernetesBootstrapProvider: kubernetesBootstrapProvider,
		cluster:                     cluster,
		cloudCapacityProvider:       cloudCapacityProvider,
		nodeIpamProvider:            nodeIpamController,
		instanceTemplateProvider:    instanceTemplateProvider,
	}, nil
}

// Create an instance given the constraints.
func (p *DefaultProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Create()")

	errs := []error{}

	instanceTypes = orderInstanceTypesByPrice(instanceTypes, scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...))
	for _, instanceType := range instanceTypes {
		regions := []string{}
		if nodeClass.Spec.Region != "" {
			regions = []string{nodeClass.Spec.Region}
		}

		if len(regions) == 0 {
			regions = getValuesByKey(instanceType, corev1.LabelTopologyRegion, p.cloudCapacityProvider.Regions())
		}

		for _, region := range regions {
			zones := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyZone).Values()
			if len(zones) == 0 {
				zones = p.cloudCapacityProvider.GetAvailableZonesInRegion(region, instanceType.Capacity)
			}

			zones = getValuesByKey(instanceType, corev1.LabelTopologyZone, zones)
			if len(zones) == 0 {
				log.Error(ErrNoZoneFound, "No zones available in region for instanceType", "region", region, "instanceType", instanceType.Name)

				continue
			}

			templateIDs := nodeClass.GetTemplateIDs(region)

			zones = p.sortBestZoneByPlacementStrategy(nodeClass.Spec.PlacementStrategy, region, lo.Intersect(zones, nodeClass.GetZones(region)))
			for _, zone := range zones {
				templates := p.instanceTemplateProvider.ListWithFilter(ctx, func(c *instancetemplate.InstanceTemplateInfo) bool {
					return c.Region == region && c.Zone == zone && slices.Contains(templateIDs, c.TemplateID)
				})

				if len(templates) == 0 {
					log.Info("Failed to get instance template", "region", region, "zone", zone, "instanceType", instanceType.Name)

					errs = append(errs, fmt.Errorf("no instance templates found in region %s and zone %s", region, zone))

					continue
				}

				template := templates[0]

				node, err := p.instanceCreate(ctx, nodeClaim, nodeClass, &template, instanceType, region, zone)
				if err != nil {
					log.Error(err, "Failed to create instance", "region", region, "zone", zone, "instanceType", instanceType.Name)
					errs = append(errs, err)

					continue
				}

				node.Labels[v1alpha1.LabelInstanceImageID] = template.TemplateHash

				return node, nil
			}
		}

		errs = append(errs, fmt.Errorf("no available regions found for instance type %s", instanceType.Name))
	}

	return nil, fmt.Errorf("failed to create instance after trying all instance types: %w", errors.Join(errs...))
}

func (p *DefaultProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	vmid, region, err := provider.ParseProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse providerID: %v", err)
	}

	vm, err := p.cluster.Cluster(region).Get(ctx, pxpool.ResourceKindVM, strconv.Itoa(vmid))
	if err != nil {
		return nil, err
	}

	node := &corev1.Node{
		Name: vm.Name,
		Labels: map[string]string{
			corev1.LabelTopologyRegion: region,
			corev1.LabelTopologyZone:   vm.Node,
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:    karpv1.ArchitectureAmd64,
				OperatingSystem: string(corev1.Linux),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	if vm.Status == "stopped" {
		node.Status.Conditions = []corev1.NodeCondition{
			{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			},
		}
	}

	return node, nil
}

func (p *DefaultProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := log.FromContext(ctx).WithName("instance.Delete()")

	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from provider-id: %v", err)
	}

	if region == "" {
		region = nodeClaim.Labels[corev1.LabelTopologyRegion]
	}

	vmr, err := p.cluster.Cluster(region).Get(ctx, pxpool.ResourceKindVM, strconv.Itoa(vmid))
	if err != nil {
		if errors.Is(err, pxpool.ErrResourceNotFound) {
			return cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return fmt.Errorf("failed to get vm: %v", err)
	}

	zone := vmr.Node
	if zone == "" {
		zone = nodeClaim.Labels[corev1.LabelTopologyZone]
	}

	log.V(1).Info("Delete instance", "region", region, "zone", zone, "vmID", vmid)

	return p.instanceDelete(ctx, nodeClaim, region, zone, vmr)
}

func (p *DefaultProvider) UpdateFirewallRules(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error {
	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from provider-id: %v", err)
	}

	if region == "" {
		region = nodeClaim.Labels[corev1.LabelTopologyRegion]
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]

	px, err := p.cluster.Get(region)
	if err != nil {
		return pxpool.ErrClusterNotFound
	}

	return reconcileFirewallRules(ctx, px, zone, vmid, buildFirewallRules(nodeClass.Spec.SecurityGroups))
}

// buildFirewallRules converts a node class's security groups into the
// per-position firewall rule list Proxmox expects (one "group" rule per
// entry, applied in order).
func buildFirewallRules(securityGroups []v1alpha1.SecurityGroups) []firewall.Rule {
	rules := make([]firewall.Rule, len(securityGroups))
	for i, sg := range securityGroups {
		rules[i] = firewall.Rule{
			Pos:    i,
			Enable: 1,
			Type:   firewall.RuleTypeGroup,
			Action: sg.Name,
			IFace:  sg.Interface,
		}
	}

	return rules
}

// reconcileFirewallRules diffs rules against the guest's current firewall
// rule list position-by-position: updates a position whose rule changed,
// creates new positions past the current list's end, and deletes now-extra
// trailing positions — the same reconcile-by-position approach the old
// goproxmox.APIClient.UpdateVMFirewallRules used. Deletions run from the
// highest position down, since Proxmox renumbers remaining rules after
// each delete (deleting ascending would target the wrong position after
// the first removal — a latent bug in the old client's identical ascending
// loop, whenever 2+ trailing rules needed removal at once).
func reconcileFirewallRules(ctx context.Context, px *proxmoxrest.Client, zone string, vmid int, rules []firewall.Rule) error {
	rulesClient := px.Nodes(zone).Qemu().Firewall().Rules(vmid)

	oldRules, err := rulesClient.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to get firewall rules for vm %d: %v", vmid, err)
	}

	for i := range max(len(oldRules), len(rules)) {
		switch {
		case i < len(oldRules) && i < len(rules) && firewallRuleChanged(oldRules[i], rules[i]):
			if err := rulesClient.Update(ctx, i, firewallRuleOptions(rules[i])); err != nil {
				return fmt.Errorf("failed to update firewall rule for vm %d: %v", vmid, err)
			}
		case i >= len(oldRules) && i < len(rules):
			if err := rulesClient.Create(ctx, firewallRuleOptions(rules[i])); err != nil {
				return fmt.Errorf("failed to create new firewall rule for vm %d: %v", vmid, err)
			}
		}
	}

	for i := len(oldRules) - 1; i >= len(rules); i-- {
		if err := rulesClient.Delete(ctx, i, ""); err != nil {
			return fmt.Errorf("failed to delete old firewall rule for vm %d: %v", vmid, err)
		}
	}

	return nil
}

// firewallRuleChanged reports whether newRule differs from old in any field
// buildFirewallRules sets (Pos, Type, Action, Enable, IFace). old, as
// returned by Proxmox, also carries server-managed fields (Digest,
// IPVersion, ...) that buildFirewallRules never sets and firewallRuleOptions
// never sends — comparing the whole struct against those would never match,
// so every reconcile would rewrite every rule even when nothing changed.
func firewallRuleChanged(old, newRule firewall.Rule) bool {
	return old.Pos != newRule.Pos ||
		old.Type != newRule.Type ||
		old.Action != newRule.Action ||
		old.Enable != newRule.Enable ||
		old.IFace != newRule.IFace
}

func firewallRuleOptions(r firewall.Rule) *firewall.RuleOptions {
	return &firewall.RuleOptions{
		Type:   r.Type,
		Action: r.Action,
		Enable: new(r.Enable == 1),
		IFace:  new(r.IFace),
	}
}

func (p *DefaultProvider) UpdateTags(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error {
	tags := lo.Uniq(nodeClass.Spec.Tags)
	slices.Sort(tags)

	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from provider-id: %v", err)
	}

	if region == "" {
		region = nodeClaim.Labels[corev1.LabelTopologyRegion]
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]

	px, err := p.cluster.Get(region)
	if err != nil {
		return pxpool.ErrClusterNotFound
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmid, nil)
	if err != nil {
		return fmt.Errorf("unable to get vm config for vm %d: %w", vmid, err)
	}

	current := []string{}
	if cfg.Tags != nil {
		current = *cfg.Tags
	}

	if !tagsEqual(current, tags) {
		newTags := qemu.Tags(tags)
		if err := px.Nodes(zone).Qemu().UpdateConfig(ctx, vmid, &qemu.Config{Tags: &newTags}); err != nil {
			return fmt.Errorf("failed to update tags for vm %d: %w", vmid, err)
		}
	}

	return nil
}

// tagsEqual reports whether a and b represent the same tag set, ignoring
// order and case. Proxmox lowercases tags on save by default (the
// datacenter.cfg tag-style setting's case-sensitive option defaults to
// off), so comparing case-sensitively against what Proxmox returns would
// never converge for any declared tag containing an uppercase letter:
// every reconcile would keep rewriting the same tags. The tags actually
// sent to Proxmox keep their declared case (see UpdateTags above); only
// this comparison normalizes case, so a case-sensitive cluster still gets
// the user's exact casing.
func tagsEqual(a, b []string) bool {
	return slices.Equal(normalizeTags(a), normalizeTags(b))
}

func normalizeTags(tags []string) []string {
	normalized := make([]string, len(tags))
	for i, t := range tags {
		normalized[i] = strings.ToLower(t)
	}

	normalized = lo.Uniq(normalized)
	slices.Sort(normalized)

	return normalized
}

func (p *DefaultProvider) UpdatePoolMembership(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass) error {
	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from provider-id: %v", err)
	}

	if region == "" {
		region = nodeClaim.Labels[corev1.LabelTopologyRegion]
	}

	px, err := p.cluster.Get(region)
	if err != nil {
		return pxpool.ErrClusterNotFound
	}

	poolName := nodeClass.Spec.ResourcePool
	poolNameOld := nodeClaim.Annotations[v1alpha1.AnnotationProxmoxNodeClassPool]

	if poolName != poolNameOld && poolNameOld != "" {
		pool, err := px.Pools().Get(ctx, poolNameOld)
		if err != nil {
			return fmt.Errorf("failed to get pool %s: %w", poolNameOld, err)
		}

		if _, ok := lo.Find(pool.Members, func(m pools.PoolMember) bool {
			return m.VMID == vmid
		}); ok {
			err = px.Pools().Update(ctx, poolNameOld, &pools.UpdateOptions{
				VMIDs:  []int{vmid},
				Remove: new(true),
			})
			if err != nil {
				return fmt.Errorf("failed to remove vm %d from pool %s: %w", vmid, poolNameOld, err)
			}
		}
	}

	if poolName == "" {
		return nil
	}

	pool, err := px.Pools().Get(ctx, poolName)
	if err != nil {
		return fmt.Errorf("failed to get pool %s: %w", poolName, err)
	}

	if _, ok := lo.Find(pool.Members, func(m pools.PoolMember) bool {
		return m.VMID == vmid
	}); ok {
		return nil
	}

	err = px.Pools().Update(ctx, poolName, &pools.UpdateOptions{
		VMIDs: []int{vmid},
	})
	if err != nil {
		return fmt.Errorf("failed to add vm %d to pool %s: %w", vmid, poolName, err)
	}

	return nil
}

func (p *DefaultProvider) DetachCloudInit(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	vmid, region, err := provider.ParseProviderID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to parse providerID: %v", err)
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]

	err = p.detachCloudInitISO(ctx, region, zone, vmid)
	if err != nil {
		return fmt.Errorf("failed to detach cloud-init ISO from vm %d in region %s: %v", vmid, region, err)
	}

	return nil
}
