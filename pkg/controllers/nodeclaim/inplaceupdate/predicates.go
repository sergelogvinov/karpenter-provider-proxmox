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

package inplaceupdate

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type inPlaceChangedPredicate struct {
	predicate.Funcs
}

var _ predicate.Predicate = inPlaceChangedPredicate{}

func (p inPlaceChangedPredicate) Delete(e event.DeleteEvent) bool {
	// We never want updates on delete
	return false
}

func (p inPlaceChangedPredicate) Update(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return true
	}

	typedOld, ok := e.ObjectOld.(*v1alpha1.ProxmoxNodeClass)
	if !ok {
		return true
	}

	typedNew, ok := e.ObjectNew.(*v1alpha1.ProxmoxNodeClass)
	if !ok {
		return true
	}

	return typedOld.InPlaceHash() != typedNew.InPlaceHash()
}
