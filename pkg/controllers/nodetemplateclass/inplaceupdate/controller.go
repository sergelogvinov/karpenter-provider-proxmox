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
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	"k8s.io/apimachinery/pkg/api/equality"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const (
	templateUpdatePeriod = time.Minute
)

// Controller reconciles an ProxmoxNodeTemplateClass object to update its status
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
	return "nodetemplateclass.inplaceupdate"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	if templateClass.Status.ImageID == "" || len(templateClass.Status.Zones) == 0 {
		return reconcile.Result{}, nil
	}

	if status := templateClass.StatusConditions().Get(v1alpha1.ConditionTemplateReady); status == nil || !status.IsTrue() {
		return reconcile.Result{RequeueAfter: templateUpdatePeriod}, nil
	}

	templateClassHash := templateClass.InPlaceHash()

	if templateClass.Annotations[v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash] == templateClassHash {
		return reconcile.Result{}, nil
	}

	log.FromContext(ctx).V(1).Info("comparing in-place update hashes",
		"templateClass", templateClass.Name,
		"templateClassHash", templateClassHash,
	)

	err := c.instanceTemplateProvider.Update(ctx, templateClass)
	if err != nil {
		return reconcile.Result{RequeueAfter: templateUpdatePeriod}, err
	}

	templateClassCopy := templateClass.DeepCopy()
	templateClassCopy.Annotations = lo.Assign(templateClass.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxTemplateInPlaceUpdateHash: templateClassHash,
	})

	if !equality.Semantic.DeepEqual(templateClass, templateClassCopy) {
		if err := c.kubeClient.Patch(ctx, templateClassCopy, client.MergeFrom(templateClass)); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&v1alpha1.ProxmoxTemplate{}, builder.WithPredicates(inPlaceChangedPredicate{})).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
