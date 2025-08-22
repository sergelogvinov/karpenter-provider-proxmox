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
	"time"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	NodeTemplateClassResolutionReason = "NodeTemplateClassResolutionError"

	templateRepeatPeriod = 10 * time.Second
	templateScanPeriod   = 5 * time.Minute
)

type InstanceTemplate struct {
	instanceTemplateProvider instancetemplate.Provider
}

func (i *InstanceTemplate) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxTemplate) (reconcile.Result, error) {
	imageID := templateClass.GetImageID()
	if templateClass.Status.ImageID != "" && templateClass.Status.ImageID != imageID {
		err := i.instanceTemplateProvider.Delete(ctx, templateClass)
		if err != nil {
			templateClass.StatusConditions().SetFalse(
				v1alpha1.ConditionTemplateReady,
				NodeTemplateClassResolutionReason,
				"Image should be updated",
			)

			return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil //nolint: nilerr
		}

		templateClass.Status.ImageID = imageID
	}

	err := i.instanceTemplateProvider.Create(ctx, templateClass)
	if err != nil {
		return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil //nolint: nilerr
	}

	if templateClass.Status.ImageID == "" || templateClass.Status.Zones == nil {
		templateClass.StatusConditions().SetUnknownWithReason(
			v1alpha1.ConditionTemplateReady,
			NodeTemplateClassResolutionReason,
			"ImageID or InstalledZones is not set yet",
		)

		return reconcile.Result{RequeueAfter: templateRepeatPeriod}, nil
	}

	templateClass.StatusConditions().SetTrue(v1alpha1.ConditionTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}
