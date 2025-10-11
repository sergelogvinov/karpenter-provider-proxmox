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

package nodeload

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	lop "github.com/samber/lo/parallel"
	"go.uber.org/multierr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

const (
	scanPeriod = 1 * time.Minute
)

type Controller struct {
	cloudCapacityProvider cloudcapacity.Provider
}

func NewController(cloudCapacityProvider cloudcapacity.Provider) *Controller {
	return &Controller{
		cloudCapacityProvider: cloudCapacityProvider,
	}
}

func (c *Controller) Name() string {
	return "providers.cloudcapacity.nodeload"
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	work := []func(ctx context.Context) error{
		c.cloudCapacityProvider.UpdateNodeLoad,
	}

	errs := make([]error, len(work))
	lop.ForEach(work, func(f func(ctx context.Context) error, i int) {
		if err := f(ctx); err != nil {
			errs[i] = err
		}
	})

	if err := multierr.Combine(errs...); err != nil {
		return reconciler.Result{}, fmt.Errorf("updating cloudcapacity, %w", err)
	}

	return reconciler.Result{RequeueAfter: scanPeriod}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
