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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type templateChangedPredicate struct {
	predicate.Funcs
}

var _ predicate.Predicate = templateChangedPredicate{}

func (p templateChangedPredicate) Create(e event.CreateEvent) bool {
	return true
}

func (p templateChangedPredicate) Delete(e event.DeleteEvent) bool {
	return true
}

func (p templateChangedPredicate) Update(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return false
	}

	typedOld, ok := e.ObjectOld.(*v1alpha1.ProxmoxTemplate)
	if !ok {
		return false
	}

	typedNew, ok := e.ObjectNew.(*v1alpha1.ProxmoxTemplate)
	if !ok {
		return false
	}

	return typedOld.Hash() != typedNew.Hash()
}
