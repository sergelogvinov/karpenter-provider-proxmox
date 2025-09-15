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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	metadataScanPeriod = 1 * time.Minute
)

type MetadataOptions struct {
	kubeClient client.Client
}

func (i *MetadataOptions) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	if nodeClass.Spec.MetadataOptions.Type == "cdrom" {
		if nodeClass.Spec.MetadataOptions.TemplatesRef == nil || nodeClass.Spec.MetadataOptions.TemplatesRef.Name == "" || nodeClass.Spec.MetadataOptions.TemplatesRef.Namespace == "" {
			nodeClass.StatusConditions().SetFalse(
				v1alpha1.ConditionInstanceMetadataOptionsReady,
				"MetadataOptionsNotFound",
				"metadataOptions.TemplatesRef is required when metadataOptions.Type is 'cdrom'",
			)

			return reconcile.Result{}, fmt.Errorf("metadataOptions.TemplatesRef is required when metadataOptions.Type is 'cdrom'")
		}

		secret := &corev1.Secret{}
		secretKey := client.ObjectKey{
			Name:      nodeClass.Spec.MetadataOptions.TemplatesRef.Name,
			Namespace: nodeClass.Spec.MetadataOptions.TemplatesRef.Namespace,
		}

		if err := i.kubeClient.Get(ctx, secretKey, secret); err != nil {
			if apierrors.IsNotFound(err) {
				nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionInstanceMetadataOptionsReady, "MetadataOptionsNotFound", "Metadata TemplatesRef secret resource not found")

				return reconcile.Result{RequeueAfter: metadataScanPeriod}, nil
			}

			return reconcile.Result{}, err
		}
	}

	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionInstanceMetadataOptionsReady)

	return reconcile.Result{}, nil
}
