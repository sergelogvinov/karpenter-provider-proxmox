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
	"math"
	"sort"

	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pxapi "github.com/Telmate/proxmox-api-go/proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	cluster "github.com/sergelogvinov/proxmox-cloud-controller-manager/pkg/cluster"
	ccmprovider "github.com/sergelogvinov/proxmox-cloud-controller-manager/pkg/provider"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider struct {
	cluster               *cluster.Cluster
	cloudcapacityProvider *cloudcapacity.Provider
}

func NewProvider(cloudcapacityProvider *cloudcapacity.Provider) (*Provider, error) {
	cfg, err := cluster.ReadCloudConfigFromFile("cloud.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	cluster, err := cluster.NewCluster(&cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxmox cluster client: %v", err)
	}

	return &Provider{
		cluster:               cluster,
		cloudcapacityProvider: cloudcapacityProvider,
	}, nil
}

// Create an instance given the constraints.
func (p *Provider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.ProxmoxNodeClass, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	instanceTypes = orderInstanceTypesByPrice(instanceTypes, scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...))
	instanceType := instanceTypes[0]

	log.FromContext(ctx).V(1).Info("Requirements", "nodeClaim", nodeClaim.Spec.Requirements, "nodeClass", nodeClass.Spec)

	region := nodeClass.Spec.Region
	if region == "" {
		requestedRegion := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyRegion)
		if len(requestedRegion.Values()) == 0 {
			region = "region-1"
		} else {
			region = requestedRegion.Any()
		}
	}

	requestedZones := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Get(corev1.LabelTopologyZone)
	zone := requestedZones.Any()
	if len(requestedZones.Values()) == 0 || zone == "" {
		zones := p.cloudcapacityProvider.GetAvailableZones(instanceType.Capacity)

		if len(zones) == 0 {
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no capacity zone available"))
		}

		zone = zones[0]
	}

	cl, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster: %v", err)
	}

	id, err := cl.GetNextID(50000)
	if err != nil {
		return nil, fmt.Errorf("failed to get next id: %v", err)
	}

	vmrs, err := cl.GetVmRefsByName(nodeClass.Spec.Template)
	if err != nil || len(vmrs) == 0 {
		return nil, fmt.Errorf("failed to get vm template: %v", err)
	}

	var template *pxapi.VmRef

	for _, vmr := range vmrs {
		if vmr.Node() == zone {
			template = vmr
			break
		}
	}

	if template == nil {
		return nil, fmt.Errorf("failed to find template for zone %s", zone)
	}

	// Create a new VM
	vm := map[string]interface{}{}
	vm["vmid"] = template.VmId()
	vm["node"] = template.Node()
	vm["newid"] = id

	vm["name"] = nodeClaim.Name
	vm["description"] = fmt.Sprintf("Karpeneter, class=%s", nodeClass.Name)
	vm["full"] = true
	vm["storage"] = nodeClass.Spec.BlockDevicesStorageID

	_, err = cl.CloneQemuVm(template, vm)
	if err != nil {
		return nil, fmt.Errorf("failed to create vm: %v", err)
	}

	vmr := pxapi.NewVmRef(id)
	vmr.SetNode(zone)
	vmr.SetVmType("qemu")

	// FIXME: Defer delete vm if failed
	defer func() {
		if err != nil {
			if _, err := cl.DeleteVm(vmr); err != nil {
				fmt.Printf("failed to delete vm %d: %v", vmr.VmId(), err)
			}
		}
	}()

	_, err = cl.ResizeQemuDiskRaw(vmr, "scsi0", fmt.Sprintf("%dG", instanceType.Capacity.StorageEphemeral().Value()/1024/1024/1024))
	if err != nil {
		return nil, fmt.Errorf("failed to resize disk: %v", err)
	}

	config, err := cl.GetVmConfig(vmr)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm config: %v", err)
	}

	smbios1 := config["smbios1"].(string)
	vmParams := map[string]interface{}{
		"memory": fmt.Sprintf("%d", instanceType.Capacity.Memory().Value()/1024/1024),
		"cores":  instanceType.Capacity.Cpu().String(),
		"smbios1": fmt.Sprintf("%s,serial=%s,sku=%s,base64=1", smbios1,
			base64.StdEncoding.EncodeToString([]byte("h="+nodeClaim.Name+";i="+strconv.Itoa(id))),
			base64.StdEncoding.EncodeToString([]byte(instanceType.Name)),
		),
	}

	if len(nodeClass.Spec.Tags) > 0 {
		vmParams["tags"] = strings.Join(nodeClass.Spec.Tags, ";")
	}

	err = applyInstanceOptimization(config, vmParams, instanceType)
	if err != nil {
		return nil, fmt.Errorf("failed to apply instance optimization: %v", err)
	}

	log.FromContext(ctx).V(1).Info("Mudify VM", "vmParams", vmParams)

	_, err = cl.SetVmConfig(vmr, vmParams)
	if err != nil {
		return nil, fmt.Errorf("failed to update disk: %v, vmParams=%+v", err, vmParams)
	}

	_, err = cl.StartVm(vmr)
	if err != nil {
		return nil, fmt.Errorf("failed to start vm %d: %v", vmr.VmId(), err)
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
			ProviderID: ccmprovider.GetProviderID(region, vmr),
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
	vmr, region, err := ccmprovider.ParseProviderID(providerID)
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

	cl, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxmox cluster: %v", err)
	}

	vmInfo, err := cl.GetVmInfo(vmr)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %v", err)
	}

	node.ObjectMeta.Name = vmInfo["name"].(string)
	node.ObjectMeta.Labels = map[string]string{
		corev1.LabelTopologyRegion:            region,
		corev1.LabelTopologyZone:              vmr.Node(),
		karpv1.CapacityTypeLabelKey:           karpv1.CapacityTypeOnDemand,
		v1alpha1.LabelInstanceCPUManufacturer: "kvm64",
	}

	return node, nil
}

func (p *Provider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	region := nodeClaim.Labels[corev1.LabelTopologyRegion]
	if region == "" {
		region = "region-1"
	}

	zone := nodeClaim.Labels[corev1.LabelTopologyZone]
	if zone == "" {
		zone = "rnd-1"
	}

	cl, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster: %v", err)
	}

	vmID, err := ccmprovider.GetVMID(nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("failed to get vm id from providerID: %v", err)
	}

	vmr := pxapi.NewVmRef(vmID)
	vmr.SetNode(zone)
	vmr.SetVmType("qemu")

	if _, err := cl.GetVmInfo(vmr); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}

		return fmt.Errorf("failed to get vm %d: %v", vmr.VmId(), err)
	}

	params := map[string]interface{}{}
	params["purge"] = "1"

	if _, err := cl.StopVm(vmr); err != nil {
		return fmt.Errorf("failed to stop vm %d: %v", vmr.VmId(), err)
	}

	if _, err := cl.DeleteVmParams(vmr, params); err != nil {
		return fmt.Errorf("failed to delete vm %d: %v", vmr.VmId(), err)
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

func applyInstanceOptimization(config map[string]interface{}, vmParams map[string]interface{}, instanceType *cloudprovider.InstanceType) error {
	// Network optimization, set queues to the number of vCPUs
	for i := 0; i <= 10; i++ {
		net, ok := config[fmt.Sprintf("net%d", i)].(string)
		if ok && net != "" {
			options := map[string]string{}

			params := strings.Split(net, ",")
			for _, param := range params {
				kv := strings.Split(param, "=")
				if len(kv) == 2 && options[kv[0]] == "" {
					options[kv[0]] = kv[1]
				}
			}

			options["queues"] = instanceType.Capacity.Cpu().String()

			opt := make([]string, 0, len(options))
			for k := range options {
				opt = append(opt, fmt.Sprintf("%s=%s", k, options[k]))
			}

			vmParams[fmt.Sprintf("net%d", i)] = strings.Join(opt, ",")
		}
	}

	return nil
}
