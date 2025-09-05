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

package lifecycle

import (
	"context"

	"github.com/awslabs/operatorpkg/reasonable"
	"go.uber.org/multierr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/bootstrap"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/nodeclaim"
	"sigs.k8s.io/karpenter/pkg/utils/result"
)

type nodeClaimLifecycleReconciler interface {
	Reconcile(context.Context, *karpv1.NodeClaim) (reconcile.Result, error)
}

type Controller struct {
	kubeClient         client.Client
	cloudProvider      cloudprovider.CloudProvider
	instanceProvider   instance.Provider
	instanceRegistered *InstanceRegistered
}

func NewController(kubeClient client.Client, kubernetesBootstrapProvider bootstrap.Provider, cloudProvider cloudprovider.CloudProvider, instanceProvider instance.Provider) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cloudProvider:    cloudProvider,
		instanceProvider: instanceProvider,
		instanceRegistered: &InstanceRegistered{
			kubeClient:                  kubeClient,
			kubernetesBootstrapProvider: kubernetesBootstrapProvider,
			instanceProvider:            instanceProvider,
		},
	}
}

func (c *Controller) Name() string {
	return "nodeclaim.lifecycle.proxmox"
}

func (c *Controller) Reconcile(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	if !nodeClaim.GetDeletionTimestamp().IsZero() {
		return reconcile.Result{}, nil
	}

	nodeClaimCopy := nodeClaim.DeepCopy()

	var errs error

	results := []reconcile.Result{}

	for _, reconciler := range []nodeClaimLifecycleReconciler{
		c.instanceRegistered,
	} {
		res, err := reconciler.Reconcile(ctx, nodeClaim)
		errs = multierr.Append(errs, err)

		results = append(results, res)
	}

	if !equality.Semantic.DeepEqual(nodeClaimCopy, nodeClaim) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// https://github.com/kubernetes/kubernetes/issues/111643#issuecomment-2016489732
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

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&karpv1.NodeClaim{}, builder.WithPredicates(nodeclaim.IsManagedPredicateFuncs(c.cloudProvider))).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
