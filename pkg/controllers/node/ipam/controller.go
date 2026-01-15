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

package ipam

import (
	"context"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"

	corev1 "k8s.io/api/core/v1"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const templateRepeatPeriod = 10 * time.Second

// Controller reconciles an ProxmoxNodeClass object to update its status
type Controller struct {
	kubeClient       client.Client
	nodeIpamProvider nodeipam.Provider
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, nodeIpamProvider nodeipam.Provider) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		nodeIpamProvider: nodeIpamProvider,
	}
}

func (c *Controller) Name() string {
	return "node.ipam"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, n *corev1.Node) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	if !n.GetDeletionTimestamp().IsZero() {
		c.nodeIpamProvider.ReleaseNodeIPs(n)

		return reconcile.Result{}, nil
	}

	err := c.nodeIpamProvider.OccupyNodeIPs(n)
	if err != nil {
		if err == nodeipam.ErrNoSubnetFound {
			return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil
		}

		log.FromContext(ctx).Error(err, "Failed to occupy node IPs, requeuing", "node", n.Name)

		return reconcile.Result{RequeueAfter: templateRepeatPeriod}, err
	}

	log.FromContext(ctx).V(4).Info("Node ipam update", "node", n.Name, "status", c.nodeIpamProvider.String())

	return reconcile.Result{}, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&corev1.Node{}, builder.WithPredicates(ipChangedPredicate{})).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
