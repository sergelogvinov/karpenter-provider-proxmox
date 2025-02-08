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

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/awslabs/operatorpkg/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const CloudProviderName = "proxmox"

type CloudProvider struct {
	kubeClient    client.Client
	instanceTypes []*cloudprovider.InstanceType
	log           logr.Logger
}

func NewCloudProvider(ctx context.Context, kubeClient client.Client, instanceTypes []*cloudprovider.InstanceType) *CloudProvider {
	log := log.FromContext(ctx).WithName(CloudProviderName)
	log.Info("NewCloudProvider()", "instanceTypes", instanceTypes)

	return &CloudProvider{
		kubeClient:    kubeClient,
		instanceTypes: instanceTypes,
		log:           log,
	}
}

// Create launches a NodeClaim with the given resource requests and requirements and returns a hydrated
// NodeClaim back with resolved NodeClaim labels for the launched NodeClaim
func (c CloudProvider) Create(_ context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	c.log.Info("Create()", "NodeClaim", nodeClaim)

	return nil, nil
}

// Delete removes a NodeClaim from the cloudprovider by its provider id. Delete should return
// NodeClaimNotFoundError if the cloudProvider instance is already terminated and nil if deletion was triggered.
// Karpenter will keep retrying until Delete returns a NodeClaimNotFound error.
func (c CloudProvider) Delete(_ context.Context, nodeClaim *karpv1.NodeClaim) error {
	c.log.Info("CreaDeletete()", "NodeClaim", nodeClaim)

	return nil
}

// Get retrieves a NodeClaim from the cloudprovider by its provider id
func (c CloudProvider) Get(_ context.Context, providerID string) (*karpv1.NodeClaim, error) {
	c.log.Info("Get()", "providerID", providerID)

	return nil, nil
}

// List retrieves all NodeClaims from the cloudprovider
func (c CloudProvider) List(context.Context) ([]*karpv1.NodeClaim, error) {
	c.log.Info("List()")

	return nil, nil
}

// GetInstanceTypes returns instance types supported by the cloudprovider.
// Availability of types or zone may vary by nodepool or over time.  Regardless of
// availability, the GetInstanceTypes method should always return all instance types,
// even those with no offerings available.
func (c CloudProvider) GetInstanceTypes(_ context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	c.log.Info("GetInstanceTypes()", "nodePool", nodePool.Name)

	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "t2.micro",
			Offerings: []cloudprovider.Offering{
				{
					Price:        0,
					Available:    true,
					Requirements: scheduling.NewRequirements(scheduling.NewRequirement("kubernetes.io/arch", corev1.NodeSelectorOpIn, "amd64")),
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
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
					Price:        0,
					Available:    true,
					Requirements: scheduling.NewRequirements(scheduling.NewRequirement("kubernetes.io/arch", corev1.NodeSelectorOpIn, "amd64")),
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{
				KubeReserved: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
	}

	c.log.Info("Successfully retrieved instance types", "count", len(instanceTypes))

	return instanceTypes, nil
}

// IsDrifted returns whether a NodeClaim has drifted from the provisioning requirements
// it is tied to.
func (c CloudProvider) IsDrifted(_ context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	c.log.Info("IsDrifted()", "nodePool", nodeClaim)

	return "", nil
}

// RepairPolicy is for CloudProviders to define a set Unhealthy condition for Karpenter
// to monitor on the node.
func (c CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{}
}

// Name returns the CloudProvider implementation name.
func (c CloudProvider) Name() string {
	c.log.Info("Name()")

	return CloudProviderName
}

// GetSupportedNodeClasses returns CloudProvider NodeClass that implements status.Object
// NOTE: It returns a list where the first element should be the default NodeClass
func (c CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.ProxmoxNodeClass{}}
}
