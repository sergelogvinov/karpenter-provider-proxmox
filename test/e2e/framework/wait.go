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
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/status"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// pollInterval is how often waiters re-check cluster state.
const pollInterval = 2 * time.Second

// templateObject is implemented by both *v1alpha1.ProxmoxTemplate and
// *v1alpha1.ProxmoxUnmanagedTemplate - both report their provisioning
// state through the same aggregate "Ready" condition (see
// pkg/apis/v1alpha1/nodetemplate_status.go).
type templateObject interface {
	client.Object
	GetConditions() []status.Condition
}

// WaitForProxmoxTemplateReady polls the named ProxmoxTemplate until its
// aggregate "Ready" condition (the same one kubectl's Ready printcolumn
// reads, see pkg/apis/v1alpha1/nodetemplate.go) reports True.
func WaitForProxmoxTemplateReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxTemplate, error) {
	tmpl := &v1alpha1.ProxmoxTemplate{}

	err := waitForReady(ctx, cli, tmpl, "proxmoxtemplate", name, timeout)

	return tmpl, err
}

// WaitForProxmoxTemplateGone polls until the named ProxmoxTemplate no
// longer exists - i.e. its termination finalizer (see
// pkg/controllers/nodetemplateclass/termination) has finished tearing down
// every Proxmox-side template/image it provisioned.
func WaitForProxmoxTemplateGone(ctx context.Context, cli client.Client, name string, timeout time.Duration) error {
	return waitForGone(ctx, cli, &v1alpha1.ProxmoxTemplate{}, "proxmoxtemplate", name, timeout)
}

// WaitForProxmoxUnmanagedTemplateReady polls the named ProxmoxUnmanagedTemplate
// until its aggregate "Ready" condition reports True - i.e. the controller
// found at least one Proxmox VM template matching spec.templateName/tags
// (see pkg/controllers/nodetemplateunmanagedclass/status).
func WaitForProxmoxUnmanagedTemplateReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxUnmanagedTemplate, error) {
	tmpl := &v1alpha1.ProxmoxUnmanagedTemplate{}

	err := waitForReady(ctx, cli, tmpl, "proxmoxunmanagedtemplate", name, timeout)

	return tmpl, err
}

// WaitForProxmoxUnmanagedTemplateGone polls until the named
// ProxmoxUnmanagedTemplate no longer exists. Unlike ProxmoxTemplate, it
// carries no termination finalizer - it never owns the Proxmox VM
// templates it matches - so deletion is expected to be near-immediate.
func WaitForProxmoxUnmanagedTemplateGone(ctx context.Context, cli client.Client, name string, timeout time.Duration) error {
	return waitForGone(ctx, cli, &v1alpha1.ProxmoxUnmanagedTemplate{}, "proxmoxunmanagedtemplate", name, timeout)
}

// waitForReady polls obj (a zero-value pointer the caller wants filled in)
// by name until its aggregate "Ready" condition reports True.
func waitForReady[T templateObject](ctx context.Context, cli client.Client, obj T, kind, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		err := cli.Get(ctx, client.ObjectKey{Name: name}, obj)

		switch {
		case apierrors.IsNotFound(err) || isTransientError(err):
			return false, nil
		case err != nil:
			return false, err
		}

		for _, cond := range obj.GetConditions() {
			if cond.Type == status.ConditionReady {
				return cond.Status == metav1.ConditionTrue, nil
			}
		}

		return false, nil
	})
	if err != nil {
		return fmt.Errorf("%s %s did not become ready: %w", kind, name, err)
	}

	return nil
}

// waitForGone polls until the named object of obj's type no longer exists.
func waitForGone[T client.Object](ctx context.Context, cli client.Client, obj T, kind, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		err := cli.Get(ctx, client.ObjectKey{Name: name}, obj)

		switch {
		case apierrors.IsNotFound(err):
			return true, nil
		case isTransientError(err):
			return false, nil
		case err != nil:
			return false, err
		}

		return false, nil
	})
	if err != nil {
		return fmt.Errorf("%s %s was not deleted: %w", kind, name, err)
	}

	return nil
}
