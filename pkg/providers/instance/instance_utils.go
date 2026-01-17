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
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/resourcemanager"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

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
		return nil, pxpool.ErrRegionNotFound
	}

	newID, err := px.GetNextID(ctx, options.FromContext(ctx).ProxmoxVMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next id: %v", err)
	}

	vmTemplateID := instanceTemplate.TemplateID
	if vmTemplateID == 0 {
		return nil, fmt.Errorf("could not find vm template")
	}

	storage := nodeClass.Spec.BootDevice.Storage
	if storage == "" {
		storage = instanceTemplate.TemplateStorageID
	}

	if storage == "" {
		return nil, fmt.Errorf("storage device must be specified in node class or instance template")
	}

	// We will use the size from the instance type if it is larger than the one specified in the node class
	// Scheduling uses StorageEphemeral capacity to determine the InstanceType
	size := max(nodeClass.Spec.BootDevice.Size.ScaledValue(resource.Giga), instanceType.Capacity.StorageEphemeral().ScaledValue(resource.Giga))

	opt := &resourcemanager.VMResourceOptions{
		ID:           newID,
		CPUs:         int(instanceType.Capacity.Cpu().Value()),
		MemoryMBytes: uint64(instanceType.Capacity.Memory().ScaledValue(resource.Mega)),
		DiskGBytes:   uint64(size),
		StorageID:    storage,
	}

	if err := p.cloudCapacityProvider.AllocateCapacityInZone(ctx, region, zone, newID, opt); err != nil {
		return nil, fmt.Errorf("failed to reserve capacity: %v", err)
	}

	capacityType := getCapacityType(nodeClaim, instanceType, region, zone)

	comments := []string{
		"Karpenter managed instance",
		fmt.Sprintf("class=%s", nodeClass.Name),
		fmt.Sprintf("capacity-type=%s", capacityType),
	}
	if !opt.CPUSet.IsEmpty() {
		comments = append(comments, fmt.Sprintf("affinity=%s", opt.CPUSet.String()))
	}

	vmOptions := goproxmox.VMCloneRequest{
		NewID:       newID,
		Node:        zone,
		Name:        nodeClaim.Name,
		Description: strings.Join(comments, ", "),
		Full:        1,
		Pool:        nodeClass.Spec.ResourcePool,
		Storage:     storage,

		CPU:          opt.CPUs,
		CPUAffinity:  opt.CPUSet.String(),
		Memory:       uint32(opt.MemoryMBytes),
		DiskSize:     fmt.Sprintf("%dG", opt.DiskGBytes),
		Tags:         strings.Join(nodeClass.Spec.Tags, ";"),
		InstanceType: instanceType.Name,
	}

	defer func() {
		if err != nil {
			if newID == 0 {
				return
			}

			if err := p.cloudCapacityProvider.ReleaseCapacityInZone(ctx, region, zone, newID, opt); err != nil {
				log.Error(err, "failed to release capacity", "vmID", newID)
			}

			if defErr := px.DeleteVMByID(ctx, zone, newID); defErr != nil && !errors.Is(defErr, goproxmox.ErrVirtualMachineNotFound) {
				log.Error(defErr, "failed to delete vm", "vmID", newID)
			}
		}
	}()

	newID, err = px.CloneVM(ctx, int(vmTemplateID), vmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to clone vm template %d: %v", vmTemplateID, err)
	}

	err = p.instanceNetworkSetup(ctx, region, zone, newID)
	if err != nil {
		return nil, fmt.Errorf("failed to configure networking for vm %d: %v", newID, err)
	}

	rules := make([]*proxmox.FirewallRule, len(nodeClass.Spec.SecurityGroups))
	for i, sg := range nodeClass.Spec.SecurityGroups {
		rules[i] = &proxmox.FirewallRule{
			Enable: 1,
			Pos:    i,
			Type:   "group",
			Action: sg.Name,
			Iface:  sg.Interface,
		}
	}

	if len(rules) > 0 {
		err = px.CreateVMFirewallRules(ctx, newID, zone, rules)
		if err != nil {
			return nil, fmt.Errorf("failed to create firewall rules for vm %d: %v", newID, err)
		}
	}

	if nodeClass.Spec.MetadataOptions.Type == "cdrom" {
		err = p.attachCloudInitISO(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone, newID)
		if err != nil {
			return nil, fmt.Errorf("failed to attach cloud-init ISO to vm %d: %v", newID, err)
		}
	}

	log.V(1).Info("Starting VM", "vmID", newID)

	vm, err := px.StartVMByID(ctx, zone, newID)
	if err != nil {
		return nil, fmt.Errorf("failed to start vm %d: %v", newID, err)
	}

	cpu := goproxmox.VMCPU{}
	if err := cpu.UnmarshalString(vm.VirtualMachineConfig.CPU); err != nil {
		log.Error(err, "Failed to parse CPU config", "config", vm.VirtualMachineConfig.CPU)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				corev1.LabelTopologyRegion:     region,
				corev1.LabelTopologyZone:       zone,
				corev1.LabelInstanceTypeStable: instanceType.Name,
				karpv1.CapacityTypeLabelKey:    capacityType,
				v1alpha1.LabelInstanceFamily:   strings.Split(instanceType.Name, ".")[0],
				v1alpha1.LabelInstanceCPUType:  cpu.Type,
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

func (p *DefaultProvider) instanceDelete(ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	region string,
	zone string,
	vmr *proxmox.ClusterResource,
) error {
	log := log.FromContext(ctx).WithName("instance.instanceDelete()").WithValues("region", region, "zone", zone)

	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return pxpool.ErrRegionNotFound
	}

	vm, err := px.GetVMConfig(ctx, int(vmr.VMID))
	if err != nil {
		return fmt.Errorf("failed to get vm config for VM %d: %v", vmr.VMID, err)
	}

	opt := &resourcemanager.VMResourceOptions{
		ID:           int(vm.VMID),
		CPUs:         vm.CPUs,
		MemoryMBytes: vm.MaxMem / (1024 * 1024),
		DiskGBytes:   uint64(nodeClaim.Status.Capacity.StorageEphemeral().ScaledValue(resource.Giga)),
	}

	if vm.VirtualMachineConfig != nil && vm.VirtualMachineConfig.Affinity != "" {
		opt.CPUSet, err = cpuset.Parse(vm.VirtualMachineConfig.Affinity)
		if err != nil {
			return fmt.Errorf("Failed to parse CPU affinity for VM %d: %w", vmr.VMID, err)
		}

		if opt.CPUSet.Size() != opt.CPUs {
			log.Info("CPU affinity size does not match allocated CPUs", "vmID", vmr.VMID, "CPUs", opt.CPUs, "CPUSet", opt.CPUSet.String())
		}
	}

	if err := p.cluster.DeleteVMByIDInRegion(ctx, region, vmr); err != nil {
		return fmt.Errorf("cannot delete VM with id %d: %w", vmr.VMID, err)
	}

	if err := p.cloudCapacityProvider.ReleaseCapacityInZone(ctx, region, zone, int(vmr.VMID), opt); err != nil {
		log.Error(err, "failed to release capacity after VM deletion", "vmID", vmr.VMID)
	}

	return nil
}
