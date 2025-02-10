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

package controllers

import (
	"context"

	"github.com/awslabs/operatorpkg/controller"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	nodeclasshash "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodeclass/hash"
	nodeclaasstatus "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodeclass/status"
)

func NewControllers(ctx context.Context, mgr manager.Manager, clk clock.Clock,
	kubeClient client.Client, recorder events.Recorder,
	cloudProvider cloudprovider.CloudProvider,
) []controller.Controller {

	controllers := make([]controller.Controller, 0)

	// Add nodeclass hash controller
	if hashCtrl, err := nodeclasshash.NewController(kubeClient); err == nil {
		controllers = append(controllers, hashCtrl)
	}

	// Add nodeclass status controller
	if statusCtrl, err := nodeclaasstatus.NewController(kubeClient); err == nil {
		controllers = append(controllers, statusCtrl)
	}

	return controllers
}
