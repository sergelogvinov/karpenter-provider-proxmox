//go:build e2e

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

package framework

import (
	"fmt"

	// Registers ProxmoxNodeClass/ProxmoxTemplate/ProxmoxUnmanagedTemplate
	// into k8s.io/client-go/kubernetes/scheme.Scheme as a side effect of
	// being imported - see pkg/apis/v1alpha1/doc.go.
	_ "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Client bundles the clients the e2e suite needs against the cluster under test.
type Client struct {
	RESTConfig *rest.Config

	// Objects is a controller-runtime client sharing client-go's global
	// scheme.Scheme, which has the karpenter.proxmox.sinextra.dev/v1alpha1
	// types registered - so it can Get/List/Create/Delete ProxmoxTemplate
	// and friends like any built-in Kubernetes type.
	Objects client.Client
}

// NewClient builds a Client from the given kubeconfig path (empty uses
// client-go's default loading rules: $KUBECONFIG, then ~/.kube/config).
func NewClient(kubeconfig string) (*Client, error) {
	restConfig, err := loadRESTConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	cli, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to build controller-runtime client: %w", err)
	}

	return &Client{
		RESTConfig: restConfig,
		Objects:    cli,
	}, nil
}

func loadRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
}
