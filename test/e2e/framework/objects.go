//go:build e2e

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

package framework

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// ProxmoxTemplateOptions parameterizes NewProxmoxTemplate.
type ProxmoxTemplateOptions struct {
	Name string

	// Region, when set, pins the template to a single Proxmox region -
	// see docs/nodetemplateclass.md. Left empty, the controller targets
	// every region configured in its cloud-config.
	Region string

	// SourceImageURL is spec.sourceImage.url - the http(s) location of the
	// qcow2/raw image Proxmox downloads into its import storage.
	SourceImageURL string
	// ImageName is spec.sourceImage.imageName.
	ImageName string

	// StorageIDs is spec.storageIDs: the Proxmox storage(s) that must
	// support the import and images content types, either separately or
	// combined.
	StorageIDs []string

	// Bridge is the network bridge for the template's single required
	// network interface (spec.network[0].bridge).
	Bridge string

	// Tags to apply to the Proxmox VM template(s) this resource creates -
	// spec.tags. Used by the e2e suite to give a ProxmoxUnmanagedTemplate
	// something unique to match on instead of picking up unrelated
	// templates already present in the target cluster.
	Tags []string
}

// NewProxmoxTemplate builds a minimal, valid ProxmoxTemplate, mirroring
// docs/nodetemplateclass.md's example: a source image, the storage IDs to
// hold it, and a single network interface.
func NewProxmoxTemplate(opts ProxmoxTemplateOptions) *v1alpha1.ProxmoxTemplate {
	return &v1alpha1.ProxmoxTemplate{
		Name: opts.Name,
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Region: opts.Region,
			SourceImage: &v1alpha1.SourceImage{
				URL:       opts.SourceImageURL,
				ImageName: opts.ImageName,
			},
			StorageIDs: opts.StorageIDs,
			Network: []v1alpha1.Network{
				{Name: "net0", Bridge: opts.Bridge},
			},
			Tags: opts.Tags,
		},
	}
}

// ProxmoxUnmanagedTemplateOptions parameterizes NewProxmoxUnmanagedTemplate.
type ProxmoxUnmanagedTemplateOptions struct {
	Name string

	// Region, when set, restricts matching to a single Proxmox region -
	// see docs/nodetemplateclass.md. Left empty, every configured region
	// is searched.
	Region string

	// Tags to match Proxmox VM templates on - spec.tags. All of them must
	// be present on a template for it to match.
	Tags []string
}

// NewProxmoxUnmanagedTemplate builds a ProxmoxUnmanagedTemplate that
// matches templates by tags only (no templateName), mirroring
// docs/nodetemplateclass.md's example.
func NewProxmoxUnmanagedTemplate(opts ProxmoxUnmanagedTemplateOptions) *v1alpha1.ProxmoxUnmanagedTemplate {
	return &v1alpha1.ProxmoxUnmanagedTemplate{
		Name: opts.Name,
		Spec: v1alpha1.ProxmoxUnmanagedTemplateSpec{
			Region: opts.Region,
			Tags:   opts.Tags,
		},
	}
}

// nodeClassKind is the Kind a NodePool's nodeClassRef uses to point at a
// ProxmoxNodeClass - see docs/deploy/nodepool.yaml.
const nodeClassKind = "ProxmoxNodeClass"

// NodePoolTestLabelKey is the label the e2e suite's NodePool template
// applies to every node it launches (see NewNodePool), value set to the
// NodePool's own name.
const NodePoolTestLabelKey = "project.io" + "/e2e-nodepool"

// NodePoolOptions parameterizes NewNodePool.
type NodePoolOptions struct {
	Name string

	// NodeClassName is the name of the (pre-existing) ProxmoxNodeClass
	// this NodePool's spec.template.spec.nodeClassRef points at.
	NodeClassName string
}

// NewNodePool builds a NodePool referencing an existing ProxmoxNodeClass by
// name, mirroring docs/deploy/nodepool.yaml's example. Every node it
// launches carries the NodePoolTestLabelKey label set to opts.Name - the
// e2e suite's workloads use that as a nodeSelector to land only on nodes
// this particular NodePool provisioned, not any other node in the cluster.
//
// Disruption.ConsolidateAfter is set to "Never" so Karpenter doesn't
// consolidate/remove nodes out from under the test while it's still
// asserting against them; the suite tears the NodePool down explicitly
// instead.
func NewNodePool(opts NodePoolOptions) *karpv1.NodePool {
	return &karpv1.NodePool{
		Name: opts.Name,
		Spec: karpv1.NodePoolSpec{
			Disruption: karpv1.Disruption{
				ConsolidateAfter: karpv1.MustParseNillableDuration("Never"),
			},
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{NodePoolTestLabelKey: opts.Name},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: apis.Group,
						Kind:  nodeClassKind,
						Name:  opts.NodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      corev1.LabelArchStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"amd64"},
						},
						{
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"c1.2VCPU-4GB", "t1.2VCPU-6GB", "c1.4VCPU-8GB"},
						},
					},
				},
			},
		},
	}
}

// workloadImage is the test workload image every StatefulSet builder in
// this file uses: small, and sleeps rather than exiting so the suite can
// wait on it becoming Ready.
const workloadImage = "alpine"

// StatefulSetOptions parameterizes NewWorkloadStatefulSet.
type StatefulSetOptions struct {
	Name      string
	Namespace string
	Replicas  int32

	// NodeSelector pins the StatefulSet's pods to nodes carrying these
	// labels - typically karpv1.NodePoolLabelKey: <NodePool name>, so the
	// suite's own ephemeral NodePool is the only thing that can satisfy
	// them.
	NodeSelector map[string]string
}

// NewWorkloadStatefulSet builds a StatefulSet mirroring
// examples/workloads/test-statefulset.yaml: a sleeping alpine container,
// non-root, spread across nodes via pod anti-affinity.
func NewWorkloadStatefulSet(opts StatefulSetOptions) *appsv1.StatefulSet {
	labels := map[string]string{"app": opts.Name}
	terminationGrace := int64(3)

	return &appsv1.StatefulSet{
		Name:      opts.Name,
		Namespace: opts.Namespace,
		Labels:    labels,
		Spec: appsv1.StatefulSetSpec{
			PodManagementPolicy: appsv1.ParallelPodManagement,
			ServiceName:         opts.Name,
			Replicas:            &opts.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: &terminationGrace,
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									TopologyKey:   "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
								},
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup:    new(int64(65534)),
						RunAsGroup: new(int64(65534)),
						RunAsUser:  new(int64(65534)),
					},
					NodeSelector:       opts.NodeSelector,
					EnableServiceLinks: new(false),
					Containers: []corev1.Container{
						{
							Name:    workloadImage,
							Image:   workloadImage,
							Command: []string{"sleep", "1d"},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: new(false),
								RunAsNonRoot:             new(true),
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}
}
