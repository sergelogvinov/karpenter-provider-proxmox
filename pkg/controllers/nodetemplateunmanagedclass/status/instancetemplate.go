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
	"time"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	templateScanPeriod = 5 * time.Minute
)

type InstanceTemplate struct {
	instanceTemplateProvider instancetemplate.Provider
}

func (i *InstanceTemplate) Reconcile(ctx context.Context, templateClass *v1alpha1.ProxmoxUnmanagedTemplate) (reconcile.Result, error) {
	if err := templateClass.Validate(); err != nil {
		templateClass.Status.Zones = nil
		templateClass.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, "TemplateValidation", err.Error())

		return reconcile.Result{}, nil
	}

	templates := i.instanceTemplateProvider.ListWithFilter(ctx, func(info *instancetemplate.InstanceTemplateInfo) bool {
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
		templateClass.Status.Zones = nil
		templateClass.StatusConditions().SetFalse(v1alpha1.ConditionTemplateReady, "TemplatesNotFound", "Proxmox Template did not match the node class requirements")

		return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
	}

	zones := make([]string, 0, len(templates))
	for _, template := range templates {
		zones = append(zones, fmt.Sprintf("%s/%s/%d", template.Region, template.Zone, template.TemplateID))
	}

	templateClass.Status.Zones = zones
	templateClass.StatusConditions().SetTrue(v1alpha1.ConditionTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}
