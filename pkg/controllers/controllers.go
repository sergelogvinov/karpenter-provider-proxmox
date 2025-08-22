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

	nodeclasshash "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodeclass/hash"
	nodeclaasstatus "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodeclass/status"
	nodetemplateclasshash "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateclass/hash"
	nodetemplateclassstatus "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateclass/status"
	nodetemplateunmanagedclasshash "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateunmanagedclass/hash"
	nodetemplateunmanagedclassstatus "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/nodetemplateunmanagedclass/status"
	cloudcapacitynode "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/providers/cloudcapacity/node"
	cloudcapacitynodeload "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/controllers/providers/cloudcapacity/nodeload"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	"k8s.io/utils/clock"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
)

func NewControllers(ctx context.Context, mgr manager.Manager, clk clock.Clock,
	kubeClient client.Client, recorder events.Recorder,
	cloudProvider cloudprovider.CloudProvider,
	instanceTemplateProvider instancetemplate.Provider,
	cloudCapacityProvider cloudcapacity.Provider,
) []controller.Controller {
	controllers := []controller.Controller{
		nodeclasshash.NewController(kubeClient),
		nodeclaasstatus.NewController(kubeClient, instanceTemplateProvider),
		nodetemplateclasshash.NewController(kubeClient),
		nodetemplateclassstatus.NewController(kubeClient, instanceTemplateProvider),
		nodetemplateunmanagedclasshash.NewController(kubeClient),
		nodetemplateunmanagedclassstatus.NewController(kubeClient, instanceTemplateProvider),
		cloudcapacitynode.NewController(cloudCapacityProvider),
		cloudcapacitynodeload.NewController(cloudCapacityProvider),
	}

	return controllers
}
