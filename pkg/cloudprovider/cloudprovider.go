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

package proxmox

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/awslabs/operatorpkg/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const (
	CloudProviderName     = "proxmox"
	ProxmoxProviderPrefix = "proxmox://"
)

type CloudProvider struct {
	kubeClient    client.Client
	instanceTypes []*cloudprovider.InstanceType
	log           logr.Logger
}

func NewCloudProvider(ctx context.Context, kubeClient client.Client, instanceTypes []*cloudprovider.InstanceType) *CloudProvider {
	log := log.FromContext(ctx).WithName(CloudProviderName)
	log.WithName("NewCloudProvider()").Info("Executed with params", "instanceTypes", instanceTypes)

	return &CloudProvider{
		kubeClient:    kubeClient,
		instanceTypes: instanceTypes,
		log:           log,
	}
}

// Create launches a NodeClaim with the given resource requests and requirements and returns a hydrated
// NodeClaim back with resolved NodeClaim labels for the launched NodeClaim
func (c CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Create()")
	log.Info("Executed with params", "nodePool", nodeClaim.Name, "spec", nodeClaim.Spec)

	return nil, fmt.Errorf("not implemented")
}

// Delete removes a NodeClaim from the cloudprovider by its provider id. Delete should return
// NodeClaimNotFoundError if the cloudProvider instance is already terminated and nil if deletion was triggered.
// Karpenter will keep retrying until Delete returns a NodeClaimNotFound error.
func (c CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := c.log.WithName("Delete()")
	log.Info("Executed with params", "nodePool", nodeClaim.Name)

	return fmt.Errorf("not implemented")
}

// Get retrieves a NodeClaim from the cloudprovider by its provider id
func (c CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Get()")
	log.Info("Executed with params", "providerID", providerID)

	return nil, fmt.Errorf("not implemented")
}

// List retrieves all NodeClaims from the cloudprovider
func (c CloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	log := c.log.WithName("List()")
	log.Info("Executed")

	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("listing nodes, %w", err)
	}

	var nodeClaims []*karpv1.NodeClaim
	for i, node := range nodeList.Items {
		if !strings.HasPrefix(node.Spec.ProviderID, ProxmoxProviderPrefix) {
			continue
		}

		nc, err := c.toNodeClaim(&nodeList.Items[i])
		if err != nil {
			return nil, fmt.Errorf("converting nodeclaim, %w", err)
		}

		nodeClaims = append(nodeClaims, nc)
	}

	log.Info("Successfully retrieved instance list", "count", len(nodeClaims))

	return nodeClaims, nil
}

// GetInstanceTypes returns instance types supported by the cloudprovider.
// Availability of types or zone may vary by nodepool or over time.  Regardless of
// availability, the GetInstanceTypes method should always return all instance types,
// even those with no offerings available.
func (c CloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	log := c.log.WithName("GetInstanceTypes()")
	log.Info("Executed with params", "nodePool", nodePool.Name)

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePool.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Failed to resolve NodeClass")
		}
		return nil, err
	}
	log.Info("Resolved NodeClass", "name", nodeClass.Name)

	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "t2.micro",
			Offerings: []cloudprovider.Offering{
				{
					Price:     1,
					Available: true,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
						scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "8VCPU-24GB"),
						// scheduling.NewRequirement("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "rnd-1", "rnd-2"),
					),
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
		{
			Name: "t2.small",
			Offerings: []cloudprovider.Offering{
				{
					Price:     1,
					Available: true,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
						scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "8VCPU-24GB"),
					),
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
		{
			Name: "8VCPU-24GB",
			Offerings: []cloudprovider.Offering{
				{
					Price:     1,
					Available: true,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
						scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, "linux"),

						scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "8VCPU-24GB"),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "rnd-1", "rnd-2"),
					),
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("24Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
	}

	log.Info("Successfully retrieved instance types", "count", len(instanceTypes))

	return instanceTypes, nil
}

// IsDrifted returns whether a NodeClaim has drifted from the provisioning requirements
// it is tied to.
func (c CloudProvider) IsDrifted(_ context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	log := c.log.WithName("IsDrifted()")
	log.Info("Executed with params", "nodeClaim", nodeClaim.Name)

	return "", nil
}

// RepairPolicy is for CloudProviders to define a set Unhealthy condition for Karpenter
// to monitor on the node.
func (c CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{}
}

// Name returns the CloudProvider implementation name.
func (c CloudProvider) Name() string {
	return CloudProviderName
}

// GetSupportedNodeClasses returns CloudProvider NodeClass that implements status.Object
// NOTE: It returns a list where the first element should be the default NodeClass
func (c CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.ProxmoxNodeClass{}}
}
