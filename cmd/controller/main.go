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

// Package main
package main

import (
	"flag"
	"fmt"
	"os"

	proxmox "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/cloudprovider"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator"

	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

func main() {
	if err := validateOptions(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, op := operator.NewOperator(coreoperator.NewOperator())
	log := op.GetLogger()

	log.Info("Karpenter Proxmox Provider version", "version", coreoperator.Version)

	proxmoxCloudProvider := proxmox.NewCloudProvider(
		ctx,
		op.GetClient(),
		op.EventRecorder,
		op.InstanceProvider,
		op.InstanceTemplateProvider,
		op.InstanceTypeProvider,
		op.CloudCapacityProvider)

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
			proxmoxCloudProvider,
			clusterState,
			op.InstanceTypeStore,
			op.PredictionStore,
		)...).
		WithControllers(ctx, controllers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.EventRecorder,
			cloudProvider,
			op.KubernetesBootstrapProvider,
			op.InstanceProvider,
			op.InstanceTemplateProvider,
			op.InstanceTypeProvider,
			op.CloudCapacityProvider,
			op.NodeIpamController,
			op.Privileges,
		)...).
		Start(ctx)
}

// validateOptions parses the flags before the core operator does, so invalid
// options exit with an error instead of the core operator's panic.
func validateOptions() error {
	fs := &coreoptions.FlagSet{
		FlagSet: flag.NewFlagSet("karpenter", flag.ContinueOnError),
	}
	for _, opt := range coreoptions.Injectables {
		opt.AddFlags(fs)
	}

	for _, opt := range coreoptions.Injectables {
		if err := opt.Parse(fs, os.Args[1:]...); err != nil {
			return err
		}
	}

	return nil
}
