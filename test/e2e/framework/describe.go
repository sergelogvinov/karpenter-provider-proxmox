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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Describe renders obj as YAML - the same shape `kubectl get -o yaml`
// prints, minus metadata.managedFields, which kubectl strips by default
// too (it's server-side-apply bookkeeping, not something a human debugging
// a test failure cares about) - for embedding in a test failure message. A
// wait that times out is otherwise hard to debug after the fact:
// t.Cleanup usually deletes the resource before a human gets a chance to
// kubectl get/describe it by name themselves, so the last-observed state
// needs to be captured in the test output directly.
func Describe(obj client.Object) string {
	copyObj, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Sprintf("<failed to copy %T for debugging>", obj)
	}

	copyObj.SetManagedFields(nil)

	data, err := yaml.Marshal(copyObj)
	if err != nil {
		return fmt.Sprintf("<failed to marshal %T for debugging: %v>", obj, err)
	}

	return string(data)
}

// DescribeOnFailure logs each of objs' current YAML state via f.Logf when
// ok is false. Called with the same condition a require.* call right after
// it is about to assert, e.g.:
//
//	f.DescribeOnFailure(len(nodeClass.Status.Conditions) > 0, nodeClass)
//	require.NotEmpty(nodeClass.Status.Conditions, "...")
//
// this leaves a debug dump behind exactly when - and only when - that
// assertion is about to fail the test, without adding noise to a passing
// `go test -v` run. Multiple objs are useful for an assertion that
// compares two resources against each other.
func (f *Framework) DescribeOnFailure(ok bool, objs ...client.Object) {
	if ok {
		return
	}

	f.T.Helper()

	for _, obj := range objs {
		f.Logf("current state:\n%s", Describe(obj))
	}
}
