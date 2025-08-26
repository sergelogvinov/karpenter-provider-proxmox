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
	"fmt"
	"strconv"

	"github.com/awslabs/operatorpkg/reasonable"
	"go.uber.org/multierr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/result"
)

type templateStatusReconciler interface {
	Reconcile(context.Context, *v1alpha1.ProxmoxTemplate) (reconcile.Result, error)
}

// Controller reconciles an ProxmoxTemplate object to update its status
type Controller struct {
	kubeClient               client.Client
	instanceTemplateProvider *InstanceTemplate
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, instanceTemplateProvider instancetemplate.Provider) *Controller {
	return &Controller{
		kubeClient:               kubeClient,
		instanceTemplateProvider: &InstanceTemplate{instanceTemplateProvider: instanceTemplateProvider},
	}
}

func (c *Controller) Name() string {
	return "nodetemplateclass.status"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())
	log.FromContext(ctx).V(1).Info("Syncing Proxmox Templates")

	if !templateClass.GetDeletionTimestamp().IsZero() {
		return c.finalize(ctx, templateClass)
	}

	templateClassCopy := templateClass.DeepCopy()

	if !controllerutil.ContainsFinalizer(templateClass, v1alpha1.TerminationFinalizer) {
		controllerutil.AddFinalizer(templateClass, v1alpha1.TerminationFinalizer)
		if err := c.kubeClient.Patch(ctx, templateClass, client.MergeFrom(templateClassCopy)); err != nil {
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil
	}

	var errs error

	results := []reconcile.Result{}

	for _, reconciler := range []templateStatusReconciler{
		c.instanceTemplateProvider,
	} {
		res, err := reconciler.Reconcile(ctx, templateClass)
		errs = multierr.Append(errs, err)

		results = append(results, res)
	}

	templateClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(templateClass.Status.Zones))),
	}

	if !equality.Semantic.DeepEqual(templateClassCopy, templateClass) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the status condition list
		// https://github.com/kubernetes/kubernetes/issues/111643#issuecomment-2016489732
		if err := c.kubeClient.Status().Patch(ctx, templateClass, client.MergeFromWithOptions(templateClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
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
		For(&v1alpha1.ProxmoxTemplate{}).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (c *Controller) finalize(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(templateClass, v1alpha1.TerminationFinalizer) {
		return reconcile.Result{}, nil
	}

	templateClassCopy := templateClass.DeepCopy()
	result := reconcile.Result{}

	if len(templateClass.Status.Zones) > 0 {
		err := c.instanceTemplateProvider.instanceTemplateProvider.Delete(ctx, templateClass)
		if err != nil {
			result = reconcile.Result{RequeueAfter: templateRepeatPeriod}
		}
	}

	if len(templateClass.Status.Zones) == 0 {
		controllerutil.RemoveFinalizer(templateClass, v1alpha1.TerminationFinalizer)
	}

	if !equality.Semantic.DeepEqual(templateClassCopy, templateClass) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the finalizer list
		// https://github.com/kubernetes/kubernetes/issues/111643#issuecomment-2016489732
		if err := c.kubeClient.Patch(ctx, templateClass, client.MergeFromWithOptions(templateClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}

			return result, client.IgnoreNotFound(fmt.Errorf("removing termination finalizer, %w", err))
		}
	}

	return result, nil
}
