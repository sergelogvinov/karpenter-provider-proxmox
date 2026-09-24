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

package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestNodePoolLifecycle exercises the common node-provisioning path against
// an already-configured ProxmoxNodeClass/ProxmoxUnmanagedTemplate (see
// docs/deploy/nodepool.yaml, and E2E_NODE_CLASS/E2E_UNMANAGED_TEMPLATE -
// the suite never creates either): create a NodePool, deploy a
// single-replica StatefulSet pinned to only the nodes that NodePool
// provisions (via the suite's own framework.NodePoolTestLabelKey node
// label), wait for it to come up, then scale to 3 replicas and wait for
// all of them to come up too.
func TestNodePoolLifecycle(t *testing.T) {
	f := framework.New(t)
	require := require.New(t)

	ctx, cancel := f.Context()
	_, err := framework.WaitForProxmoxUnmanagedTemplateReady(ctx, f.Client.Objects, f.Config.UnmanagedTemplateName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxunmanagedtemplate %s (E2E_UNMANAGED_TEMPLATE) is not ready - is it already deployed in the target cluster?", f.Config.UnmanagedTemplateName)

	ctx, cancel = f.Context()
	_, err = framework.WaitForProxmoxNodeClassReady(ctx, f.Client.Objects, f.Config.NodeClassName, f.Config.Timeout)

	cancel()
	require.NoError(err, "proxmoxnodeclass %s (E2E_NODE_CLASS) is not ready - is it already deployed in the target cluster?", f.Config.NodeClassName)

	nodePoolName := f.Name()

	nodePool := framework.NewNodePool(framework.NodePoolOptions{
		Name:          nodePoolName,
		NodeClassName: f.Config.NodeClassName,
	})

	f.Logf("creating nodepool %s (nodeClass=%s)", nodePoolName, f.Config.NodeClassName)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, nodePool)

	cancel()
	require.NoError(err, "failed to create nodepool %s", nodePoolName)

	t.Cleanup(func() {
		done := f.Step("deleting nodepool %s (draining every node it provisioned)", nodePoolName)
		ctx, cancel := f.ContextTimeout(f.Config.NodeTimeout)
		err := framework.WaitForNodePoolGone(ctx, f.Client.Objects, nodePoolName, f.Config.NodeTimeout)

		cancel()
		done()

		if err != nil {
			t.Errorf("nodepool %s did not clean up: %v", nodePoolName, err)
		}
	})

	done := f.Step("waiting for nodepool %s to become ready", nodePoolName)
	ctx, cancel = f.Context()
	_, err = framework.WaitForNodePoolReady(ctx, f.Client.Objects, nodePoolName, f.Config.Timeout)

	cancel()
	done()
	require.NoError(err, "nodepool %s never became ready", nodePoolName)

	// Every node this NodePool provisions carries this label - see
	// framework.NewNodePool - so pinning the workload to it guarantees
	// its pods land only on nodes this test itself is responsible for.
	nodeSelector := map[string]string{framework.NodePoolTestLabelKey: nodePoolName}

	stsName := f.Name()

	sts := framework.NewWorkloadStatefulSet(framework.StatefulSetOptions{
		Name:         stsName,
		Namespace:    f.Config.Namespace,
		Replicas:     1,
		NodeSelector: nodeSelector,
	})

	f.Logf("creating statefulset %s/%s (replicas=1, nodeSelector=%v)", f.Config.Namespace, stsName, nodeSelector)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Create(ctx, sts)

	cancel()
	require.NoError(err, "failed to create statefulset %s/%s", f.Config.Namespace, stsName)

	t.Cleanup(func() {
		f.Logf("deleting statefulset %s/%s", f.Config.Namespace, stsName)

		ctx, cancel := f.Context()
		err := f.Client.Objects.Delete(ctx, sts)

		cancel()

		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("failed to delete statefulset %s/%s: %v", f.Config.Namespace, stsName, err)
		}
	})

	done = f.Step("waiting for statefulset %s/%s to reach 1 ready replica (a new Proxmox VM must boot and join)", f.Config.Namespace, stsName)
	ctx, cancel = f.ContextTimeout(f.Config.NodeTimeout)
	_, err = framework.WaitForStatefulSetReplicasReady(ctx, f.Client.Objects, f.Config.Namespace, stsName, 1, f.Config.NodeTimeout)

	cancel()
	done()
	require.NoError(err, "statefulset %s/%s never reached 1 ready replica", f.Config.Namespace, stsName)

	f.Logf("statefulset %s/%s is up with 1 replica, scaling to 3", f.Config.Namespace, stsName)

	ctx, cancel = f.Context()
	err = f.Client.Objects.Get(ctx, client.ObjectKeyFromObject(sts), sts)

	cancel()
	require.NoError(err, "failed to refresh statefulset %s/%s before scaling", f.Config.Namespace, stsName)

	replicas := int32(3)
	sts.Spec.Replicas = &replicas

	ctx, cancel = f.Context()
	err = f.Client.Objects.Update(ctx, sts)

	cancel()
	require.NoError(err, "failed to scale statefulset %s/%s to %d replicas", f.Config.Namespace, stsName, replicas)

	done = f.Step("waiting for statefulset %s/%s to reach %d ready replicas (up to %d more Proxmox VMs must boot and join)", f.Config.Namespace, stsName, replicas, replicas-1)
	ctx, cancel = f.ContextTimeout(f.Config.NodeTimeout)
	scaled, err := framework.WaitForStatefulSetReplicasReady(ctx, f.Client.Objects, f.Config.Namespace, stsName, replicas, f.Config.NodeTimeout)

	cancel()
	done()
	require.NoError(err, "statefulset %s/%s never reached %d ready replicas", f.Config.Namespace, stsName, replicas)

	f.Logf("statefulset %s/%s is up with %d/%d ready replicas", f.Config.Namespace, stsName, scaled.Status.ReadyReplicas, replicas)
}
