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

package status

import (
	"context"

	"github.com/awslabs/operatorpkg/reasonable"
	"go.uber.org/multierr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/result"
)

type nodeClassStatusReconciler interface {
	Reconcile(context.Context, *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error)
}

// Controller reconciles an ProxmoxNodeClass object to update its status
type Controller struct {
	kubeClient               client.Client
	instanceTemplateProvider *InstanceTemplate
	metadataOptions          *MetadataOptions
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, cloudCapacityProvider cloudcapacity.Provider) *Controller {
	return &Controller{
		kubeClient:               kubeClient,
		instanceTemplateProvider: &InstanceTemplate{kubeClient: kubeClient, cloudCapacityProvider: cloudCapacityProvider},
		metadataOptions:          &MetadataOptions{kubeClient: kubeClient},
	}
}

func (c *Controller) Name() string {
	return "nodeclass.status"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	nodeClassCopy := nodeClass.DeepCopy()

	var errs error

	results := []reconcile.Result{}

	for _, reconciler := range []nodeClassStatusReconciler{
		c.instanceTemplateProvider,
		c.metadataOptions,
	} {
		res, err := reconciler.Reconcile(ctx, nodeClass)
		errs = multierr.Append(errs, err)

		results = append(results, res)
	}

	if !equality.Semantic.DeepEqual(nodeClassCopy, nodeClass) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the status condition list
		if err := c.kubeClient.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(nodeClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
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
		For(&v1alpha1.ProxmoxNodeClass{}).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
