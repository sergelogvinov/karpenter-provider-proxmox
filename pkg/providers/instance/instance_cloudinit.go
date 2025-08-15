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
	"strconv"
	"strings"

	"github.com/luthermonson/go-proxmox"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func (p *DefaultProvider) attachCloudInitISO(
	ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceTemplate *instancetemplate.InstanceTemplateInfo,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
	vmID int,
) error {
	px, err := p.cluster.GetProxmoxCluster(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox cluster with region name %s: %v", region, err)
	}

	node, err := px.Node(ctx, zone)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", zone, err)
	}

	vm, err := node.VirtualMachine(ctx, vmID)
	if err != nil {
		return fmt.Errorf("unable to find vm with id %d: %w", vmID, err)
	}

	userdata, metadata, vendordata, networkconfig, err := p.generateCloudInit(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone, vm)
	if err != nil {
		return fmt.Errorf("failed to generate cloud-init for vm %d in region %s: %v", vmID, region, err)
	}

	err = vm.CloudInit(ctx, "ide2", userdata, metadata, vendordata, networkconfig)
	if err != nil {
		return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
	}

	return nil
}

func networkFromInstanceConfig(vmc *proxmox.VirtualMachineConfig) cloudinit.NetworkConfig {
	network := cloudinit.NetworkConfig{}

	if vmc.Nameserver != "" {
		network.NameServers = strings.Split(vmc.Nameserver, " ")
	}

	if vmc.Searchdomain != "" {
		network.SearchDomains = strings.Split(vmc.Searchdomain, " ")
	}

	nets := vmc.MergeNets()
	if len(nets) == 0 {
		return network
	}

	ipconfigs := vmc.MergeIPConfigs()

	for i, net := range nets {
		inx, _ := strconv.Atoi(strings.TrimPrefix(i, "net"))
		params := strings.Split(net, ",")

		iface := cloudinit.InterfaceConfig{
			Name:    fmt.Sprintf("eth%d", inx),
			MacAddr: strings.Split(params[0], "=")[1],
		}

		if ipparams, ok := ipconfigs[fmt.Sprintf("ipconfig%d", inx)]; ok {
			for _, p := range strings.Split(ipparams, ",") {
				parts := strings.SplitN(p, "=", 2)
				value := parts[1]

				switch parts[0] {
				case "ip":
					if value == "dhcp" {
						iface.DHCPv4 = true
					} else {
						iface.Address4 = []string{value}
					}
				case "gw":
					iface.Gateway4 = value
				case "ip6":
					switch value {
					case "dhcp":
						iface.DHCPv6 = true
					case "auto":
					default:
						iface.Address6 = []string{value}
					}
				case "gw6":
					iface.Gateway6 = value
				}
			}
		}

		network.Interfaces = append(network.Interfaces, iface)
	}

	return network
}

func (p *DefaultProvider) generateCloudInit(
	ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	_ *instancetemplate.InstanceTemplateInfo,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
	vm *proxmox.VirtualMachine,
) (string, string, string, string, error) {
	secretKey := nodeClass.Spec.MetadataOptions.SecretRef

	secret, err := p.kubernetesInterface.CoreV1().Secrets(secretKey.Namespace).Get(ctx, secretKey.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to get secret %s/%s: %v", secretKey.Namespace, secretKey.Name, err)
	}

	data := cloudinit.CloudInitData{
		Hostname:     nodeClaim.Name,
		InstanceID:   fmt.Sprintf("%d", vm.VMID),
		InstanceType: instanceType.Name,
		ProviderID:   provider.GetProviderID(region, int(vm.VMID)),
		Region:       region,
		Zone:         zone,
	}

	userdata := string(secret.Data["user-data"])
	if userdata == "" {
		userdata = cloudinit.DefaultUserdata
	}

	userdata, err = cloudinit.ExecuteTemplate(userdata, data)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to execute userdata template: %v", err)
	}

	metadata := string(secret.Data["meta-data"])
	if metadata == "" {
		metadata = cloudinit.DefaultMetadata
	}

	metadata, err = cloudinit.ExecuteTemplate(metadata, data)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to execute metadata template: %v", err)
	}

	vendordata := string(secret.Data["vendor-data"])
	if vendordata != "" {
		vendordata, err = cloudinit.ExecuteTemplate(vendordata, data)
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to execute vendor-data template: %v", err)
		}
	}

	networkconfig := string(secret.Data["network-config"])
	if networkconfig == "" {
		networkconfig = cloudinit.DefaultNetworkV1
	}

	if networkconfig != "" {
		network := networkFromInstanceConfig(vm.VirtualMachineConfig)

		networkconfig, err = cloudinit.ExecuteTemplate(networkconfig, network)
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to execute network-config template: %v", err)
		}
	}

	return userdata, metadata, vendordata, networkconfig, nil
}
