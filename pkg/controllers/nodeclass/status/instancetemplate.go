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
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	templateScanPeriod = 15 * time.Second
)

type InstanceTemplate struct {
	kubeClient            client.Client
	cloudCapacityProvider cloudcapacity.Provider
}

func (i *InstanceTemplate) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	template, err := nodeClass.ReadTemplate(ctx, i.kubeClient)
	if err != nil {
		if errors.IsNotFound(err) {
			nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionNodeClassTemplateReady, "TemplatesNotFound", "Proxmox Template resource reference did not find")
		}

		return reconcile.Result{}, fmt.Errorf("resolving TemplateClass from nodeClass failed: %w", err)
	}

	placements := template.TemplatePlacements()

	if nodeClass.Spec.Region != "" {
		placements = lo.Filter(placements, func(p v1alpha1.TemplatePlacement, _ int) bool {
			return p.Region == nodeClass.Spec.Region
		})
	}

	availablePlacements := []v1alpha1.TemplatePlacement{}

	for _, region := range i.cloudCapacityProvider.Regions() {
		storage := i.cloudCapacityProvider.GetStorage(region, nodeClass.Spec.BootDevice.Storage)
		if storage == nil {
			continue
		}

		for _, z := range storage.Zones {
			if placement, ok := lo.Find(placements, func(p v1alpha1.TemplatePlacement) bool {
				return p.Region == region && p.Zone == z
			}); ok {
				availablePlacements = append(availablePlacements, placement)
			}
		}
	}

	nodeClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(availablePlacements))),
	}
	nodeClass.SetTemplatePlacements(availablePlacements)

	if len(availablePlacements) == 0 {
		if ready := templateReady(template); ready != nil && droppedForCPUAffinity(ready.Reason) {
			nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionNodeClassTemplateReady, v1alpha1.ReasonTemplateCPUAffinitySet, ready.Message)

			return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
		}

		nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionNodeClassTemplateReady, "TemplatesNotFound", "Proxmox Template did not match the node class requirements")

		return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
	}

	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionNodeClassTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}

// templateReady returns the template's Ready condition, nil when it has none.
func templateReady(template v1alpha1.ProxmoxCommonTemplate) *status.Condition {
	obj, ok := template.(status.Object)
	if !ok {
		return nil
	}

	return obj.StatusConditions().Get(v1alpha1.ConditionTemplateReady)
}

// droppedForCPUAffinity reports whether a template Ready reason says VM
// templates were dropped for having affinity set, so a NodeClass left with
// no template placements may have lost them to it, e.g. in its region.
func droppedForCPUAffinity(reason string) bool {
	return reason == v1alpha1.ReasonTemplateCPUAffinitySet || reason == v1alpha1.ReasonTemplateCPUAffinityDropped
}
