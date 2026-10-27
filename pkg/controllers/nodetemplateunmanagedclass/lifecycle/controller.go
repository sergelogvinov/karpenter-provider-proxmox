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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const (
	templateRepeatPeriod = 5 * time.Second
	templateScanPeriod   = 5 * time.Minute
)

// Controller keeps the template placements of a ProxmoxUnmanagedTemplate in
// step with the Proxmox VM templates that match its spec.
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
	return "nodetemplateunmanagedclass.lifecycle"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxUnmanagedTemplate) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())
	log.FromContext(ctx).V(4).Info("Syncing Proxmox Unmanaged Templates")

	templateClassCopy := templateClass.DeepCopy()

	res := c.syncTemplates(ctx, templateClass)

	templateClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(templateClass.Status.Zones))),
	}

	if !equality.Semantic.DeepEqual(templateClassCopy, templateClass) {
		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the status condition list
		if err := c.kubeClient.Status().Patch(ctx, templateClass, client.MergeFromWithOptions(templateClassCopy, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil
			}

			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
	}

	log.FromContext(ctx).V(1).Info("Finished syncing Proxmox Unmanaged Templates", "zones", len(templateClass.Status.Zones))

	return res, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&v1alpha1.ProxmoxUnmanagedTemplate{}).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

// syncTemplates records the placements of the VM templates that match the
// spec by name, region and tags, and the outcome in the Ready condition.
func (c *Controller) syncTemplates(ctx context.Context, templateClass *v1alpha1.ProxmoxUnmanagedTemplate) reconcile.Result {
	if err := templateClass.Validate(); err != nil {
		templateClass.SetTemplatePlacements(nil)
		templateClass.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, "TemplateValidation", err.Error())

		return reconcile.Result{}
	}

	if err := c.instanceTemplateProvider.SyncInstanceTemplates(ctx); err != nil {
		return reconcile.Result{RequeueAfter: templateScanPeriod}
	}

	templates := c.instanceTemplateProvider.ListWithFilter(ctx, func(info *instancetemplate.InstanceTemplateInfo) bool {
		if (templateClass.Spec.TemplateName != "" && info.Name != templateClass.Spec.TemplateName) ||
			(templateClass.Spec.Region != "" && info.Region != templateClass.Spec.Region) {
			return false
		}

		if len(templateClass.Spec.Tags) > 0 {
			if !lo.Every(info.TemplateTags, templateClass.Spec.Tags) {
				return false
			}
		}

		return true
	})
	if len(templates) == 0 {
		templateClass.SetTemplatePlacements(nil)
		templateClass.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, "TemplatesNotFound", "Proxmox Template did not match the node class requirements")

		return reconcile.Result{RequeueAfter: templateScanPeriod}
	}

	placements := make([]v1alpha1.TemplatePlacement, 0, len(templates))
	for _, template := range templates {
		placements = append(placements, template.Placement())
	}

	templateClass.SetTemplatePlacements(placements)
	templateClass.StatusConditions().SetTrue(v1alpha1.ConditionTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}
}
