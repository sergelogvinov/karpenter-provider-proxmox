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

package operator

import (
	"context"
	"os"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	providerconfig "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
	pxpool "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/proxmoxpool"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/operator"
)

func init() {
}

type Operator struct {
	*operator.Operator

	ProxmoxPool              *pxpool.ProxmoxPool
	CloudCapacityProvider    cloudcapacity.Provider
	InstanceProvider         instance.Provider
	InstanceTemplateProvider instancetemplate.Provider
	InstanceTypeProvider     instancetype.Provider
}

func NewOperator(ctx context.Context, operator *operator.Operator) (context.Context, *Operator) {
	log.FromContext(ctx).Info("Initializing Karpenter Proxmox Provider Operator", "cloud-config", options.FromContext(ctx).CloudConfigPath)

	cfg, err := providerconfig.ReadCloudConfigFromFile(options.FromContext(ctx).CloudConfigPath)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to read cloud config")

		os.Exit(1)
	}

	pxPool, err := pxpool.NewProxmoxPool(ctx, cfg.Clusters)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to create proxmox cluster client")

		os.Exit(1)
	}

	cloudCapacityProvider := cloudcapacity.NewProvider(ctx, pxPool)
	cloudCapacityProvider.UpdateNodeCapacity(ctx)

	instanceTemplateProvider := instancetemplate.NewDefaultProvider(ctx, pxPool, cloudCapacityProvider)
	instanceTemplateProvider.UpdateInstanceTemplates(ctx)

	instanceTypeProvider := instancetype.NewDefaultProvider(ctx, cloudCapacityProvider)
	if err = instanceTypeProvider.UpdateInstanceTypes(ctx); err != nil {
		log.FromContext(ctx).Error(err, "failed to update instance types")

		os.Exit(1)
	}

	instanceProvider, err := instance.NewProvider(ctx, operator.KubernetesInterface, pxPool, cloudCapacityProvider, instanceTemplateProvider)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed creating instance provider")

		os.Exit(1)
	}

	return ctx, &Operator{
		Operator:                 operator,
		ProxmoxPool:              pxPool,
		CloudCapacityProvider:    cloudCapacityProvider,
		InstanceTemplateProvider: instanceTemplateProvider,
		InstanceTypeProvider:     instanceTypeProvider,
		InstanceProvider:         instanceProvider,
	}
}
