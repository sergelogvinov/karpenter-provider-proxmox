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
	"strconv"
	"strings"

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	goproxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmox"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
}

type DefaultProvider struct {
	kubernetesInterface      kubernetes.Interface
	cluster                  *pxpool.ProxmoxPool
	cloudCapacityProvider    cloudcapacity.Provider
	instanceTemplateProvider instancetemplate.Provider
}

func NewProvider(
	ctx context.Context,
	kubernetesInterface kubernetes.Interface,
	cluster *pxpool.ProxmoxPool,
	cloudCapacityProvider cloudcapacity.Provider,
	instanceTemplateProvider instancetemplate.Provider,
) (*DefaultProvider, error) {
	return &DefaultProvider{
		kubernetesInterface:      kubernetesInterface,
		cluster:                  cluster,
		cloudCapacityProvider:    cloudCapacityProvider,
		instanceTemplateProvider: instanceTemplateProvider,
	}, nil
}

// Create an instance given the constraints.
func (p *DefaultProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Create()")

	errs := []error{}

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

			zones = p.sortBestZoneByPlacementStrategy(nodeClass.Spec.PlacementStrategy, region, zones)
			for _, zone := range zones {
				instanceTemplate, err := p.instanceTemplateProvider.Get(ctx, nodeClass, region, zone)
				if err != nil {
					log.Error(err, "Failed to get instance template", "region", region, "zone", zone, "instanceType", instanceType.Name)
					errs = append(errs, err)

					continue
				}

				node, err := p.instanceCreate(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone)
				if err != nil {
					log.Error(err, "Failed to create instance", "region", region, "zone", zone, "instanceType", instanceType.Name)
					errs = append(errs, err)

					continue
				}

				node.Labels[v1alpha1.LabelInstanceImageID] = strconv.Itoa(int(instanceTemplate.TemplateID))

				// FIXME: reserve capacity in creation stage
				p.cloudCapacityProvider.UpdateNodeCapacityInZone(ctx, region, zone)

				return node, nil
			}
		}

		errs = append(errs, fmt.Errorf("no available regions found for instance type %s", instanceType.Name))
	}

	return nil, fmt.Errorf("failed to create instance after trying all instance types: %w", errors.Join(errs...))
}

func (p *DefaultProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.Get()").WithValues("providerID", providerID)
	log.Info("Get instance by providerID")

	vmid, region, err := provider.ParseProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse providerID: %v", err)
	}

	vm, err := p.cluster.GetVMByIDInRegion(ctx, region, uint64(vmid))
	if err != nil {
		if err == goproxmox.ErrVirtualMachineNotFound {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return nil, fmt.Errorf("failed to get vm: %v", err)
	}

	// FIXME
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: vm.Name,
			Labels: map[string]string{
				corev1.LabelTopologyRegion:  region,
				corev1.LabelTopologyZone:    vm.Node,
				karpv1.CapacityTypeLabelKey: karpv1.CapacityTypeOnDemand,
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

	log.V(1).Info("Get instance", "node", vm.Name, "vmID", vm.VMID)

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

	vm, err := p.cluster.GetVMByIDInRegion(ctx, region, uint64(vmid))
	if err != nil {
		if err == goproxmox.ErrVirtualMachineNotFound {
			return cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return fmt.Errorf("failed to get vm: %v", err)
	}

	zone := vm.Node
	if zone == "" {
		zone = nodeClaim.Labels[corev1.LabelTopologyZone]
	}

	log.V(1).Info("Delete instance", "region", region, "zone", zone, "vmID", vmid)

	if err = p.cluster.DeleteVMByIDInRegion(ctx, region, vm); err != nil {
		return fmt.Errorf("cannot delete vm with id %d: %w", vmid, err)
	}

	return nil
}

func (p *DefaultProvider) instanceCreate(ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceTemplate *instancetemplate.InstanceTemplateInfo,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
) (*corev1.Node, error) {
	log := log.FromContext(ctx).WithName("instance.instanceCreate()").WithValues("region", region, "zone", zone, "instanceType", instanceType.Name)

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	// FIXME: Need random ID generator
	newID, err := px.GetNextID(ctx, options.FromContext(ctx).ProxmoxVMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next id: %v", err)
	}

	vmTemplateID := instanceTemplate.TemplateID
	if vmTemplateID == 0 {
		return nil, fmt.Errorf("could not find vm template: %w", err)
	}

	storage := nodeClass.Spec.BootDevice.Storage
	if storage == "" {
		storage = instanceTemplate.TemplateStorageID
	}

	if storage == "" {
		return nil, fmt.Errorf("storage device must be specified in node class or instance template")
	}

	size := nodeClass.Spec.BootDevice.Size.ScaledValue(resource.Giga)
	sizeInstType := instanceType.Capacity.StorageEphemeral().ScaledValue(resource.Giga)

	// We will use the size from the instance type if it is larger than the one specified in the node class
	// Scheduling uses StorageEphemeral capacity to determine the InstanceType
	if sizeInstType > 0 && size < sizeInstType {
		size = sizeInstType
	}

	vmOptions := goproxmox.VMCloneRequest{
		NewID:       newID,
		Node:        zone,
		Name:        nodeClaim.Name,
		Description: fmt.Sprintf("Karpeneter, class=%s", nodeClass.Name),
		Full:        1,
		Storage:     storage,

		CPU:          int(instanceType.Capacity.Cpu().Value()),
		Memory:       uint32(instanceType.Capacity.Memory().Value() / 1024 / 1024),
		DiskSize:     fmt.Sprintf("%dG", size),
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

	newID, err = px.CloneVM(ctx, int(vmTemplateID), vmOptions)
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

	if len(rules) > 0 {
		err = px.CreateVMFirewallRules(ctx, newID, zone, rules)
		if err != nil {
			return nil, fmt.Errorf("failed to create firewall rules for vm %d in region %s: %v", newID, region, err)
		}
	}

	if nodeClass.Spec.MetadataOptions.Type == "cdrom" {
		err = p.attachCloudInitISO(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone, newID)
		if err != nil {
			return nil, fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", newID, region, err)
		}
	}

	log.V(1).Info("Starting VM", "vmID", newID)

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
