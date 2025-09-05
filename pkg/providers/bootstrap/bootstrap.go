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

package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const BootstrapUserPostfix = "karpenter:proxmox"

type Provider interface {
	CreateToken(context.Context, *karpv1.NodeClaim) (string, error)
	DeleteToken(context.Context, string) error
	DeleteExpiredTokens(context.Context) error
}

type DefaultProvider struct {
	kubernetesInterface kubernetes.Interface
}

func NewProvider(
	ctx context.Context,
	kubernetesInterface kubernetes.Interface,
) *DefaultProvider {
	return &DefaultProvider{
		kubernetesInterface: kubernetesInterface,
	}
}

func (p *DefaultProvider) CreateToken(ctx context.Context, nodeClaim *karpv1.NodeClaim) (string, error) {
	token, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", err
	}

	t := strings.Split(token, ".")
	tokenID := t[0]
	tokenSecret := t[1]
	tokenExpiredTime := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootstraputil.BootstrapTokenSecretName(tokenID),
			Namespace: metav1.NamespaceSystem,
			Labels: map[string]string{
				v1alpha1.LabelBootstrapToken: "true",
			},
		},
		Type: bootstrapapi.SecretTypeBootstrapToken,
		StringData: map[string]string{
			bootstrapapi.BootstrapTokenIDKey:               tokenID,
			bootstrapapi.BootstrapTokenSecretKey:           tokenSecret,
			bootstrapapi.BootstrapTokenUsageAuthentication: "true",
			bootstrapapi.BootstrapTokenExtraGroupsKey:      fmt.Sprintf("%s:%s", bootstrapapi.BootstrapDefaultGroup, BootstrapUserPostfix),
			bootstrapapi.BootstrapTokenDescriptionKey:      "Karpenter Proxmox Bootstrap Token",
			bootstrapapi.BootstrapTokenExpirationKey:       tokenExpiredTime,
		},
	}

	if _, err := p.kubernetesInterface.CoreV1().Secrets(metav1.NamespaceSystem).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return "", err
	}

	nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
		v1alpha1.AnnotationProxmoxCloudInitToken: tokenID,
	})

	return token, nil
}

func (p *DefaultProvider) DeleteToken(ctx context.Context, tokenID string) error {
	if tokenID == "" {
		return fmt.Errorf("tokenID is required")
	}

	if err := p.kubernetesInterface.CoreV1().Secrets(metav1.NamespaceSystem).Delete(ctx, bootstraputil.BootstrapTokenSecretName(tokenID), metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return err
	}

	return nil
}

func (p *DefaultProvider) DeleteExpiredTokens(ctx context.Context) error {
	secrets, err := p.kubernetesInterface.CoreV1().Secrets(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{
		LabelSelector: v1alpha1.LabelBootstrapToken + "=true",
	})
	if err != nil {
		return fmt.Errorf("listing bootstrap tokens: %w", err)
	}

	for _, secret := range secrets.Items {
		if secret.Type == bootstrapapi.SecretTypeBootstrapToken {
			if expBytes, ok := secret.Data["expiration"]; ok {
				expTime, err := time.Parse(time.RFC3339, string(expBytes))
				if err != nil {
					continue
				}

				if time.Now().After(expTime) {
					p.DeleteToken(ctx, strings.TrimPrefix(secret.Name, bootstrapapi.BootstrapTokenSecretPrefix))
				}
			}
		}
	}

	return nil
}
