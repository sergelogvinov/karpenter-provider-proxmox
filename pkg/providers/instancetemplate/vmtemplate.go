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

package instancetemplate

import (
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

// PlacedVMTemplates returns the template placement of each of templates with
// the hugepages and affinity keys of its VM config, in order.
func PlacedVMTemplates(templates []InstanceTemplateInfo) []v1alpha1.PlacedVMTemplate {
	placed := make([]v1alpha1.PlacedVMTemplate, 0, len(templates))

	for _, info := range templates {
		placed = append(placed, v1alpha1.PlacedVMTemplate{Placement: info.Placement(), VMTemplate: toVMTemplateInfo(info)})
	}

	return placed
}

// toVMTemplateInfo returns the hugepages and affinity keys of the template
// config of info.
func toVMTemplateInfo(info InstanceTemplateInfo) *v1alpha1.VMTemplateInfo {
	return &v1alpha1.VMTemplateInfo{Hugepages: info.Hugepages, Affinity: info.Affinity}
}
