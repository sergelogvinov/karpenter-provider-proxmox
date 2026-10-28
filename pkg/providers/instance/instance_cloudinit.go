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
	"maps"
	"os"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"

	proxmoxrest "github.com/sergelogvinov/go-proxmox-rest"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/go-proxmox-rest/task"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	provider "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
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

// cloudInitInputs is everything renderCloudInit needs: the claim and where
// its VM runs, plus what gatherCloudInitInputs fetched from Kubernetes and
// Proxmox.
type cloudInitInputs struct {
	NodeClaim    *karpv1.NodeClaim
	NodeClass    *v1alpha1.ProxmoxNodeClass
	InstanceType *cloudprovider.InstanceType
	Region       string
	Zone         string
	VMID         int

	// InstanceUUID is the VM's SMBIOS UUID.
	InstanceUUID string
	// Network is the VM's network config as read from its Proxmox config.
	Network cloudinit.NetworkConfig

	KubernetesVersion string
	RootCA            string
	BootstrapToken    string
	// Templates is the templates secret's data. Each of user-data,
	// meta-data, vendor-data and network-config is optional.
	Templates map[string][]byte
	// Values is the values secret's data, exposed to templates as .Values.
	Values map[string]string
}

// cloudInitFiles is a rendered NoCloud file set. An empty VendorData or
// NetworkConfig is left off the ISO.
type cloudInitFiles struct {
	UserData      string
	MetaData      string
	VendorData    string
	NetworkConfig string
}

// attachCloudInitISO renders the VM's cloud-init, uploads it as an ISO and
// mounts it in the VM's cloud-init CD-ROM slot. Removing the ISO is added
// to rollback before the upload starts.
func (p *DefaultProvider) attachCloudInitISO(
	ctx context.Context,
	rollback *launchRollback,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
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

	inputs, err := p.gatherCloudInitInputs(ctx, nodeClaim, nodeClass, instanceType, region, zone, vmID, cfg)
	if err != nil {
		return fmt.Errorf("failed to gather cloud-init inputs for vm %d in region %s: %v", vmID, region, err)
	}

	files, err := renderCloudInit(inputs)
	if err != nil {
		return fmt.Errorf("failed to render cloud-init for vm %d in region %s: %v", vmID, region, err)
	}

	isoName := fmt.Sprintf("vm-%d-custom-cloudinit.iso", vmID)

	isoPath, cleanup, err := buildCloudInitISO(files)
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

	volid := fmt.Sprintf("%s:iso/%s", storageID, isoName)

	// Added before the upload, which can still finish after its wait times out.
	rollback.add(func(ctx context.Context) {
		cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmID, nil)
		if err != nil {
			cfg = &qemu.Config{}
		}

		if err := removeCloudInitISO(ctx, px, zone, vmID, cfg, volid); err != nil {
			log.FromContext(ctx).Error(err, "failed to remove cloud-init ISO", "vmID", vmID, "volume", volid)
		}
	})

	uploadTask, err := px.Nodes(zone).Storage().Upload(ctx, storageID, &storage.UploadOptions{
		Content:  "iso",
		Filename: isoName,
		File:     isoFile,
	})
	if err != nil {
		return fmt.Errorf("failed to upload cloud-init ISO for vm %d: %v", vmID, err)
	}

	if err = uploadTask.Wait(ctx, &task.WaitOptions{Timeout: cloneTaskTimeout}); err != nil {
		return fmt.Errorf("failed to upload cloud-init ISO for vm %d: %v", vmID, err)
	}

	attachTask, err := px.Nodes(zone).Qemu().AttachISO(ctx, vmID, &qemu.AttachISOOptions{
		Drive:  cloudInitDrive,
		Volume: volid,
	})
	if err != nil {
		return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
	}

	if err = attachTask.Wait(ctx, &task.WaitOptions{Timeout: powerActionTimeout}); err != nil {
		return fmt.Errorf("failed to attach cloud-init ISO to vm %d in region %s: %v", vmID, region, err)
	}

	return nil
}

// detachCloudInitISO ejects and removes the custom cloud-init ISO attached
// by attachCloudInitISO, if any. Rather than tracking attachment with a
// marker tag (the old client's approach), it re-reads the guest's current
// CD-ROM slot and acts only if something is actually mounted there — see
// docs/migration.md §7.2 for why.
func detachCloudInitISO(ctx context.Context, px *proxmoxrest.Client, zone string, vmID int) error {
	cfg, err := px.Nodes(zone).Qemu().Config(ctx, vmID, nil)
	if err != nil {
		return fmt.Errorf("unable to get vm config for vm %d: %w", vmID, err)
	}

	drive, ok := cfg.IDE[2]
	if !ok || drive.File == "" || drive.File == "none" || !strings.Contains(drive.File, ":") {
		return nil
	}

	return removeCloudInitISO(ctx, px, zone, vmID, cfg, drive.File)
}

// removeCloudInitISO ejects volid from the VM's cloud-init CD-ROM slot if
// cfg shows it mounted there, then deletes volid from its storage.
func removeCloudInitISO(ctx context.Context, px *proxmoxrest.Client, zone string, vmID int, cfg *qemu.Config, volid string) error {
	storageID, volume, ok := strings.Cut(volid, ":")
	if !ok {
		return fmt.Errorf("invalid volume id %q", volid)
	}

	if drive, ok := cfg.IDE[2]; ok && drive.File == volid {
		detachTask, err := px.Nodes(zone).Qemu().DetachISO(ctx, vmID, cloudInitDrive)
		if err != nil {
			return fmt.Errorf("failed to detach cloud-init ISO %s from vm %d: %v", volid, vmID, err)
		}

		if err = detachTask.Wait(ctx, &task.WaitOptions{Timeout: powerActionTimeout}); err != nil {
			return fmt.Errorf("failed to detach cloud-init ISO %s from vm %d: %v", volid, vmID, err)
		}
	}

	deleteTask, err := px.Nodes(zone).Storage().Content(storageID).Delete(ctx, volume, 0)
	if err != nil {
		return fmt.Errorf("failed to delete cloud-init ISO %s of vm %d: %v", volid, vmID, err)
	}

	if err = deleteTask.Wait(ctx, &task.WaitOptions{Timeout: powerActionTimeout}); err != nil {
		return fmt.Errorf("failed to delete cloud-init ISO %s of vm %d: %v", volid, vmID, err)
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

// buildCloudInitISO writes files into a NoCloud-layout ISO9660 image at a
// new temp path of its own, returning that path and a cleanup func that
// removes it. cleanup is always safe to call, even if err != nil. The path
// is unique per call, since VM ids repeat across regions and two launches
// sharing one would overwrite and remove each other's image.
func buildCloudInitISO(cloudInit cloudInitFiles) (isopath string, cleanup func(), err error) {
	cleanup = func() {}

	isoFile, err := os.CreateTemp("", "cloudinit-*.iso")
	if err != nil {
		return "", cleanup, err
	}

	// cleanup removes the path captured here: the error returns below
	// reset the isopath result to "".
	path := isoFile.Name()
	isopath = path
	cleanup = func() { os.Remove(path) }

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
		"user-data": cloudInit.UserData,
		"meta-data": cloudInit.MetaData,
	}
	if cloudInit.VendorData != "" {
		files["vendor-data"] = cloudInit.VendorData
	}

	if cloudInit.NetworkConfig != "" {
		files["network-config"] = cloudInit.NetworkConfig
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
			kubeletConfig.EvictionHard = maps.Clone(DefaultEvictionHard)
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

// gatherCloudInitInputs fetches what rendering cloud-init needs from
// Kubernetes (server version, root CA, the node class's template and value
// secrets, a bootstrap token) and from the VM's Proxmox config.
func (p *DefaultProvider) gatherCloudInitInputs(
	ctx context.Context,
	nodeClaim *karpv1.NodeClaim,
	nodeClass *v1alpha1.ProxmoxNodeClass,
	instanceType *cloudprovider.InstanceType,
	region string,
	zone string,
	vmID int,
	cfg *qemu.Config,
) (cloudInitInputs, error) {
	inputs := cloudInitInputs{
		NodeClaim:    nodeClaim,
		NodeClass:    nodeClass,
		InstanceType: instanceType,
		Region:       region,
		Zone:         zone,
		VMID:         vmID,
		Values:       map[string]string{},
	}

	systemNamespace := strings.TrimSpace(os.Getenv("SYSTEM_NAMESPACE"))
	if systemNamespace == "" {
		systemNamespace = "kube-system"
	}

	version, err := p.kubernetesInterface.Discovery().ServerVersion()
	if err != nil {
		return inputs, fmt.Errorf("failed to get kubernetes server version: %v", err)
	}

	inputs.KubernetesVersion = version.String()

	cm, err := p.kubernetesInterface.CoreV1().ConfigMaps(systemNamespace).Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return inputs, fmt.Errorf("failed to get configmap %s/kube-root-ca.crt: %v", systemNamespace, err)
	}

	inputs.RootCA = cm.Data["ca.crt"]

	secretKey := nodeClass.Spec.MetadataOptions.TemplatesRef
	secret, err := p.kubernetesInterface.CoreV1().Secrets(secretKey.Namespace).Get(ctx, secretKey.Name, metav1.GetOptions{})
	if err != nil {
		return inputs, fmt.Errorf("failed to get secret %s/%s: %v", secretKey.Namespace, secretKey.Name, err)
	}

	inputs.Templates = secret.Data

	if nodeClass.Spec.MetadataOptions.ValuesRef != nil && nodeClass.Spec.MetadataOptions.ValuesRef.Name != "" && nodeClass.Spec.MetadataOptions.ValuesRef.Namespace != "" {
		valuesKey := nodeClass.Spec.MetadataOptions.ValuesRef
		secret, err := p.kubernetesInterface.CoreV1().Secrets(valuesKey.Namespace).Get(ctx, valuesKey.Name, metav1.GetOptions{})
		if err != nil {
			return inputs, fmt.Errorf("failed to get secret %s/%s: %v", valuesKey.Namespace, valuesKey.Name, err)
		}

		for k, v := range secret.Data {
			inputs.Values[k] = string(v)
		}
	}

	inputs.BootstrapToken, err = p.kubernetesBootstrapProvider.CreateToken(ctx, nodeClaim)
	if err != nil {
		return inputs, fmt.Errorf("failed to create bootstrap token: %v", err)
	}

	if cfg.SMBios1 != nil {
		inputs.InstanceUUID = cfg.SMBios1.UUID
	}

	ifaces := map[string]cloudcapacity.NetworkIfaceInfo{}

	net := p.cloudCapacityProvider.GetNetwork(region, zone)
	if net != nil {
		ifaces = net.Ifaces
	}

	inputs.Network = cloudinit.GetNetworkConfigFromVirtualMachineConfig(cfg, ifaces)

	return inputs, nil
}

// renderCloudInit executes the node class's cloud-init templates, falling
// back to the built-in user-data, meta-data and network-config templates
// for any the templates secret leaves out. User-data sees UserDataValues,
// meta-data and vendor-data see the instance metadata, and network-config
// sees the VM's network config.
func renderCloudInit(inputs cloudInitInputs) (cloudInitFiles, error) {
	nodeClass := inputs.NodeClass
	instanceType := inputs.InstanceType

	metadataValues := cloudinit.MetaData{
		Hostname:     inputs.NodeClaim.Name,
		InstanceID:   fmt.Sprintf("%d", inputs.VMID),
		InstanceType: instanceType.Name,
		InstanceUUID: inputs.InstanceUUID,
		ProviderID:   provider.GetProviderID(inputs.Region, inputs.VMID),
		Region:       inputs.Region,
		Zone:         inputs.Zone,
		Tags:         nodeClass.Spec.Tags,
		NodeClass:    nodeClass.Name,
	}

	userdataValues := UserDataValues{
		Metadata: metadataValues,
		Network:  inputs.Network,
		Resources: Resources{
			CPU:    instanceType.Capacity.Cpu().Value(),
			Memory: instanceType.Capacity.Memory().Value(),
		},
		Kubernetes: Kubernetes{
			Version:              inputs.KubernetesVersion,
			RootCA:               inputs.RootCA,
			BootstrapToken:       inputs.BootstrapToken,
			KubeletConfiguration: applyKubernetesConfiguration(nodeClass, instanceType),
		},
		Values: inputs.Values,
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

	var (
		files cloudInitFiles
		err   error
	)

	userdata := string(inputs.Templates["user-data"])
	if userdata == "" {
		userdata = cloudinit.DefaultUserdata
	}

	files.UserData, err = cloudinit.ExecuteTemplate(userdata, userdataValues)
	if err != nil {
		return cloudInitFiles{}, fmt.Errorf("failed to execute userdata template: %v", err)
	}

	metadata := string(inputs.Templates["meta-data"])
	if metadata == "" {
		metadata = cloudinit.DefaultMetadata
	}

	files.MetaData, err = cloudinit.ExecuteTemplate(metadata, metadataValues)
	if err != nil {
		return cloudInitFiles{}, fmt.Errorf("failed to execute metadata template: %v", err)
	}

	if vendordata := string(inputs.Templates["vendor-data"]); vendordata != "" {
		files.VendorData, err = cloudinit.ExecuteTemplate(vendordata, metadataValues)
		if err != nil {
			return cloudInitFiles{}, fmt.Errorf("failed to execute vendor-data template: %v", err)
		}
	}

	networkconfig := string(inputs.Templates["network-config"])
	if networkconfig == "" {
		networkconfig = cloudinit.DefaultNetworkV2
	}

	if networkconfig != "" {
		files.NetworkConfig, err = cloudinit.ExecuteTemplate(networkconfig, inputs.Network)
		if err != nil {
			return cloudInitFiles{}, fmt.Errorf("failed to execute network-config template: %v", err)
		}
	}

	return files, nil
}
