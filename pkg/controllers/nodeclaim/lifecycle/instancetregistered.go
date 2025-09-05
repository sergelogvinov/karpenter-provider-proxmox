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

package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/bootstrap"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

type InstanceRegistered struct {
	kubeClient                  client.Client
	kubernetesBootstrapProvider bootstrap.Provider
	instanceProvider            instance.Provider
}

func (i *InstanceRegistered) Reconcile(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	if !nodeClaim.StatusConditions().Get(karpv1.ConditionTypeInitialized).IsTrue() {
		return reconcile.Result{}, nil
	}

	if !strings.HasPrefix(nodeClaim.Status.ProviderID, proxmox.ProxmoxProviderPrefix) {
		return reconcile.Result{}, nil
	}

	if _, ok := nodeClaim.Annotations[v1alpha1.AnnotationProxmoxCloudInitStatus]; ok {
		return reconcile.Result{}, nil
	}

	nodeClass, err := i.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		return reconcile.Result{}, err
	}

	if nodeClass.Spec.MetadataOptions.Type == "cdrom" {
		err = i.instanceProvider.DetachCloudInit(ctx, nodeClaim)
		if err != nil {
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}
	}

	if nodeClaim.Annotations[v1alpha1.AnnotationProxmoxCloudInitToken] != "" {
		err = i.kubernetesBootstrapProvider.DeleteToken(ctx, nodeClaim.Annotations[v1alpha1.AnnotationProxmoxCloudInitToken])
		if err != nil {
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}
	}

	nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxCloudInitStatus: string(karpv1.ConditionTypeInitialized),
	})

	return reconcile.Result{}, nil
}

func (i *InstanceRegistered) resolveNodeClassFromNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.ProxmoxNodeClass, error) {
	ref := nodeClaim.Spec.NodeClassRef
	if ref == nil {
		return nil, fmt.Errorf("nodeClaim missing NodeClassRef")
	}

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	if err := i.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, nodeClass); err != nil {
		return nil, err
	}

	return nodeClass, nil
}
