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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"
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

	userdata, metadata, vendordata, networkconfig, err := p.generateCloudInitVars(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone, vm)
	if err != nil {
		return fmt.Errorf("failed to generate cloud-init for vm %d in region %s: %v", vmID, region, err)
	}

	err = vm.CloudInit(ctx, "ide2", userdata, metadata, vendordata, networkconfig)
	if err != nil {
		return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
	}

	return nil
}

func (p *DefaultProvider) detachCloudInitISO(
	ctx context.Context,
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

	if vm.HasTag(proxmox.MakeTag(proxmox.TagCloudInit)) {
		err = vm.UnmountCloudInitISO(ctx, "ide2")
		if err != nil {
			return fmt.Errorf("failed to detach cloud-init ISO from vm %d in region %s: %v", vmID, region, err)
		}

		vm.RemoveTag(ctx, proxmox.MakeTag(proxmox.TagCloudInit))
	}

	return nil
}

func applyKubernetesConfiguration(
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceType *cloudprovider.InstanceType,
) *KubeletConfiguration {
	kubeletConfig := &KubeletConfiguration{}

	if nodeClass.Spec.KubeletConfiguration != nil {
		data, _ := json.Marshal(nodeClass.Spec.KubeletConfiguration) //nolint: errchkjson
		json.Unmarshal(data, kubeletConfig)
	}

	if instanceType.Overhead != nil {
		if len(instanceType.Overhead.KubeReserved) > 0 {
			kubeletConfig.KubeReserved = requestsToMap(instanceType.Overhead.KubeReserved)
		}

		if len(instanceType.Overhead.SystemReserved) > 0 {
			kubeletConfig.SystemReserved = requestsToMap(instanceType.Overhead.SystemReserved)
		}

		if len(instanceType.Overhead.EvictionThreshold) > 0 && instanceType.Overhead.EvictionThreshold.Memory().String() != "" {
			kubeletConfig.EvictionHard = DefaultEvictionHard
			kubeletConfig.EvictionHard["memory.available"] = instanceType.Overhead.EvictionThreshold.Memory().String()
		}
	}

	return kubeletConfig
}

func requestsToMap(requests corev1.ResourceList) map[string]string {
	m := make(map[string]string)

	cpu := requests.Cpu().MilliValue()
	if cpu > 0 {
		m[string(corev1.ResourceCPU)] = fmt.Sprintf("%dm", cpu)
	}

	mem := requests.Memory().Value() / (1024 * 1024)
	if mem > 0 {
		m[string(corev1.ResourceMemory)] = fmt.Sprintf("%dMi", mem)
	}

	storage := requests.StorageEphemeral().Value() / (1024 * 1024 * 1024)
	if storage > 0 {
		m[string(corev1.ResourceEphemeralStorage)] = fmt.Sprintf("%dGi", storage)
	}

	return m
}

func (p *DefaultProvider) generateCloudInitVars(
	ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	_ *instancetemplate.InstanceTemplateInfo,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
	vm *proxmox.VirtualMachine,
) (string, string, string, string, error) {
	systemNamespace := strings.TrimSpace(os.Getenv("SYSTEM_NAMESPACE"))
	if systemNamespace == "" {
		systemNamespace = "kube-system"
	}

	cm, err := p.kubernetesInterface.CoreV1().ConfigMaps(systemNamespace).Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to get configmap %s/kube-root-ca.crt: %v", systemNamespace, err)
	}

	rootCA := cm.Data["ca.crt"]

	secretKey := nodeClass.Spec.MetadataOptions.TemplatesRef
	secret, err := p.kubernetesInterface.CoreV1().Secrets(secretKey.Namespace).Get(ctx, secretKey.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to get secret %s/%s: %v", secretKey.Namespace, secretKey.Name, err)
	}

	values := map[string]string{}
	if nodeClass.Spec.MetadataOptions.ValuesRef != nil && nodeClass.Spec.MetadataOptions.ValuesRef.Name != "" && nodeClass.Spec.MetadataOptions.ValuesRef.Namespace != "" {
		valuesKey := nodeClass.Spec.MetadataOptions.ValuesRef
		secret, err := p.kubernetesInterface.CoreV1().Secrets(valuesKey.Namespace).Get(ctx, valuesKey.Name, metav1.GetOptions{})
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to get secret %s/%s: %v", valuesKey.Namespace, valuesKey.Name, err)
		}

		for k, v := range secret.Data {
			values[k] = string(v)
		}
	}

	bootstrapToken, err := p.kubernetesBootstrapProvider.CreateToken(ctx, nodeClaim)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to create bootstrap token: %v", err)
	}

	metadataValues := cloudinit.MetaData{
		Hostname:     nodeClaim.Name,
		InstanceID:   fmt.Sprintf("%d", vm.VMID),
		InstanceType: instanceType.Name,
		InstanceUUID: goproxmox.GetVMUUID(vm),
		ProviderID:   provider.GetProviderID(region, int(vm.VMID)),
		Region:       region,
		Zone:         zone,
		Tags:         nodeClass.Spec.Tags,
		NodeClass:    nodeClass.Name,
	}

	ifaces := map[string]cloudcapacity.NetworkIfaceInfo{}

	net := p.cloudCapacityProvider.GetNetwork(region, zone)
	if net != nil {
		ifaces = net.Ifaces
	}

	networkValues := cloudinit.GetNetworkConfigFromVirtualMachineConfig(vm.VirtualMachineConfig, ifaces)

	userdataValues := UserDataValues{
		Metadata: metadataValues,
		Network:  networkValues,
		Kubernetes: Kubernetes{
			RootCA:               rootCA,
			BootstrapToken:       bootstrapToken,
			KubeletConfiguration: applyKubernetesConfiguration(nodeClass, instanceType),
		},
		Values: values,
	}
	userdataValues.Kubernetes.KubeletConfiguration.ProviderID = metadataValues.ProviderID

	if len(userdataValues.Kubernetes.KubeletConfiguration.RegisterWithTaints) == 0 {
		userdataValues.Kubernetes.KubeletConfiguration.RegisterWithTaints = []KubernetesTaint{
			{
				Key:    karpv1.UnregisteredTaintKey,
				Effect: corev1.TaintEffectNoExecute,
			},
		}
	}

	userdata := string(secret.Data["user-data"])
	if userdata == "" {
		userdata = cloudinit.DefaultUserdata
	}

	userdata, err = cloudinit.ExecuteTemplate(userdata, userdataValues)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to execute userdata template: %v", err)
	}

	metadata := string(secret.Data["meta-data"])
	if metadata == "" {
		metadata = cloudinit.DefaultMetadata
	}

	metadata, err = cloudinit.ExecuteTemplate(metadata, metadataValues)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to execute metadata template: %v", err)
	}

	vendordata := string(secret.Data["vendor-data"])
	if vendordata != "" {
		vendordata, err = cloudinit.ExecuteTemplate(vendordata, metadataValues)
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to execute vendor-data template: %v", err)
		}
	}

	networkconfig := string(secret.Data["network-config"])
	if networkconfig == "" {
		networkconfig = cloudinit.DefaultNetworkV2
	}

	if networkconfig != "" {
		networkconfig, err = cloudinit.ExecuteTemplate(networkconfig, networkValues)
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to execute network-config template: %v", err)
		}
	}

	return userdata, metadata, vendordata, networkconfig, nil
}
