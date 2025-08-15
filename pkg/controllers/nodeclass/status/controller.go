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
	"slices"
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/awslabs/operatorpkg/status"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// Condition types
	ConditionTypeAutoPlacement = "AutoPlacement"
)

// Controller reconciles an ProxmoxNodeClass object to update its status
type Controller struct {
	kubeClient               client.Client
	instanceTemplateProvider instancetemplate.Provider
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, instanceTemplateProvider instancetemplate.Provider) (*Controller, error) {
	return &Controller{
		kubeClient:               kubeClient,
		instanceTemplateProvider: instanceTemplateProvider,
	}, nil
}

func (c *Controller) Name() string {
	return "nodeclass.status"
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) (reconcile.Result, error) {
	// Initialize status if needed
	if nodeClass.Status.Conditions == nil {
		nodeClass.Status.Conditions = []metav1.Condition{}
	}

	if nodeClass.Status.SelectedZones == nil {
		nodeClass.Status.SelectedZones = []string{}
	}

	// Validate the nodeclass configuration
	if err := c.validateNodeClass(ctx, nodeClass); err != nil {
		patch := client.MergeFrom(nodeClass.DeepCopy())
		nodeClass.Status.LastValidationTime = metav1.Now()
		nodeClass.Status.ValidationError = err.Error()
		if err := c.kubeClient.Status().Patch(ctx, nodeClass, patch); err != nil {
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, err
	}

	// Clear any previous validation error
	if nodeClass.Status.ValidationError != "" {
		patch := client.MergeFrom(nodeClass.DeepCopy())
		nodeClass.Status.LastValidationTime = metav1.Now()
		nodeClass.Status.ValidationError = ""
		if err := c.kubeClient.Status().Patch(ctx, nodeClass, patch); err != nil {
			return reconcile.Result{}, err
		}
	}

	if nodeClass.Status.ValidationError == "" {
		templates, err := c.instanceTemplateProvider.List(ctx, nodeClass)
		if err != nil || templates == nil {
			c.updateCondition(nodeClass, ConditionTypeAutoPlacement, metav1.ConditionFalse, "ZoneSelectionNotStarted", "Zone selection has not started yet")

			return reconcile.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf("listing instance templates: %w", err)
		}

		zones := make([]string, 0, len(templates))
		for _, template := range templates {
			if slices.Contains(zones, template.Zone) {
				continue
			}

			zones = append(zones, fmt.Sprintf("%s/%s", template.Region, template.Zone))
		}

		if !slices.Equal(nodeClass.Status.SelectedZones, zones) {
			if len(nodeClass.Status.SelectedZones) == 0 {
				c.updateCondition(nodeClass, ConditionTypeAutoPlacement, metav1.ConditionTrue, "ZoneSelectionSucceeded", "Zone selection completed successfully")
			}

			nodeClass.Status.Resources = corev1.ResourceList{
				v1alpha1.ResourceZones: resource.MustParse(strconv.Itoa(len(zones))),
			}

			nodeClass.Status.SelectedZones = zones

			if err := c.kubeClient.Status().Update(ctx, nodeClass); err != nil {
				return reconcile.Result{}, fmt.Errorf("updating nodeclass status: %w", err)
			}
		}
	}

	if nodeClass.Status.ValidationError == "" && len(nodeClass.Status.SelectedZones) > 0 {
		nodeClass.StatusConditions().SetTrue(status.ConditionReady)

		if err := c.kubeClient.Status().Update(ctx, nodeClass); err != nil {
			return reconcile.Result{}, fmt.Errorf("updating nodeclass status: %w", err)
		}
	}

	return reconcile.Result{}, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&v1alpha1.ProxmoxNodeClass{}).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 5,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

// validateNodeClass performs validation of the ProxmoxNodeClass configuration
func (c *Controller) validateNodeClass(_ context.Context, nc *v1alpha1.ProxmoxNodeClass) error {
	if nc.Spec.InstanceTemplate.Type == "" || nc.Spec.InstanceTemplate.Name == "" {
		return fmt.Errorf("instanceTemplate.Type and instanceTemplate.Name are required")
	}

	if nc.Spec.InstanceTemplate.Type != "template" {
		return fmt.Errorf("instanceTemplate.Type must be 'template', got '%s'", nc.Spec.InstanceTemplate.Type)
	}

	if nc.Spec.MetadataOptions.Type == "cdrom" {
		if nc.Spec.MetadataOptions.SecretRef.Name == "" || nc.Spec.MetadataOptions.SecretRef.Namespace == "" {
			return fmt.Errorf("metadataOptions.SecretRef is required when metadataOptions.Type is 'cdrom'")
		}
	}

	return nil
}

// updateCondition updates a condition in the nodeclass status
func (c *Controller) updateCondition(nodeClass *v1alpha1.ProxmoxNodeClass, conditionType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	newCondition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: nodeClass.Generation,
	}

	// Find and update existing condition or append new one
	for i, existingCond := range nodeClass.Status.Conditions {
		if existingCond.Type == conditionType {
			if existingCond.Status != status {
				nodeClass.Status.Conditions[i] = newCondition
			}

			return
		}
	}

	// Append new condition if not found
	nodeClass.Status.Conditions = append(nodeClass.Status.Conditions, newCondition)
}
