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
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	goproxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmox"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error)
	Get(ctx context.Context, providerID string) (*corev1.Node, error)
	Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error
}

type DefaultProvider struct {
	cluster               *pxpool.ProxmoxPool
	cloudCapacityProvider cloudcapacity.Provider
}

func NewProvider(ctx context.Context, cluster *pxpool.ProxmoxPool, cloudCapacityProvider cloudcapacity.Provider) (*DefaultProvider, error) {
	return &DefaultProvider{
		cluster:               cluster,
		cloudCapacityProvider: cloudCapacityProvider,
	}, nil
}

// Create an instance given the constraints.
func (p *DefaultProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Create()")
	log.V(1).Info("Requirements", "nodeClaim", nodeClaim.Spec.Requirements, "nodeClass", nodeClass.Spec)

	var errs []error

	instanceTypes = orderInstanceTypesByPrice(instanceTypes, scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...))
	for _, instanceType := range instanceTypes {
		regions := []string{}

		if nodeClass.Spec.Region == "" {
			requestedRegion := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyRegion)
			if len(requestedRegion.Values()) == 0 {
				regions = p.cloudCapacityProvider.Regions()
			} else {
				regions = requestedRegion.Values()
			}
		}

		for _, region := range regions {
			requestedZones := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyZone)
			zones := requestedZones.Values()
			if len(zones) == 0 {
				zones = p.cloudCapacityProvider.GetAvailableZonesInRegion(region, instanceType.Capacity)
			}

			if len(zones) == 0 {
				log.Error(fmt.Errorf("no zones available"), "No zones available in region", "region", region, "instanceType", instanceType.Name)

				continue
			}

			for _, zone := range p.sortBestZoneByPlacementStrategy(nodeClass.Spec.PlacementStrategy, region, zones) {
				node, err := p.instanceCreate(ctx, nodeClaim, nodeClass, instanceType, region, zone)
				if err != nil {
					log.Error(err, "Failed to create instance", "nodeClaim", nodeClaim.Name, "instanceType", instanceType.Name, "region", region, "zone", zone)
					errs = append(errs, err)

					continue
				}

				p.cloudCapacityProvider.UpdateNodeCapacityInZone(ctx, region, zone)

				return node, nil
			}
		}
	}

	return nil, fmt.Errorf("failed to create instance after trying all instance types: %w", errors.Join(errs...))
}

func (p *DefaultProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Get()")

	vmid, region, err := provider.ParseProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse providerID: %v", err)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.LabelTopologyRegion: region,
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:    karpv1.ArchitectureAmd64,
				OperatingSystem: string(corev1.Linux),
			},
		},
	}

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	vm, err := px.FindVMByID(ctx, uint64(vmid))
	if err != nil {
		if err == goproxmox.ErrVirtualMachineNotFound {
			return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance not found: %w", err))
		}

		return nil, fmt.Errorf("failed to get vm: %v", err)
	}

	node.ObjectMeta.Name = vm.Name
	node.ObjectMeta.Labels = map[string]string{
		corev1.LabelTopologyRegion:            region,
		corev1.LabelTopologyZone:              vm.Node,
		karpv1.CapacityTypeLabelKey:           karpv1.CapacityTypeOnDemand,
		v1alpha1.LabelInstanceCPUManufacturer: "kvm64",
	}

	log.V(1).Info("Get instance", "node", node.Name, "providerID", providerID, "vmID", vm.VMID)

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

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	vm, err := px.FindVMByID(ctx, uint64(vmid))
	if err != nil {
		if err == goproxmox.ErrVirtualMachineNotFound {
			return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance not found: %w", err))
		}

		return fmt.Errorf("failed to get vm: %v", err)
	}

	zone := vm.Node
	if zone == "" {
		zone = nodeClaim.Labels[corev1.LabelTopologyZone]
	}

	log.V(1).Info("Delete instance", "nodeClaim", nodeClaim.Name, "vmID", vmid, "region", region, "zone", zone)

	if err = px.DeleteVMByID(ctx, zone, vmid); err != nil {
		return fmt.Errorf("cannot delete vm with id %d: %w", vmid, err)
	}

	return nil
}

func (p *DefaultProvider) instanceCreate(ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.instanceCreate()")

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	newID, err := px.GetNextID(ctx, options.FromContext(ctx).ProxmoxVMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next id: %v", err)
	}

	vmTemplateID, err := px.FindVMTemplateByName(ctx, zone, nodeClass.Spec.InstanceTemplate.Name)
	if err != nil {
		return nil, fmt.Errorf("could not find vm template: %w", err)
	}

	vmOptions := goproxmox.VMCloneRequest{
		NewID:       newID,
		Node:        zone,
		Name:        nodeClaim.Name,
		Description: fmt.Sprintf("Karpeneter, class=%s", nodeClass.Name),
		Full:        1,
		Storage:     nodeClass.Spec.BlockDevicesStorageID,
		DiskSize:    fmt.Sprintf("%dG", instanceType.Capacity.StorageEphemeral().Value()/1024/1024/1024),

		CPU:          int(instanceType.Capacity.Cpu().Value()),
		Memory:       uint32(instanceType.Capacity.Memory().Value() / 1024 / 1024),
		Tags:         strings.Join(nodeClass.Spec.Tags, ";"),
		InstanceType: instanceType.Name,
	}

	defer func() {
		if err != nil {
			if newID == 0 {
				return
			}

			if defErr := px.DeleteVMByID(ctx, zone, newID); defErr != nil {
				fmt.Printf("failed to delete vm %d in region %s: %v", newID, region, defErr)
			}
		}
	}()

	newID, err = px.CloneVM(ctx, vmTemplateID, vmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone vm template %d in region %s: %v", vmTemplateID, region, err)
	}

	rules := make([]*proxmox.FirewallRule, len(nodeClass.Spec.SecurityGroups))
	for i, sg := range nodeClass.Spec.SecurityGroups {
		rules[i] = &proxmox.FirewallRule{
			Enable: 1,
			Type:   "group",
			Action: sg.Name,
			Iface:  sg.Interface,
		}
	}

	if err = px.CreateVMFirewallRules(ctx, newID, zone, rules); err != nil {
		return nil, fmt.Errorf("failed to create firewall rules for vm %d in region %s: %v", newID, region, err)
	}

	log.V(1).Info("StartVM", "Name", nodeClaim.Name, "ID", newID, "region", region, "zone", zone)

	if _, err = px.StartVMByID(ctx, zone, newID); err != nil {
		return nil, fmt.Errorf("failed to start vm %d in region %s: %v", newID, region, err)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				corev1.LabelTopologyRegion:     region,
				corev1.LabelTopologyZone:       zone,
				corev1.LabelInstanceTypeStable: instanceType.Name,
				karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
				v1alpha1.LabelInstanceFamily:   strings.Split(instanceType.Name, ".")[0],
			},
			Annotations:       map[string]string{},
			CreationTimestamp: metav1.Now(),
		},
		Spec: corev1.NodeSpec{
			ProviderID: provider.GetProviderID(region, newID),
			Taints:     []corev1.Taint{karpv1.UnregisteredNoExecuteTaint},
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:    karpv1.ArchitectureAmd64,
				OperatingSystem: string(corev1.Linux),
			},
		},
	}

	return node, nil
}

func (p *DefaultProvider) sortBestZoneByPlacementStrategy(placementStrategy *v1alpha1.PlacementStrategy, region string, zones []string) []string {
	if len(zones) == 1 {
		return zones
	}

	strategy := placementStrategy
	if strategy == nil {
		strategy = &v1alpha1.PlacementStrategy{
			ZoneBalance: v1alpha1.PlacementStrategyBalanced,
		}
	}

	switch strategy.ZoneBalance {
	case v1alpha1.PlacementStrategyAvailabilityFirst:
		// Sort zones randomly to prioritize availability
		sortedZones := make([]string, len(zones))
		for i, v := range rand.Perm(len(zones)) {
			sortedZones[v] = zones[i]
		}

		return sortedZones
	default:
		// Sort zones by CPU load
		return p.cloudCapacityProvider.SortZonesByCPULoad(region, zones)
	}
}

func orderInstanceTypesByPrice(instanceTypes []*cloudprovider.InstanceType, requirements scheduling.Requirements) []*cloudprovider.InstanceType {
	// Order instance types so that we get the cheapest instance types of the available offerings
	sort.Slice(instanceTypes, func(i, j int) bool {
		iPrice := math.MaxFloat64
		jPrice := math.MaxFloat64

		if len(instanceTypes[i].Offerings.Available().Compatible(requirements)) > 0 {
			iPrice = instanceTypes[i].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if len(instanceTypes[j].Offerings.Available().Compatible(requirements)) > 0 {
			jPrice = instanceTypes[j].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if iPrice == jPrice {
			return instanceTypes[i].Name < instanceTypes[j].Name
		}

		return iPrice < jPrice
	})

	return instanceTypes
}
