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

package v1alpha1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadTemplate reads the template the node class's instanceTemplateRef refers to.
// A template that does not exist fails with an error errors.IsNotFound recognizes.
func (in *ProxmoxNodeClass) ReadTemplate(ctx context.Context, reader client.Reader) (ProxmoxCommonTemplate, error) {
	ref := in.Spec.InstanceTemplateRef
	if ref == nil {
		return nil, fmt.Errorf("node class has no instanceTemplateRef")
	}

	var obj interface {
		client.Object
		ProxmoxCommonTemplate
	}

	switch ref.Kind {
	case "ProxmoxUnmanagedTemplate":
		obj = &ProxmoxUnmanagedTemplate{}
	case "ProxmoxTemplate":
		obj = &ProxmoxTemplate{}
	default:
		return nil, fmt.Errorf("unsupported InstanceTemplateRef kind: %s", ref.Kind)
	}

	if err := reader.Get(ctx, types.NamespacedName{Name: ref.Name}, obj); err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", ref.Kind, ref.Name, err)
	}

	return obj, nil
}
