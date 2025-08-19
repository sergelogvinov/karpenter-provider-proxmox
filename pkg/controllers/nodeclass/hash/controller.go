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

package hash

import (
	"context"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

// Controller computes a hash of the ProxmoxNodeClass spec and stores it in the status
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	return &Controller{
		kubeClient: kubeClient,
	}, nil
}

func (c *Controller) Name() string {
	return "nodeclass.hash"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	nodeClassCopy := nodeClass.DeepCopy()
	nodeClassCopy.Annotations = lo.Assign(nodeClass.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxNodeClassHash:        nodeClass.Hash(),
		v1alpha1.AnnotationProxmoxNodeClassHashVersion: v1alpha1.ProxmoxNodeClassHashVersion,
	})

	if !equality.Semantic.DeepEqual(nodeClass, nodeClassCopy) {
		if err := c.kubeClient.Patch(ctx, nodeClassCopy, client.MergeFrom(nodeClass)); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
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
