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

package main

import (
	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"

	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
)

func main() {
	ctx, op := coreoperator.NewOperator()
	log := op.GetLogger()

	log.Info("Karpenter Proxmox Provider version", "version", coreoperator.Version)

	instanceTypes, err := proxmox.ConstructInstanceTypes(ctx)
	if err != nil {
		log.Error(err, "failed constructing instance types")
	}

	proxmoxCloudProvider := proxmox.NewCloudProvider(ctx, op.GetClient(), instanceTypes)
	cloudProvider := metrics.Decorate(proxmoxCloudProvider)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

	op.
		WithControllers(ctx, corecontrollers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.EventRecorder,
			cloudProvider,
			clusterState,
		)...).
		Start(ctx)
}
