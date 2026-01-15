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

package ipam

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type ipChangedPredicate struct {
	predicate.Funcs
}

var _ predicate.Predicate = ipChangedPredicate{}

func (p ipChangedPredicate) Create(e event.CreateEvent) bool {
	return true
}

func (p ipChangedPredicate) Delete(e event.DeleteEvent) bool {
	return false
}

func (p ipChangedPredicate) Update(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return false
	}

	typedOld, ok := e.ObjectOld.(*corev1.Node)
	if !ok {
		return false
	}

	typedNew, ok := e.ObjectNew.(*corev1.Node)
	if !ok {
		return false
	}

	return !equality.Semantic.DeepEqual(typedOld.Status.Addresses, typedNew.Status.Addresses) ||
		typedOld.ObjectMeta.DeletionTimestamp != typedNew.ObjectMeta.DeletionTimestamp
}
