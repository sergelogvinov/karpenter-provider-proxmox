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
	"fmt"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/mitchellh/hashstructure/v2"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Controller computes a hash of the ProxmoxNodeClass spec and stores it in the status
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}
	return &Controller{
		kubeClient: kubeClient,
	}, nil
}

func (c *Controller) Name() string {
	return "nodeclass.hash"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.ProxmoxNodeClass{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Compute hash of the spec
	hash, err := hashstructure.Hash(nc.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	// Update status if hash changed
	if nc.Status.SpecHash != hash {
		patch := client.MergeFrom(nc.DeepCopy())
		nc.Status.SpecHash = hash
		if err := c.kubeClient.Status().Patch(ctx, nc, patch); err != nil {
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
		WithEventFilter(predicate.NewPredicateFuncs(func(object client.Object) bool {
			return true // Only reconcile on spec changes
		})).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 1,
		}).
		Complete(c)
}
