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
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	cloudproviderevents "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider/events"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
)

const (
	CloudProviderName     = "proxmox"
	ProxmoxProviderPrefix = "proxmox://"
)

type CloudProvider struct {
	kubeClient client.Client
	recorder   events.Recorder

	instanceProvider         instance.Provider
	instanceTypeProvider     instancetype.Provider
	instanceTemplateProvider instancetemplate.Provider
	cloudcapacityProvider    cloudcapacity.Provider

	log logr.Logger
}

func NewCloudProvider(
	ctx context.Context,
	kubeClient client.Client,
	recorder events.Recorder,
	instanceProvider instance.Provider,
	instanceTemplateProvider instancetemplate.Provider,
	instanceTypeProvider instancetype.Provider,
	cloudcapacityProvider cloudcapacity.Provider,
) *CloudProvider {
	log := log.FromContext(ctx).WithName(CloudProviderName)

	return &CloudProvider{
		kubeClient:               kubeClient,
		recorder:                 recorder,
		instanceProvider:         instanceProvider,
		instanceTemplateProvider: instanceTemplateProvider,
		instanceTypeProvider:     instanceTypeProvider,
		cloudcapacityProvider:    cloudcapacityProvider,
		log:                      log,
	}
}

// Create launches a NodeClaim with the given resource requests and requirements and returns a hydrated
// NodeClaim back with resolved NodeClaim labels for the launched NodeClaim
func (c CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Create()").WithValues("nodeClaim", nodeClaim.Name)

	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			c.recorder.Publish(cloudproviderevents.NodeClaimFailedToResolveNodeClass(nodeClaim))
		}

		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("resolving node class, %w", err))
	}

	nodeClassReady := nodeClass.StatusConditions().Get(status.ConditionReady)
	if nodeClassReady.IsFalse() {
		return nil, cloudprovider.NewNodeClassNotReadyError(stderrors.New(nodeClassReady.Message))
	}

	if nodeClassReady.IsUnknown() {
		return nil, cloudprovider.NewCreateError(
			fmt.Errorf("resolving NodeClass readiness, NodeClass is in Ready=Unknown, %s", nodeClassReady.Message),
			"NodeClassReadinessUnknown",
			"NodeClass is in Ready=Unknown",
		)
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

	instanceType, err := c.resolveInstanceTypeFromNode(ctx, node)
	if err != nil {
		log.Error(err, "Failed to resolve instance type from node", "node", node.Name)
	}

	nc, err := c.nodeToNodeClaim(ctx, instanceType, node)
	if err != nil {
		log.Error(err, "Failed to convert node to NodeClaim", "node", node.Name)

		return nil, fmt.Errorf("converting node to NodeClaim, %w", err)
	}

	nc.Annotations = lo.Assign(nc.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxNodeClassHash:        nodeClass.Hash(),
		v1alpha1.AnnotationProxmoxNodeClassHashVersion: v1alpha1.ProxmoxNodeClassHashVersion,
	})

	return nc, nil
}

// Delete removes a NodeClaim from the cloudprovider by its provider id. Delete should return
// NodeClaimNotFoundError if the cloudProvider instance is already terminated and nil if deletion was triggered.
// Karpenter will keep retrying until Delete returns a NodeClaimNotFound error.
func (c CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := c.log.WithName("Delete()").WithValues("nodeClaim", nodeClaim.Name, "providerID", nodeClaim.Status.ProviderID)

	providerID := nodeClaim.Status.ProviderID
	if providerID == "" {
		log.Info("providerID is empty")

		return nil
	}

	if !strings.HasPrefix(providerID, ProxmoxProviderPrefix) {
		log.Info("providerID does not have the correct prefix")

		return nil
	}

	return c.instanceProvider.Delete(ctx, nodeClaim)
}

// Get retrieves a NodeClaim from the cloudprovider by its provider id
// It uses for termination.finalize only (karpenter/pkg/controllers/node/termination/controller.go)
// In future versions, we may need to provide more information.
func (c CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	log := c.log.WithName("Get()").WithValues("providerID", providerID)

	if providerID == "" {
		log.Info("providerID is empty")

		return nil, fmt.Errorf("providerID is empty")
	}

	if !strings.HasPrefix(providerID, ProxmoxProviderPrefix) {
		log.Info("providerID does not have the correct prefix")

		return nil, fmt.Errorf("providerID does not have the correct prefix")
	}

	node, err := c.instanceProvider.Get(ctx, providerID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return nil, fmt.Errorf("getting status of instance, %w", err)
	}

	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: node.Name}, node); err != nil {
		if errors.IsNotFound(err) {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}

		return nil, fmt.Errorf("getting node resource, %w", err)
	}

	return c.nodeToNodeClaim(ctx, nil, node)
}

// List retrieves all NodeClaims from the cloudprovider
func (c CloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	log := c.log.WithName("List()")

	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("listing nodes, %w", err)
	}

	nodeClaims := []*karpv1.NodeClaim{}

	for _, node := range nodeList.Items {
		if !strings.HasPrefix(node.Spec.ProviderID, ProxmoxProviderPrefix) {
			continue
		}

		instanceType, err := c.resolveInstanceTypeFromNode(ctx, &node)
		if err != nil {
			log.V(1).Info("Failed to resolve instance type from node", "node", node.Name, "error", err)
		}

		nc, err := c.nodeToNodeClaim(ctx, instanceType, &node)
		if err != nil {
			log.Error(err, "Failed to convert nodeclaim from node", "node", node.Name)

			continue
		}

		nodeClaims = append(nodeClaims, nc)
	}

	log.V(4).Info("Successfully retrieved node claims list", "count", len(nodeClaims))

	return nodeClaims, nil
}

// GetInstanceTypes returns instance types supported by the cloudprovider.
// Availability of types or zone may vary by nodepool or over time.  Regardless of
// availability, the GetInstanceTypes method should always return all instance types,
// even those with no offerings available.
func (c CloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	log := c.log.WithName("GetInstanceTypes()")

	nodeClass, err := c.resolveNodeClassFromNodePool(ctx, nodePool)
	if err != nil {
		if errors.IsNotFound(err) {
			c.recorder.Publish(cloudproviderevents.NodePoolFailedToResolveNodeClass(nodePool))

			log.Error(err, "Failed to resolve nodeClass for nodePool", "nodePool", nodePool.Name)
		}

		return nil, fmt.Errorf("resolving node class, %w", err)
	}

	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("constructing instance types, %w", err)
	}

	log.V(4).Info("Resolved instance types", "nodePool", nodePool.Name, "nodeclass", nodeClass.Name, "count", len(instanceTypes))

	return instanceTypes, nil
}

// IsDrifted returns whether a NodeClaim has drifted from the provisioning requirements
// it is tied to.
func (c CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	log := c.log.WithName("IsDrifted()")

	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			c.recorder.Publish(cloudproviderevents.NodeClaimFailedToResolveNodeClass(nodeClaim))

			log.Error(err, "Failed to resolve nodeClass for nodeClaim", "nodeClaim", nodeClaim.Name)
		}

		return "", client.IgnoreNotFound(err)
	}

	return c.isNodeClassDrifted(ctx, nodeClaim, nodeClass)
}

// RepairPolicy is for CloudProviders to define a set Unhealthy condition for Karpenter
// to monitor on the node.
func (c CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionFalse,
			TolerationDuration: 15 * time.Minute,
		},
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionUnknown,
			TolerationDuration: 15 * time.Minute,
		},
	}
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

func (c *CloudProvider) resolveNodeClassFromNodePool(ctx context.Context, nodePool *karpv1.NodePool) (*v1alpha1.ProxmoxNodeClass, error) {
	ref := nodePool.Spec.Template.Spec.NodeClassRef
	if ref == nil {
		return nil, fmt.Errorf("nodePool missing NodeClassRef")
	}

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, nodeClass); err != nil {
		return nil, err
	}

	return nodeClass, nil
}

func (c *CloudProvider) resolveNodeClassFromNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.ProxmoxNodeClass, error) {
	ref := nodeClaim.Spec.NodeClassRef
	if ref == nil {
		return nil, fmt.Errorf("nodeClaim missing NodeClassRef")
	}

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, nodeClass); err != nil {
		return nil, err
	}

	return nodeClass, nil
}
