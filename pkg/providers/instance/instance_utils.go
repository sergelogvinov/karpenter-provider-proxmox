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
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/cluster"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu/firewall"
	"github.com/sergelogvinov/go-proxmox-rest/task"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// cloneTaskTimeout and powerActionTimeout bound how long the clone/resize
// and start/delete background tasks are waited on, respectively.
const (
	cloneTaskTimeout   = 5 * time.Minute
	powerActionTimeout = time.Minute
)

// instanceCreate launches the VM reserved in zone. A failed launch is
// rolled back, the reservation included.
func (p *DefaultProvider) instanceCreate(ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceTemplate *instancetemplate.InstanceTemplateInfo,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
	reserved resources.VMResources,
) (_ *corev1.Node, err error) {
	log := log.FromContext(ctx).WithName("instance.instanceCreate()").WithValues("region", region, "zone", zone, "instanceType", instanceType.Name)

	newID := reserved.ID

	rollback := &launchRollback{}

	defer func() {
		if err != nil {
			rollback.run(ctx)
		}
	}()

	rollback.add(func(ctx context.Context) {
		if err := p.cloudCapacityProvider.Release(ctx, region, zone, newID); err != nil {
			log.Error(err, "failed to release capacity", "vmID", newID)
		}
	})

	px, err := p.cluster.Get(region)
	if err != nil {
		return nil, pxpool.ErrClusterNotFound
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

	reserved.DiskGBytes = uint64(size)
	reserved.StorageID = storage

	capacityType := getCapacityType(nodeClaim, instanceType, region, zone)

	comments := []string{
		"Karpenter managed instance",
		fmt.Sprintf("class=%s", nodeClass.Name),
		fmt.Sprintf("capacity-type=%s", capacityType),
	}
	comments = append(comments, reserved.DescriptionTokens...)

	// Added before the clone, which can fail after Proxmox created the VM.
	// DeleteVM stops the VM first if it is running (e.g. when creation
	// fails after Start succeeds), unlike a bare Qemu().Delete which
	// Proxmox refuses on a running guest and would leave it orphaned.
	rollback.add(func(ctx context.Context) {
		if err := p.cluster.Cluster(region).DeleteVM(ctx, &cluster.Resource{Node: zone, VMID: newID}); err != nil && !proxmoxrest.IsNotFound(err) {
			log.Error(err, "failed to delete vm", "vmID", newID)
		}
	})

	if err = cloneAndConfigureVM(ctx, px, zone, vmTemplateID, newID, cloneVMParams{
		description:      strings.Join(comments, ", "),
		pool:             nodeClass.Spec.ResourcePool,
		storage:          storage,
		resources:        &reserved,
		tags:             nodeClass.Spec.Tags,
		instanceTypeName: instanceType.Name,
		vmName:           nodeClaim.Name,
		root:             p.privileges.Root(region),
	}); err != nil {
		return nil, err
	}

	err = p.instanceNetworkSetup(ctx, rollback, region, zone, newID)
	if err != nil {
		return nil, fmt.Errorf("failed to configure networking for vm %d: %v", newID, err)
	}

	rules := buildFirewallRules(nodeClass.Spec.SecurityGroups)
	if len(rules) > 0 {
		if err = px.Nodes(zone).Qemu().Firewall().Options(newID).Update(ctx, &firewall.Options{
			Enable:    1,
			DHCP:      true,
			PolicyIn:  firewall.PolicyDrop,
			PolicyOut: firewall.PolicyAccept,
		}); err != nil {
			return nil, fmt.Errorf("failed to set firewall options for vm %d: %v", newID, err)
		}

		// Proxmox inserts each new rule at position 0, so creating them
		// last to first leaves them in order.
		for _, r := range slices.Backward(rules) {
			if err = px.Nodes(zone).Qemu().Firewall().Rules(newID).Create(ctx, firewallRuleOptions(r)); err != nil {
				return nil, fmt.Errorf("failed to create firewall rules for vm %d: %v", newID, err)
			}
		}
	}

	if nodeClass.Spec.MetadataOptions.Type == "cdrom" {
		err = p.attachCloudInitISO(ctx, rollback, nodeClaim, nodeClass, instanceType, region, zone, newID)
		if err != nil {
			return nil, fmt.Errorf("failed to attach cloud-init ISO to vm %d: %v", newID, err)
		}
	}

	log.V(1).Info("Starting VM", "vmID", newID)

	startTask, err := px.Nodes(zone).Qemu().Start(ctx, newID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start vm %d: %v", newID, err)
	}

	if err = startTask.Wait(ctx, &task.WaitOptions{Timeout: powerActionTimeout}); err != nil {
		return nil, fmt.Errorf("failed to start vm %d: %v", newID, err)
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, newID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get config of vm %d: %v", newID, err)
	}

	cpuType := ""
	if cfg.CPU != nil {
		cpuType = cfg.CPU.Type
	}

	node := &corev1.Node{
		Name: nodeClaim.Name,
		Labels: map[string]string{
			corev1.LabelTopologyRegion:     region,
			corev1.LabelTopologyZone:       zone,
			corev1.LabelInstanceTypeStable: instanceType.Name,
			karpv1.CapacityTypeLabelKey:    capacityType,
			v1alpha1.LabelInstanceFamily:   strings.Split(instanceType.Name, ".")[0],
			v1alpha1.LabelInstanceCPUType:  cpuType,
		},
		Annotations:       map[string]string{},
		CreationTimestamp: metav1.Now(),
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
	region string,
	zone string,
	vmr *cluster.Resource,
) error {
	log := log.FromContext(ctx).WithName("instance.instanceDelete()").WithValues("region", region, "zone", zone)

	px, err := p.cluster.Get(region)
	if err != nil {
		return pxpool.ErrClusterNotFound
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmr.VMID, nil)
	if err != nil {
		return fmt.Errorf("failed to get vm config for VM %d: %v", vmr.VMID, err)
	}

	if err := p.cluster.Cluster(region).DeleteVM(ctx, vmr); err != nil {
		return fmt.Errorf("cannot delete VM with id %d: %w", vmr.VMID, err)
	}

	networkValues := cloudinit.GetNetworkConfigFromVirtualMachineConfig(cfg, nil)
	for _, iface := range networkValues.Interfaces {
		for _, cidr := range iface.Address4 {
			err := p.nodeIpamProvider.ReleaseIP(cidr)
			if err != nil {
				log.Error(err, "Failed to release IP", "cidr", cidr)
			}
		}
	}

	if err := p.cloudCapacityProvider.Release(ctx, region, zone, vmr.VMID); err != nil {
		log.Error(err, "Failed to release capacity after VM deletion", "vmID", vmr.VMID)
	}

	return nil
}

// cloneVMParams groups the per-instance values cloneAndConfigureVM needs to
// clone a template and apply the new guest's resource/identity overrides.
type cloneVMParams struct {
	description      string
	pool             string
	storage          string
	resources        *resources.VMResources
	tags             []string
	instanceTypeName string
	vmName           string
	// root is whether the region's credentials are root@pam, which alone may set affinity and hugepages.
	root bool
}

// cloneAndConfigureVM clones vmTemplateID into newID, resizes its boot disk
// to params.resources.DiskGBytes, and applies the CPU/memory/NUMA/tags/SMBIOS
// overrides cloneConfigOverrides computes — the same three-step sequence
// (clone, resize, reconfigure) the old goproxmox.CloneVM performed in a
// single call.
func cloneAndConfigureVM(ctx context.Context, px *proxmoxrest.Client, zone string, vmTemplateID uint64, newID int, params cloneVMParams) error {
	full := true

	cloneTask, err := px.Nodes(zone).Qemu().Clone(ctx, int(vmTemplateID), &qemu.CloneOptions{
		NewID:       newID,
		Name:        params.vmName,
		Description: params.description,
		Full:        &full,
		Pool:        params.pool,
		Storage:     params.storage,
	})
	if err != nil {
		return fmt.Errorf("failed to clone vm template %d: %v", vmTemplateID, err)
	}

	if err = cloneTask.Wait(ctx, &task.WaitOptions{Timeout: cloneTaskTimeout}); err != nil {
		return fmt.Errorf("failed to clone vm template %d: %v", vmTemplateID, err)
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, newID, nil)
	if err != nil {
		return fmt.Errorf("failed to get config of vm %d: %v", newID, err)
	}

	bootDisk := detectBootDisk(cfg)
	if bootDisk == "" {
		return fmt.Errorf("failed to detect boot disk for vm %d", newID)
	}

	resizeTask, err := px.Nodes(zone).Qemu().Resize(ctx, newID, &qemu.ResizeOptions{
		Disk: bootDisk,
		Size: fmt.Sprintf("%dG", params.resources.DiskGBytes),
	})
	if err != nil {
		return fmt.Errorf("failed to resize disk %s for vm %d: %v", bootDisk, newID, err)
	}

	if err = resizeTask.Wait(ctx, &task.WaitOptions{Timeout: cloneTaskTimeout}); err != nil {
		return fmt.Errorf("failed to resize disk %s for vm %d: %v", bootDisk, newID, err)
	}

	vmConfig := cloneConfigOverrides(params, newID, cfg)

	if err = px.Nodes(zone).Qemu().UpdateConfig(ctx, newID, vmConfig); err != nil {
		return fmt.Errorf("unable to configure vm: %w", err)
	}

	return nil
}

// detectBootDisk returns the name of the cloned VM's boot disk, checking
// virtio, scsi, sata, and ide buses in order of preference — the same
// priority the old goproxmox.CloneVM used.
func detectBootDisk(cfg *qemu.Config) string {
	switch {
	case cfg.VirtIO[0].File != "":
		return "virtio0"
	case cfg.SCSI[0].File != "":
		return "scsi0"
	case cfg.SATA[0].File != "":
		return "sata0"
	case cfg.IDE[0].File != "":
		return "ide0"
	default:
		return ""
	}
}

// cloneConfigOverrides builds the typed config update applied to a freshly
// cloned VM vmid, whose cloned config is cfg: CPU/memory/affinity/NUMA
// pinning from params.resources, the node class's
// tags, and two cosmetic tweaks the old goproxmox.CloneVM also applied
// (SMBIOS serial/SKU identifying the instance, and multi-queue virtio NICs
// sized to the vCPU count).
//
// Only root@pam may set affinity and hugepages (ADR 0003), so without root
// (params.root false) neither is written: a pinned VM's host CPUs travel only in the `affinity=`
// description token, which the proxmox-scheduler applies, and hugepages come
// from the template. numaN, hostnodes and policy included, needs no root and
// is written either way.
func cloneConfigOverrides(params cloneVMParams, vmid int, cfg *qemu.Config) *qemu.Config {
	opt := params.resources
	update := &qemu.Config{}

	if opt.CPUs != 0 {
		update.Cores = new(opt.CPUs)
	}

	if params.root && !opt.CPUSet.IsEmpty() {
		update.Affinity = opt.CPUSet.String()
	}

	if opt.Memory != 0 {
		update.Memory = &qemu.Memory{Current: new(int(opt.Memory / 1024 / 1024))}
	}

	if len(opt.NUMANodes) > 0 {
		update.NUMAEnabled = new(true)
		update.NUMA = make(map[int]qemu.NUMA, len(opt.NUMANodes))

		idx := 0

		for hostNode, state := range opt.NUMANodes {
			policy := state.Policy
			if !slices.Contains([]string{"preferred", "bind", "interleave"}, policy) {
				policy = "preferred"
			}

			update.NUMA[idx] = qemu.NUMA{
				CPUIDs:    strings.Split(state.CPUs, ","),
				HostNodes: []string{strconv.Itoa(hostNode)},
				Memory:    new(int(state.Memory)),
				Policy:    policy,
			}
			idx++
		}
	}

	if params.root && opt.HugePageSize > 0 {
		update.Hugepages = new(strconv.FormatUint(opt.HugePageSize/zonecapacity.MiB, 10))
	}

	if len(params.tags) > 0 {
		vmTags := qemu.Tags(params.tags)
		update.Tags = &vmTags
	}

	smbios := qemu.SMBios1{}
	if cfg.SMBios1 != nil {
		smbios = *cfg.SMBios1
	}

	// The inherited family/manufacturer/product/version are only already
	// base64-encoded if the template itself had smbios1's base64 flag set.
	// Since we force base64=1 below for the SKU/Serial we add, any plaintext
	// inherited here must be encoded too, or Proxmox will try (and fail) to
	// base64-decode it. UUID is never base64 encoded by Proxmox.
	if smbios.Base64 == nil || !*smbios.Base64 {
		smbios.Family = base64EncodeIfNotEmpty(smbios.Family)
		smbios.Manufacturer = base64EncodeIfNotEmpty(smbios.Manufacturer)
		smbios.Product = base64EncodeIfNotEmpty(smbios.Product)
		smbios.Version = base64EncodeIfNotEmpty(smbios.Version)
	}

	smbios.Base64 = new(true)
	smbios.SKU = base64.StdEncoding.EncodeToString([]byte(params.instanceTypeName))
	smbios.Serial = base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "h=%s;i=%d", params.vmName, vmid))
	update.SMBios1 = &smbios

	if len(cfg.Net) > 0 {
		update.Net = make(map[int]qemu.Net, len(cfg.Net))
		for idx, n := range cfg.Net {
			n.Queues = new(opt.CPUs)
			update.Net[idx] = n
		}
	}

	return update
}

// base64EncodeIfNotEmpty base64-encodes s, leaving an empty string unchanged
// so an unset SMBIOS field stays unset instead of becoming an encoded empty
// string.
func base64EncodeIfNotEmpty(s string) string {
	if s == "" {
		return s
	}

	return base64.StdEncoding.EncodeToString([]byte(s))
}
