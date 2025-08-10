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

	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator"
)

func init() {
}

type Operator struct {
	*operator.Operator

	CapacityProvider *cloudcapacity.Provider
	// InstanceTypeProvider *[]cloudprovider.InstanceType
	InstanceProvider *instance.Provider
	InstanceTypes    []*cloudprovider.InstanceType
}

func NewOperator(ctx context.Context, operator *operator.Operator) (context.Context, *Operator) {
	log.FromContext(ctx).Info("Initializing Karpenter Proxmox Provider Operator", "cloud-config", options.FromContext(ctx).CloudConfigPath)

	cloudcapacityProvider, err := cloudcapacity.NewProvider(ctx)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed creating cloud capacity provider")

		os.Exit(1)
	}

	cloudcapacityProvider.Sync(ctx)

	instanceTypes, err := proxmox.ConstructInstanceTypes(ctx, cloudcapacityProvider)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed constructing instance types")

		os.Exit(1)
	}

	instanceProvider, err := instance.NewProvider(ctx, cloudcapacityProvider)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed creating instance provider")

		os.Exit(1)
	}

	return ctx, &Operator{
		Operator:         operator,
		CapacityProvider: cloudcapacityProvider,
		InstanceTypes:    instanceTypes,
		InstanceProvider: instanceProvider,
	}
}
