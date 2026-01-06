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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UserDataValues is cloud-init template values
type UserDataValues struct {
	Metadata   cloudinit.MetaData
	Network    cloudinit.NetworkConfig
	Kubernetes Kubernetes
	Values     map[string]string
}

type Kubernetes struct {
	RootCA               string
	BootstrapToken       string
	KubeletConfiguration *KubeletConfiguration
}

type KubernetesTaint struct {
	Key    string             `yaml:"key,omitempty"`
	Value  string             `yaml:"value,omitempty"`
	Effect corev1.TaintEffect `yaml:"effect,omitempty"`
}

type KubeletConfiguration struct {
	// CPUManagerPolicy is the name of the policy to use.
	CPUManagerPolicy string `yaml:"cpuManagerPolicy,omitempty"`
	// CPUCFSQuota enables CPU CFS quota enforcement for containers that specify CPU limits.
	CPUCFSQuota *bool `yaml:"cpuCFSQuota,omitempty"`
	// CPUManagerPolicyOptions is a set of key=value which 	allows to set extra options
	// to fine tune the behavior of the cpu manager policies.
	CPUManagerPolicyOptions map[string]string `yaml:"cpuManagerPolicyOptions,omitempty"`
	// CPU Manager reconciliation period.
	CPUManagerReconcilePeriod *metav1.Duration `yaml:"cpuManagerReconcilePeriod,omitempty"`
	// MemoryManagerPolicy is the name of the policy to use.
	// Requires the MemoryManager feature gate to be enabled.
	MemoryManagerPolicy string `yaml:"memoryManagerPolicy,omitempty"`
	// TopologyManagerPolicy is the name of the policy to use.
	TopologyManagerPolicy string `yaml:"topologyManagerPolicy,omitempty"`
	// TopologyManagerScope represents the scope of topology hint generation
	// that topology manager requests and hint providers generate.
	TopologyManagerScope string `yaml:"topologyManagerScope,omitempty"`
	// TopologyManagerPolicyOptions is a set of key=value which allows to set extra options
	// to fine tune the behavior of the topology manager policies.
	// Requires  both the "TopologyManager" and "TopologyManagerPolicyOptions" feature gates to be enabled.
	TopologyManagerPolicyOptions map[string]string `yaml:"topologyManagerPolicyOptions,omitempty"`
	// ImageMinimumGCAge is the minimum age for an unused image before it is
	// garbage collected.
	ImageMinimumGCAge *metav1.Duration `yaml:"imageMinimumGCAge,omitempty"`
	// ImageMaximumGCAge is the maximum age an image can be unused before it is garbage collected.
	// The default of this field is "0s", which disables this field--meaning images won't be garbage
	// collected based on being unused for too long.
	ImageMaximumGCAge *metav1.Duration `yaml:"imageMaximumGCAge,omitempty"`
	// imageGCHighThresholdPercent is the percent of disk usage after which
	// image garbage collection is always run. The percent is calculated as
	// this field value out of 100.
	ImageGCHighThresholdPercent *int32 `yaml:"imageGCHighThresholdPercent,omitempty"`
	// imageGCLowThresholdPercent is the percent of disk usage before which
	// image garbage collection is never run. Lowest disk usage to garbage
	// collect to. The percent is calculated as this field value out of 100.
	ImageGCLowThresholdPercent *int32 `yaml:"imageGCLowThresholdPercent,omitempty"`
	// ShutdownGracePeriod specifies the total duration that the node should delay the shutdown and total grace period for pod termination during a node shutdown.
	// Defaults to 0 seconds.
	// +featureGate=GracefulNodeShutdown
	// +optional
	ShutdownGracePeriod *metav1.Duration `yaml:"shutdownGracePeriod,omitempty"`
	// ShutdownGracePeriodCriticalPods specifies the duration used to terminate critical pods during a node shutdown. This should be less than ShutdownGracePeriod.
	// Defaults to 0 seconds.
	// For example, if ShutdownGracePeriod=30s, and ShutdownGracePeriodCriticalPods=10s,
	// during a node shutdown the first 20 seconds would be reserved for gracefully terminating normal pods,
	// and the last 10 seconds would be reserved for terminating critical pods.
	// +featureGate=GracefulNodeShutdown
	// +optional
	ShutdownGracePeriodCriticalPods *metav1.Duration `yaml:"shutdownGracePeriodCriticalPods,omitempty"`
	// A comma separated allowlist of unsafe sysctls or sysctl patterns (ending in `*`).
	// Unsafe sysctl groups are `kernel.shm*`, `kernel.msg*`, `kernel.sem`, `fs.mqueue.*`, and `net.*`.
	// These sysctls are namespaced but not allowed by default.
	// For example: "`kernel.msg*,net.ipv4.route.min_pmtu`"
	// +optional
	AllowedUnsafeSysctls []string `yaml:"allowedUnsafeSysctls,omitempty"`
	// clusterDNS is a list of IP addresses for a cluster DNS server. If set,
	// kubelet will configure all containers to use this for DNS resolution
	// instead of the host's DNS servers.
	ClusterDNS []string `yaml:"clusterDNS,omitempty"`
	// maxPods is the number of pods that can run on this Kubelet.
	MaxPods *int32 `yaml:"maxPods,omitempty"`
	// providerID, if set, sets the unique id of the instance that an external provider (i.e. cloudprovider)
	// can use to identify a specific node
	ProviderID string `yaml:"providerID,omitempty"`
	// Tells the Kubelet to fail to start if swap is enabled on the node.
	FailSwapOn *bool `yaml:"failSwapOn,omitempty"`

	// A set of ResourceName=ResourceQuantity (e.g. cpu=200m,memory=150G,ephemeral-storage=1G,pid=100) pairs
	// that describe resources reserved for non-kubernetes components.
	// Currently only cpu, memory and local ephemeral storage for root file system are supported.
	// See https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources for more detail.
	SystemReserved map[string]string `yaml:"systemReserved,omitempty"`
	// A set of ResourceName=ResourceQuantity (e.g. cpu=200m,memory=150G,ephemeral-storage=1G,pid=100) pairs
	// that describe resources reserved for kubernetes system components.
	// Currently only cpu, memory and local ephemeral storage for root file system are supported.
	// See https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources for more detail.
	KubeReserved map[string]string `yaml:"kubeReserved,omitempty"`
	// A set of ResourceName=ResourceQuantity (e.g. cpu=200m,memory=150G,ephemeral-storage=1G,pid=100) pairs
	// that describe resources reserved for kubernetes system components.
	// Currently only cpu, memory and local ephemeral storage for root file system are supported.
	// See https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources for more detail.
	EvictionHard map[string]string `yaml:"evictionHard,omitempty"`

	// RegisterWithTaints is a list of taints to add to a node object when the kubelet registers itself.
	// This only takes effect when registerNode is true and upon the initial registration of the node
	// See https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/#kubelet-config-k8s-io-v1beta1-KubeletConfiguration
	RegisterWithTaints []KubernetesTaint `yaml:"registerWithTaints,omitempty"`
}

// DefaultEvictionHard is the default eviction hard thresholds for Kubernetes
var DefaultEvictionHard = map[string]string{
	"memory.available":  "100Mi",
	"nodefs.available":  "10%",
	"imagefs.available": "15%",
}
