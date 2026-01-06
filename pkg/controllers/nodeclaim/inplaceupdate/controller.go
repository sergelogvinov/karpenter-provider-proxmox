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

package inplaceupdate

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"
	"go.uber.org/multierr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/nodeclaim"
	"sigs.k8s.io/karpenter/pkg/utils/result"
)

const (
	templateRepeatPeriod = 10 * time.Second
)

type nodeTemplateStatusReconciler interface {
	Reconcile(context.Context, *karpv1.NodeClaim, *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error)
}

// Controller reconciles an ProxmoxNodeTemplateClass object to update its status
type Controller struct {
	kubeClient                     client.Client
	instancePoolProvider           *InstancePool
	instanceSecurityGroupsProvider *InstanceSecurityGroups
	instanceTagProvider            *InstanceTag
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, instanceProvider instance.Provider) *Controller {
	return &Controller{
		kubeClient: kubeClient,
		instancePoolProvider: &InstancePool{
			instanceProvider: instanceProvider,
		},
		instanceSecurityGroupsProvider: &InstanceSecurityGroups{
			instanceProvider: instanceProvider,
		},
		instanceTagProvider: &InstanceTag{
			instanceProvider: instanceProvider,
		},
	}
}

func (c *Controller) Name() string {
	return "nodeclaim.inplaceupdate.proxmox"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	if !nodeClaim.GetDeletionTimestamp().IsZero() {
		return reconcile.Result{}, nil
	}

	if nodeClaim.Status.ProviderID == "" {
		return reconcile.Result{}, nil
	}

	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		return reconcile.Result{}, err
	}

	nodeClassHash := nodeClass.InPlaceHash()
	if nodeClaim.Annotations[v1alpha1.AnnotationProxmoxNodeInPlaceUpdateHash] == nodeClassHash {
		return reconcile.Result{}, nil
	}

	log.FromContext(ctx).V(1).Info("comparing in-place update hashes",
		"nodeClaim", nodeClaim.Name,
		"nodeClassHash", nodeClassHash,
		"nodeClaimHash", nodeClaim.Annotations[v1alpha1.AnnotationProxmoxNodeInPlaceUpdateHash],
	)

	nodeClaimCopy := nodeClaim.DeepCopy()

	var errs error

	results := []reconcile.Result{}

	for _, reconciler := range []nodeTemplateStatusReconciler{
		c.instanceTagProvider,
		c.instanceSecurityGroupsProvider,
		c.instancePoolProvider,
	} {
		res, err := reconciler.Reconcile(ctx, nodeClaim, nodeClass)
		errs = multierr.Append(errs, err)

		results = append(results, res)
	}

	if errs == nil {
		nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
			v1alpha1.AnnotationProxmoxNodeInPlaceUpdateHash: nodeClassHash,
			v1alpha1.AnnotationProxmoxNodeClassPool:         nodeClass.Spec.ResourcePool,
		})
	}

	if !equality.Semantic.DeepEqual(nodeClaimCopy, nodeClaim) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the status condition list
		if err := c.kubeClient.Patch(ctx, nodeClaim, client.MergeFromWithOptions(nodeClaimCopy, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}

			errs = multierr.Append(errs, client.IgnoreNotFound(err))
		}
	}

	if errs != nil {
		return reconcile.Result{}, errs
	}

	return result.Min(results...), nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&karpv1.NodeClaim{}).
		Watches(&v1alpha1.ProxmoxNodeClass{}, nodeclaim.NodeClassEventHandler(m.GetClient()), builder.WithPredicates(inPlaceChangedPredicate{})).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (i *Controller) resolveNodeClassFromNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.ProxmoxNodeClass, error) {
	ref := nodeClaim.Spec.NodeClassRef
	if ref == nil {
		return nil, fmt.Errorf("nodeClaim missing NodeClassRef")
	}

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := i.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, nodeClass); err != nil {
		return nil, err
	}

	return nodeClass, nil
}
