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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/templatecheck"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TemplateCompatible sets ConditionNodeClassTemplateCompatible from the same check that drops the node class's offerings in
// instancetype.List. It runs after InstanceTemplate, on the template placements it selected, and is rerun
// with it every templateScanPeriod, so the condition follows changes to the template's status.
type TemplateCompatible struct {
	kubeClient            client.Client
	cloudCapacityProvider cloudcapacity.Provider
}

func (i *TemplateCompatible) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	templatecheck.ForNodeClass(ctx, i.kubeClient, nodeClass, i.cloudCapacityProvider).SetCondition(nodeClass)

	return reconcile.Result{RequeueAfter: templateScanPeriod}, nil
}
