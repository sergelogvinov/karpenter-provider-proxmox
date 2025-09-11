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
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

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
	template, err := i.resolveProxmoxTemplateFromNodeClass(ctx, nodeClass)
	if err != nil {
		if errors.IsNotFound(err) {
			nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionInstanceTemplateReady, "TemplatesNotFound", "Proxmox Template resource reference did not find")
		}

		return reconcile.Result{}, fmt.Errorf("resolving TemplateClass from nodeClass failed: %w", err)
	}

	zones := template.GetZones()

	if nodeClass.Spec.Region != "" {
		zones = lo.Filter(zones, func(zone string, _ int) bool {
			region := strings.SplitN(zone, "/", 2)[0]

			return region == nodeClass.Spec.Region
		})
	}

	availableZones := []string{}

	for _, region := range i.cloudCapacityProvider.Regions() {
		storage := i.cloudCapacityProvider.GetStorage(region, nodeClass.Spec.BootDevice.Storage)
		if storage == nil {
			continue
		}

		for _, z := range storage.Zones {
			key := fmt.Sprintf("%s/%s/", region, z)

			if zone, ok := lo.Find(zones, func(item string) bool {
				return strings.HasPrefix(item, key)
			}); ok {
				availableZones = append(availableZones, zone)
			}
		}
	}

	nodeClass.Status.Resources = corev1.ResourceList{
		v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(availableZones))),
	}
	nodeClass.Status.SelectedZones = availableZones

	if len(availableZones) == 0 {
		nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionInstanceTemplateReady, "TemplatesNotFound", "Proxmox Template did not match the node class requirements")

		return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
	}

	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionInstanceTemplateReady)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}

func (i *InstanceTemplate) resolveProxmoxTemplateFromNodeClass(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (v1alpha1.ProxmoxCommonTemplate, error) {
	ref := nodeClass.Spec.InstanceTemplateRef
	if ref == nil {
		return nil, fmt.Errorf("nodeClaim missing InstanceTemplateRef")
	}

	var obj client.Object

	switch ref.Kind {
	case "ProxmoxUnmanagedTemplate":
		obj = &v1alpha1.ProxmoxUnmanagedTemplate{}
	case "ProxmoxTemplate":
		obj = &v1alpha1.ProxmoxTemplate{}
	default:
		return nil, fmt.Errorf("unsupported InstanceTemplateRef kind: %s", ref.Kind)
	}

	if err := i.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, obj); err != nil {
		return nil, err
	}

	template, ok := obj.(v1alpha1.ProxmoxCommonTemplate)
	if !ok {
		return nil, fmt.Errorf("unsupported InstanceTemplateRef kind: %s", ref.Kind)
	}

	return template, nil
}
