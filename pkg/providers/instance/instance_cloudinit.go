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
	"path/filepath"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/tasks"
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

// cloudInitDrive is the CD-ROM slot the custom NoCloud cloud-init ISO is
// attached to.
const cloudInitDrive = "ide2"

// cloudInitVolumeLabel is the ISO9660 volume label cloud-init's NoCloud
// datasource looks for.
const cloudInitVolumeLabel = "cidata"

// cloudInitISOBlockSize is the sector size used when building the
// cloud-init ISO, matching the old client's default.
const cloudInitISOBlockSize = 2048

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
	px, err := p.cluster.Get(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox client with region name %s: %v", region, err)
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmID, nil)
	if err != nil {
		return fmt.Errorf("unable to get vm config for vm %d: %w", vmID, err)
	}

	userdata, metadata, vendordata, networkconfig, err := p.generateCloudInitVars(ctx, nodeClaim, nodeClass, instanceTemplate, instanceType, region, zone, vmID, cfg)
	if err != nil {
		return fmt.Errorf("failed to generate cloud-init for vm %d in region %s: %v", vmID, region, err)
	}

	isoName := fmt.Sprintf("vm-%d-custom-cloudinit.iso", vmID)

	isoPath, cleanup, err := buildCloudInitISO(isoName, userdata, metadata, vendordata, networkconfig)
	defer cleanup()

	if err != nil {
		return fmt.Errorf("failed to build cloud-init ISO for vm %d: %v", vmID, err)
	}

	storageID, err := findISOStorage(ctx, px, zone)
	if err != nil {
		return fmt.Errorf("failed to find an iso-capable storage on node %s in region %s: %v", zone, region, err)
	}

	isoFile, err := os.Open(isoPath)
	if err != nil {
		return fmt.Errorf("failed to open cloud-init ISO for vm %d: %v", vmID, err)
	}
	defer isoFile.Close()

	uploadUPID, err := px.Nodes(zone).Storage().Upload(ctx, storageID, &storage.UploadOptions{
		Content:  "iso",
		Filename: isoName,
		File:     isoFile,
	})
	if err != nil {
		return fmt.Errorf("failed to upload cloud-init ISO for vm %d: %v", vmID, err)
	}

	if uploadUPID != "" {
		if err = px.Nodes(zone).Tasks().Wait(ctx, uploadUPID, &tasks.WaitOptions{Timeout: cloneTaskTimeout}); err != nil {
			return fmt.Errorf("failed to upload cloud-init ISO for vm %d: %v", vmID, err)
		}
	}

	attachUPID, err := px.Nodes(zone).Qemu().AttachISO(ctx, vmID, &qemu.AttachISOOptions{
		Drive:  cloudInitDrive,
		Volume: fmt.Sprintf("%s:iso/%s", storageID, isoName),
	})
	if err != nil {
		return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
	}

	if attachUPID != "" {
		if err = px.Nodes(zone).Tasks().Wait(ctx, attachUPID, &tasks.WaitOptions{Timeout: powerActionTimeout}); err != nil {
			return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
		}
	}

	return nil
}

// detachCloudInitISO ejects and removes the custom cloud-init ISO attached
// by attachCloudInitISO, if any. Rather than tracking attachment with a
// marker tag (the old client's approach), it re-reads the guest's current
// CD-ROM slot and acts only if something is actually mounted there — see
// docs/migration.md §7.2 for why.
func (p *DefaultProvider) detachCloudInitISO(
	ctx context.Context,
	region string,
	zone string,
	vmID int,
) error {
	px, err := p.cluster.Get(region)
	if err != nil {
		return fmt.Errorf("failed to get proxmox client with region name %s: %v", region, err)
	}

	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmID, nil)
	if err != nil {
		return fmt.Errorf("unable to get vm config for vm %d: %w", vmID, err)
	}

	drive, ok := cfg.IDE[2]
	if !ok || drive.File == "" || drive.File == "none" {
		return nil
	}

	storageID, volume, ok := strings.Cut(drive.File, ":")
	if !ok {
		return nil
	}

	detachUPID, err := px.Nodes(zone).Qemu().DetachISO(ctx, vmID, cloudInitDrive)
	if err != nil {
		return fmt.Errorf("failed to detach cloud-init ISO from vm %d in region %s: %v", vmID, region, err)
	}

	if detachUPID != "" {
		if err = px.Nodes(zone).Tasks().Wait(ctx, detachUPID, &tasks.WaitOptions{Timeout: powerActionTimeout}); err != nil {
			return fmt.Errorf("failed to detach cloud-init ISO from vm %d in region %s: %v", vmID, region, err)
		}
	}

	deleteUPID, err := px.Nodes(zone).Storage().Content(storageID).Delete(ctx, volume, 0)
	if err != nil {
		return fmt.Errorf("failed to delete cloud-init ISO %s from vm %d in region %s: %v", drive.File, vmID, region, err)
	}

	if deleteUPID != "" {
		if err = px.Nodes(zone).Tasks().Wait(ctx, deleteUPID, &tasks.WaitOptions{Timeout: powerActionTimeout}); err != nil {
			return fmt.Errorf("failed to delete cloud-init ISO %s from vm %d in region %s: %v", drive.File, vmID, region, err)
		}
	}

	return nil
}

// findISOStorage returns the id of the first enabled storage on zone that
// can hold "iso" content, matching the old client's node.StorageISO
// (first enabled storage whose content list includes "iso").
func findISOStorage(ctx context.Context, px *proxmoxrest.Client, zone string) (string, error) {
	storages, err := px.Nodes(zone).Storage().List(ctx, &storage.ListOptions{Content: []string{"iso"}})
	if err != nil {
		return "", err
	}

	for _, s := range storages {
		if s.Enabled {
			return s.Storage, nil
		}
	}

	return "", fmt.Errorf("no enabled iso-capable storage found on node %s", zone)
}

// buildCloudInitISO renders userdata/metadata/vendordata/networkconfig into
// a NoCloud-layout ISO9660 image at a temp path named filename, returning
// that path and a cleanup func that removes it. cleanup is always safe to
// call, even if err != nil.
func buildCloudInitISO(filename, userdata, metadata, vendordata, networkconfig string) (isopath string, cleanup func(), err error) {
	isopath = filepath.Join(os.TempDir(), filename)
	cleanup = func() { os.Remove(isopath) }

	isoFile, err := os.Create(isopath)
	if err != nil {
		return "", cleanup, err
	}

	if err := isoFile.Close(); err != nil {
		return "", cleanup, err
	}

	iso, err := file.OpenFromPath(isopath, false)
	if err != nil {
		return "", cleanup, err
	}

	defer func() {
		if cerr := iso.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	fs, err := iso9660.Create(iso, 0, 0, cloudInitISOBlockSize, "")
	if err != nil {
		return "", cleanup, err
	}

	if err = fs.Mkdir("/"); err != nil {
		return "", cleanup, err
	}

	files := map[string]string{
		"user-data": userdata,
		"meta-data": metadata,
	}
	if vendordata != "" {
		files["vendor-data"] = vendordata
	}

	if networkconfig != "" {
		files["network-config"] = networkconfig
	}

	for name, content := range files {
		rw, ferr := fs.OpenFile("/"+name, os.O_CREATE|os.O_RDWR)
		if ferr != nil {
			return "", cleanup, ferr
		}

		if _, ferr = rw.Write([]byte(content)); ferr != nil {
			return "", cleanup, ferr
		}

		// The file handle must be closed before Finalize so its size is
		// recorded correctly (go-diskfs's Joliet finalization reads it back).
		if ferr = rw.Close(); ferr != nil {
			return "", cleanup, ferr
		}
	}

	if err = fs.Finalize(iso9660.FinalizeOptions{
		RockRidge:        true,
		Joliet:           true,
		VolumeIdentifier: cloudInitVolumeLabel,
	}); err != nil {
		return "", cleanup, err
	}

	return isopath, cleanup, nil
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
	vmID int,
	cfg *qemu.Config,
) (string, string, string, string, error) {
	systemNamespace := strings.TrimSpace(os.Getenv("SYSTEM_NAMESPACE"))
	if systemNamespace == "" {
		systemNamespace = "kube-system"
	}

	version, err := p.kubernetesInterface.Discovery().ServerVersion()
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to get kubernetes server version: %v", err)
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

	uuid := ""
	if cfg.SMBios1 != nil {
		uuid = cfg.SMBios1.UUID
	}

	metadataValues := cloudinit.MetaData{
		Hostname:     nodeClaim.Name,
		InstanceID:   fmt.Sprintf("%d", vmID),
		InstanceType: instanceType.Name,
		InstanceUUID: uuid,
		ProviderID:   provider.GetProviderID(region, vmID),
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

	networkValues := cloudinit.GetNetworkConfigFromVirtualMachineConfig(cfg, ifaces)

	userdataValues := UserDataValues{
		Metadata: metadataValues,
		Network:  networkValues,
		Resources: Resources{
			CPU:    instanceType.Capacity.Cpu().Value(),
			Memory: instanceType.Capacity.Memory().Value(),
		},
		Kubernetes: Kubernetes{
			Version:              version.String(),
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

	for name, quantity := range instanceType.Capacity {
		if size, ok := strings.CutPrefix(string(name), corev1.ResourceHugePagesPrefix); ok {
			switch size {
			case "1Gi":
				userdataValues.Resources.Hugepages1Gi = int(quantity.Value() / (1024 * 1024 * 1024))
			case "2Mi":
				userdataValues.Resources.Hugepages2Mi = int(quantity.Value() / (2 * 1024 * 1024))
			}
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
