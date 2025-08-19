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
	"slices"
	"strconv"
	"time"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	templateScanPeriod = 5 * time.Minute
)

type InstanceTemplate struct {
	instanceTemplateProvider instancetemplate.Provider
}

func (i *InstanceTemplate) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	log := log.FromContext(ctx)

	if nodeClass.Spec.InstanceTemplate.Type != "template" {
		nodeClass.Status.SelectedZones = nil
		nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionInstanceTemplateReady, "TemplatesNotFound", "Instance Template did not match the node class requirements")

		return reconcile.Result{}, fmt.Errorf("instanceTemplate.Type must be 'template', got '%s'", nodeClass.Spec.InstanceTemplate.Type)
	}

	templates, err := i.instanceTemplateProvider.List(ctx, nodeClass)
	if err != nil {
		log.Error(err, "listing images")

		return reconcile.Result{}, err
	}

	if len(templates) == 0 {
		nodeClass.Status.SelectedZones = nil
		nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionInstanceTemplateReady, "TemplatesNotFound", "Instance Template did not match the node class requirements")

		return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
	}

	zones := make([]string, 0, len(templates))
	for _, template := range templates {
		if slices.Contains(zones, template.Zone) {
			continue
		}

		zones = append(zones, fmt.Sprintf("%s/%s", template.Region, template.Zone))
	}

	nodeClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(zones))),
	}
	nodeClass.Status.SelectedZones = zones
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionInstanceTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}
