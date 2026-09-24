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

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// pollInterval is how often waiters re-check cluster state.
const pollInterval = 2 * time.Second

// readyObject is implemented by every CRD the suite waits on - both
// v1alpha1 template/nodeclass types and karpv1.NodePool - which all report
// their provisioning state through the same aggregate "Ready" condition
// (see pkg/apis/v1alpha1/nodetemplate_status.go, nodeclass_status.go, and
// vendor/sigs.k8s.io/karpenter/pkg/apis/v1/nodepool_status.go).
type readyObject interface {
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

// WaitForProxmoxTemplateNotReady polls the named ProxmoxTemplate until its
// aggregate "Ready" condition reports anything other than True - e.g. when
// none of spec.storageIDs resolve to real Proxmox storage, in which case
// the controller never sets status.imageID/zones at all and leaves Ready
// Unknown rather than False (see
// pkg/controllers/nodetemplateclass/status/instancetemplate.go).
func WaitForProxmoxTemplateNotReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxTemplate, error) {
	tmpl := &v1alpha1.ProxmoxTemplate{}

	err := waitForReadyState(ctx, cli, tmpl, "proxmoxtemplate", name, false, timeout)

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

// WaitForProxmoxUnmanagedTemplateNotReady polls the named
// ProxmoxUnmanagedTemplate until its aggregate "Ready" condition reports
// anything other than True - e.g. when spec.tags/templateName match no
// Proxmox VM template at all, which the controller reports as an explicit
// False condition with reason "TemplatesNotFound" (see
// pkg/controllers/nodetemplateunmanagedclass/status/instancetemplate.go).
func WaitForProxmoxUnmanagedTemplateNotReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxUnmanagedTemplate, error) {
	tmpl := &v1alpha1.ProxmoxUnmanagedTemplate{}

	err := waitForReadyState(ctx, cli, tmpl, "proxmoxunmanagedtemplate", name, false, timeout)

	return tmpl, err
}

// WaitForProxmoxUnmanagedTemplateGone polls until the named
// ProxmoxUnmanagedTemplate no longer exists. Unlike ProxmoxTemplate, it
// carries no termination finalizer - it never owns the Proxmox VM
// templates it matches - so deletion is expected to be near-immediate.
func WaitForProxmoxUnmanagedTemplateGone(ctx context.Context, cli client.Client, name string, timeout time.Duration) error {
	return waitForGone(ctx, cli, &v1alpha1.ProxmoxUnmanagedTemplate{}, "proxmoxunmanagedtemplate", name, timeout)
}

// WaitForProxmoxNodeClassReady polls the named (pre-existing)
// ProxmoxNodeClass until its aggregate "Ready" condition reports True -
// see pkg/apis/v1alpha1/nodeclass_status.go.
func WaitForProxmoxNodeClassReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxNodeClass, error) {
	nodeClass := &v1alpha1.ProxmoxNodeClass{}

	err := waitForReady(ctx, cli, nodeClass, "proxmoxnodeclass", name, timeout)

	return nodeClass, err
}

// WaitForProxmoxNodeClassNotReady polls the named ProxmoxNodeClass until
// its aggregate "Ready" condition reports anything other than True - e.g.
// after spec.metadataOptions.type is switched to "cdrom" but the
// referenced templatesRef secret doesn't exist yet (see
// pkg/controllers/nodeclass/status/metadataoptions.go).
func WaitForProxmoxNodeClassNotReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*v1alpha1.ProxmoxNodeClass, error) {
	nodeClass := &v1alpha1.ProxmoxNodeClass{}

	err := waitForReadyState(ctx, cli, nodeClass, "proxmoxnodeclass", name, false, timeout)

	return nodeClass, err
}

// WaitForNodePoolReady polls the named NodePool until its aggregate
// "Ready" condition reports True - i.e. Karpenter validated its spec and
// resolved its nodeClassRef to a Ready ProxmoxNodeClass (see
// ConditionTypeValidationSucceeded/ConditionTypeNodeClassReady in
// vendor/sigs.k8s.io/karpenter/pkg/apis/v1/nodepool_status.go). This is
// about the NodePool object itself being usable, not about any node it
// has launched yet.
func WaitForNodePoolReady(ctx context.Context, cli client.Client, name string, timeout time.Duration) (*karpv1.NodePool, error) {
	nodePool := &karpv1.NodePool{}

	err := waitForReady(ctx, cli, nodePool, "nodepool", name, timeout)

	return nodePool, err
}

// WaitForNodePoolGone deletes the named NodePool - with foreground
// propagation, so the API server blocks its actual removal until every
// NodeClaim it owns has finished terminating, rather than just marking it
// for deletion and returning immediately - and polls until it's gone,
// i.e. Karpenter's own termination finalizer (karpv1.TerminationFinalizer)
// has finished deprovisioning every NodeClaim/node it owns.
func WaitForNodePoolGone(ctx context.Context, cli client.Client, name string, timeout time.Duration) error {
	err := cli.Delete(ctx, &karpv1.NodePool{Name: name}, client.PropagationPolicy(metav1.DeletePropagationForeground))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete nodepool %s: %w", name, err)
	}

	return waitForGone(ctx, cli, &karpv1.NodePool{}, "nodepool", name, timeout)
}

// WaitForStatefulSetReplicasReady polls until the named StatefulSet
// reports the given number of ready replicas.
func WaitForStatefulSetReplicasReady(ctx context.Context, cli client.Client, namespace, name string, replicas int32, timeout time.Duration) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{}

	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, sts)

		switch {
		case apierrors.IsNotFound(err) || isTransientError(err):
			return false, nil
		case err != nil:
			return false, err
		}

		return sts.Status.ReadyReplicas == replicas, nil
	})
	if err != nil {
		return sts, fmt.Errorf("statefulset %s/%s did not reach %d ready replica(s): %w", namespace, name, replicas, err)
	}

	return sts, nil
}

// waitForReady polls obj (a zero-value pointer the caller wants filled in)
// by name until its aggregate "Ready" condition reports True.
func waitForReady[T readyObject](ctx context.Context, cli client.Client, obj T, kind, name string, timeout time.Duration) error {
	return waitForReadyState(ctx, cli, obj, kind, name, true, timeout)
}

// waitForReadyState polls obj (a zero-value pointer the caller wants
// filled in) by name until its aggregate "Ready" condition reports True
// (want=true) or anything other than True (want=false). An object with no
// Ready condition yet is treated as not-ready.
func waitForReadyState[T readyObject](ctx context.Context, cli client.Client, obj T, kind, name string, want bool, timeout time.Duration) error {
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
				return (cond.Status == metav1.ConditionTrue) == want, nil
			}
		}

		// No Ready condition yet: the controller has not reconciled the
		// object, so neither state has been reported.
		return false, nil
	})
	if err != nil {
		verb := "did not become ready"
		if !want {
			verb = "did not become unready"
		}

		return fmt.Errorf("%s %s %s: %w", kind, name, verb, err)
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
