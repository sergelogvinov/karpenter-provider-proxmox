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
	"fmt"
	"math"
	"sort"
	"strings"

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

type Provider struct {
	cluster               *pxpool.ProxmoxPool
	cloudCapacityProvider cloudcapacity.Provider
}

func NewProvider(ctx context.Context, cluster *pxpool.ProxmoxPool, cloudCapacityProvider cloudcapacity.Provider) (*Provider, error) {
	return &Provider{
		cluster:               cluster,
		cloudCapacityProvider: cloudCapacityProvider,
	}, nil
}

// Create an instance given the constraints.
func (p *Provider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Create()")

	instanceTypes = orderInstanceTypesByPrice(instanceTypes, scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...))
	instanceType := instanceTypes[0]

	log.V(1).Info("Requirements", "nodeClaim", nodeClaim.Spec.Requirements, "nodeClass", nodeClass.Spec)

	region := nodeClass.Spec.Region
	if region == "" {
		requestedRegion := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyRegion)
		if len(requestedRegion.Values()) == 0 {
			// FIXME: need to try all regions
			region = "region-1"
		} else {
			region = requestedRegion.Any()
		}
	}

	requestedZones := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyZone)
	zone := requestedZones.Any()
	if len(requestedZones.Values()) == 0 || zone == "" {
		// FIXME: use best offering in zones based on placementStrategy
		zones := p.cloudCapacityProvider.GetAvailableZonesInRegion(region, instanceType.Capacity)

		if len(zones) == 0 {
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no capacity zone available"))
		}

		zone = zones[0]
	}

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	newID, err := px.GetNextID(ctx, options.FromContext(ctx).ProxmoxVMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next id: %v", err)
	}

	vmTemplateID, err := px.FindVMTemplateByName(ctx, zone, nodeClass.Spec.Template)
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
			if err := px.DeleteVMByID(ctx, zone, newID); err != nil {
				fmt.Printf("failed to delete vm %d in region %s: %v", newID, region, err)
			}
		}
	}()

	newID, err = px.CloneVM(ctx, vmTemplateID, vmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone vm template %d in region %s: %v", vmTemplateID, region, err)
	}

	log.V(1).Info("StartVM", "Name", nodeClaim.Name, "ID", newID, "region", region, "zone", zone)

	if _, err = px.StartVMByID(ctx, zone, newID); err != nil {
		return nil, fmt.Errorf("failed to start vm %d in region %s: %v", newID, region, err)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				corev1.LabelTopologyRegion:            region,
				corev1.LabelTopologyZone:              zone,
				corev1.LabelInstanceTypeStable:        instanceType.Name,
				karpv1.CapacityTypeLabelKey:           karpv1.CapacityTypeOnDemand,
				v1alpha1.LabelInstanceFamily:          strings.Split(instanceType.Name, ".")[0],
				v1alpha1.LabelInstanceCPUManufacturer: "kvm64",
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

func (p *Provider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
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

	log.V(1).Info("Get instance", "node", node.Name, "providerID", providerID, "vm", vm)

	return node, nil
}

func (p *Provider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := log.FromContext(ctx).WithName("instance.Delete()")

	// FIXME: Get region and zone from nodeClaim
	region := nodeClaim.Labels[corev1.LabelTopologyRegion]
	if region == "" {
		region = "region-1"
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]
	if zone == "" {
		zone = "rnd-1"
	}

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	vmID, err := provider.GetVMID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from providerID: %v", err)
	}

	vm, err := px.FindVMByID(ctx, uint64(vmID))
	if err != nil {
		if err == goproxmox.ErrVirtualMachineNotFound {
			return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance not found: %w", err))
		}

		return fmt.Errorf("failed to get vm: %v", err)
	}

	log.V(1).Info("Delete instance", "nodeClaim", nodeClaim.Name, "vm", vm.VMID, "region", region, "zone", zone)

	if err = px.DeleteVMByID(ctx, zone, vmID); err != nil {
		return fmt.Errorf("cannot delete vm with id %d: %w", vmID, err)
	}

	return nil
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
