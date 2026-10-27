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
	"fmt"
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const (
	NodeTemplateClassResolutionReason = "NodeTemplateClassResolutionError"

	templateRepeatPeriod = 10 * time.Second
	templateScanPeriod   = 5 * time.Minute
)

// Controller drives the template lifecycle of a ProxmoxTemplate: it creates
// the VM templates, replaces them when a rebuild field changes, updates them
// in place when an in-place field changes, and deletes them with the object.
type Controller struct {
	kubeClient               client.Client
	instanceTemplateProvider instancetemplate.Provider
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, instanceTemplateProvider instancetemplate.Provider) *Controller {
	return &Controller{
		kubeClient:               kubeClient,
		instanceTemplateProvider: instanceTemplateProvider,
	}
}

func (c *Controller) Name() string {
	return "nodetemplateclass.lifecycle"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	if !templateClass.GetDeletionTimestamp().IsZero() {
		return c.finalize(ctx, templateClass)
	}

	// The finalizer must be stored before any VM template exists.
	if !controllerutil.ContainsFinalizer(templateClass, v1alpha1.TerminationFinalizer) {
		templateClassCopy := templateClass.DeepCopy()

		controllerutil.AddFinalizer(templateClass, v1alpha1.TerminationFinalizer)

		// A merge patch replaces the whole finalizer list, so lock it against
		// finalizers added by others since the read.
		if err := c.kubeClient.Patch(ctx, templateClass, client.MergeFromWithOptions(templateClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil
			}

			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
	}

	log.FromContext(ctx).V(1).Info("Syncing Proxmox Templates")

	templateClassCopy := templateClass.DeepCopy()

	res := c.syncTemplates(ctx, templateClass)

	templateClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(templateClass.Status.Zones))),
	}

	if !equality.Semantic.DeepEqual(templateClassCopy.Status, templateClass.Status) {
		// This controller is the only status writer, so the patch takes no
		// optimistic lock: a concurrent metadata change, such as the deletion
		// timestamp, must not drop placements that finalize needs to clean up.
		if err := c.kubeClient.Status().Patch(ctx, templateClass, client.MergeFrom(templateClassCopy)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
	}

	if err := c.updateInPlace(ctx, templateClass); err != nil {
		return reconcile.Result{}, err
	}

	log.FromContext(ctx).V(1).Info("Finished syncing Proxmox Templates", "zones", len(templateClass.Status.Zones))

	return res, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&v1alpha1.ProxmoxTemplate{}, builder.WithPredicates(predicate.Or[client.Object](
			predicate.GenerationChangedPredicate{},
			deletionStartedPredicate{},
		))).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

// syncTemplates creates the VM templates, replacing them first when the
// source image changed, and records the outcome in the Ready condition.
func (c *Controller) syncTemplates(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) reconcile.Result {
	imageID := templateClass.GetImageID()
	if templateClass.Status.ImageID != "" && templateClass.Status.ImageID != imageID {
		err := c.instanceTemplateProvider.Delete(ctx, templateClass)
		if err != nil {
			templateClass.StatusConditions().SetFalse(
				v1alpha1.ConditionTemplateReady,
				NodeTemplateClassResolutionReason,
				"Image should be updated",
			)

			return reconcile.Result{RequeueAfter: templateRepeatPeriod}
		}

		templateClass.Status.ImageID = imageID
	}

	err := c.instanceTemplateProvider.Create(ctx, templateClass)
	if err != nil {
		return reconcile.Result{RequeueAfter: templateRepeatPeriod}
	}

	if templateClass.Status.ImageID == "" || templateClass.Status.Zones == nil {
		templateClass.StatusConditions().SetUnknownWithReason(
			v1alpha1.ConditionTemplateReady,
			NodeTemplateClassResolutionReason,
			"ImageID or InstalledZones is not set yet",
		)

		return reconcile.Result{RequeueAfter: templateRepeatPeriod}
	}

	templateClass.StatusConditions().SetTrue(v1alpha1.ConditionTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}
}

// updateInPlace applies in-place fields to ready VM templates and records the
// applied in-place hash. A template without a recorded hash adopts the current
// one without an update, so hand edits survive an upgrade.
func (c *Controller) updateInPlace(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) error {
	if status := templateClass.StatusConditions().Get(v1alpha1.ConditionTemplateReady); status == nil || !status.IsTrue() {
		return nil
	}

	inPlaceHash := templateClass.InPlaceHash()

	applied, ok := templateClass.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash]
	if ok && applied == inPlaceHash {
		return nil
	}

	if ok {
		log.FromContext(ctx).V(4).Info("comparing in-place update hashes",
			"templateClass", templateClass.Name,
			"inPlaceHash", inPlaceHash,
		)

		if err := c.instanceTemplateProvider.Update(ctx, templateClass); err != nil {
			return err
		}

		log.FromContext(ctx).V(1).Info("Finished in-place update",
			"templateClass", templateClass.Name,
			"inPlaceHash", inPlaceHash,
		)
	}

	templateClassCopy := templateClass.DeepCopy()
	templateClass.Annotations = lo.Assign(templateClass.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash: inPlaceHash,
	})

	return c.kubeClient.Patch(ctx, templateClass, client.MergeFrom(templateClassCopy))
}

func (c *Controller) finalize(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(templateClass, v1alpha1.TerminationFinalizer) {
		return reconcile.Result{}, nil
	}

	templateClassCopy := templateClass.DeepCopy()

	if len(templateClass.Status.Zones) > 0 {
		err := c.instanceTemplateProvider.Delete(ctx, templateClass)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to delete Proxmox Template", "templateClass", templateClass.Name)

			return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil //nolint: nilerr
		}
	}

	if len(templateClass.Status.Zones) == 0 {
		controllerutil.RemoveFinalizer(templateClass, v1alpha1.TerminationFinalizer)

		log.FromContext(ctx).Info("Finished cleaning up Proxmox Templates")
	}

	if !equality.Semantic.DeepEqual(templateClassCopy, templateClass) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the finalizer list
		// https://github.com/kubernetes/kubernetes/issues/111643#issuecomment-2016489732
		if err := c.kubeClient.Patch(ctx, templateClass, client.MergeFromWithOptions(templateClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil
			}

			return reconcile.Result{}, client.IgnoreNotFound(fmt.Errorf("removing termination finalizer, %w", err))
		}
	}

	return reconcile.Result{}, nil
}
