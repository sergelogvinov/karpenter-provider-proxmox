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

	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/samber/lo"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

const (
	CloudProviderName     = "proxmox"
	ProxmoxProviderPrefix = "proxmox://"
)

type CloudProvider struct {
	kubeClient       client.Client
	instanceTypes    []*cloudprovider.InstanceType
	instanceProvider *instance.Provider
	log              logr.Logger
}

func NewCloudProvider(ctx context.Context, kubeClient client.Client, instanceTypes []*cloudprovider.InstanceType, instanceProvider *instance.Provider) *CloudProvider {
	log := log.FromContext(ctx).WithName(CloudProviderName)
	log.WithName("NewCloudProvider()").Info("Executed with params", "instanceTypes", instanceTypes)

	return &CloudProvider{
		kubeClient:       kubeClient,
		instanceTypes:    instanceTypes,
		instanceProvider: instanceProvider,
		log:              log,
	}
}

// Create launches a NodeClaim with the given resource requests and requirements and returns a hydrated
// NodeClaim back with resolved NodeClaim labels for the launched NodeClaim
func (c CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Create()")
	log.Info("Executed with params", "nodePool", nodeClaim.Name, "spec", nodeClaim.Spec)

	if nodeClaim == nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("nodeClaim is nil"))
	}

	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("resolving node class, %w", err))
	}

	instanceTypes, err := c.resolveInstanceTypes(ctx, nodeClaim, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("resolving instance types, %w", err)
	}

	if len(instanceTypes) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("all requested instance types were unavailable during launch"))
	}

	log.Info("Successfully resolved instance types", "count", len(instanceTypes))

	node, err := c.instanceProvider.Create(ctx, nodeClaim, nodeClass, instanceTypes)
	if err != nil {
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	log.Info("Successfully created instance", "providerID", node.Spec.ProviderID)

	return c.nodeToNodeClaim(ctx, node)
}

// Delete removes a NodeClaim from the cloudprovider by its provider id. Delete should return
// NodeClaimNotFoundError if the cloudProvider instance is already terminated and nil if deletion was triggered.
// Karpenter will keep retrying until Delete returns a NodeClaimNotFound error.
func (c CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := c.log.WithName("Delete()")
	log.Info("Executed with params", "nodePool", nodeClaim.Name)

	if nodeClaim == nil {
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("nodeClaim is nil"))
	}

	providerID := nodeClaim.Status.ProviderID
	if providerID == "" {
		log.Info("providerID is empty")

		return nil
	}

	return c.instanceProvider.Delete(ctx, nodeClaim)
}

// Get retrieves a NodeClaim from the cloudprovider by its provider id
func (c CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Get()")
	log.Info("Executed with params", "providerID", providerID)

	if providerID == "" {
		return nil, fmt.Errorf("providerID is empty")
	}

	if !strings.HasPrefix(providerID, ProxmoxProviderPrefix) {
		return nil, fmt.Errorf("providerID does not have the correct prefix")
	}

	node, err := c.instanceProvider.Get(ctx, providerID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return nil, fmt.Errorf("getting instance, %w", err)
	}

	return c.nodeToNodeClaim(ctx, node)
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

		nc, err := c.nodeToNodeClaim(ctx, &nodeList.Items[i])
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

	log.Info("Successfully retrieved instance types", "count", len(c.instanceTypes))

	return c.instanceTypes, nil
}

// IsDrifted returns whether a NodeClaim has drifted from the provisioning requirements
// it is tied to.
func (c CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	log := c.log.WithName("IsDrifted()")
	log.Info("Executed with params", "nodeClaim", nodeClaim.Name)

	if nodeClaim == nil {
		return "", cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("nodeClaim is nil"))
	}

	if nodeClaim.Spec.NodeClassRef == nil {
		return "", nil
	}

	_, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		return "", cloudprovider.NewInsufficientCapacityError(fmt.Errorf("resolving node class, %w", err))
	}

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

func (c *CloudProvider) resolveNodeClassFromNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.ProxmoxNodeClass, error) {
	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}

	// For the purposes of NodeClass CloudProvider resolution, we treat deleting NodeClasses as NotFound
	if !nodeClass.DeletionTimestamp.IsZero() {
		return nil, errors.NewNotFound(v1alpha1.SchemeGroupVersion.WithResource("proxmoxnodeclass").GroupResource(), nodeClass.Name)
	}

	return nodeClass, nil
}

func (c *CloudProvider) resolveInstanceTypes(_ context.Context, nodeClaim *karpv1.NodeClaim, _ *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)

	return lo.Filter(c.instanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
		return len(i.Offerings.Compatible(reqs).Available()) > 0 &&
			resources.Fits(nodeClaim.Spec.Resources.Requests, i.Allocatable())
	}), nil
}
