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
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

// ProxmoxTemplateOptions parameterizes NewProxmoxTemplate.
type ProxmoxTemplateOptions struct {
	Name string

	// Region, when set, pins the template to a single Proxmox region -
	// see docs/nodetemplateclass.md. Left empty, the controller targets
	// every region configured in its cloud-config.
	Region string

	// SourceImageURL is spec.sourceImage.url - the http(s) location of the
	// qcow2/raw image Proxmox downloads into its import storage.
	SourceImageURL string
	// ImageName is spec.sourceImage.imageName.
	ImageName string

	// StorageIDs is spec.storageIDs: the Proxmox storage(s) that must
	// support the import and images content types, either separately or
	// combined.
	StorageIDs []string

	// Bridge is the network bridge for the template's single required
	// network interface (spec.network[0].bridge).
	Bridge string

	// Tags to apply to the Proxmox VM template(s) this resource creates -
	// spec.tags. Used by the e2e suite to give a ProxmoxUnmanagedTemplate
	// something unique to match on instead of picking up unrelated
	// templates already present in the target cluster.
	Tags []string
}

// NewProxmoxTemplate builds a minimal, valid ProxmoxTemplate, mirroring
// docs/nodetemplateclass.md's example: a source image, the storage IDs to
// hold it, and a single network interface.
func NewProxmoxTemplate(opts ProxmoxTemplateOptions) *v1alpha1.ProxmoxTemplate {
	return &v1alpha1.ProxmoxTemplate{
		Name: opts.Name,
		Spec: v1alpha1.ProxmoxTemplateSpec{
			Region: opts.Region,
			SourceImage: &v1alpha1.SourceImage{
				URL:       opts.SourceImageURL,
				ImageName: opts.ImageName,
			},
			StorageIDs: opts.StorageIDs,
			Network: []v1alpha1.Network{
				{Name: "net0", Bridge: opts.Bridge},
			},
			Tags: opts.Tags,
		},
	}
}

// ProxmoxUnmanagedTemplateOptions parameterizes NewProxmoxUnmanagedTemplate.
type ProxmoxUnmanagedTemplateOptions struct {
	Name string

	// Region, when set, restricts matching to a single Proxmox region -
	// see docs/nodetemplateclass.md. Left empty, every configured region
	// is searched.
	Region string

	// Tags to match Proxmox VM templates on - spec.tags. All of them must
	// be present on a template for it to match.
	Tags []string
}

// NewProxmoxUnmanagedTemplate builds a ProxmoxUnmanagedTemplate that
// matches templates by tags only (no templateName), mirroring
// docs/nodetemplateclass.md's example.
func NewProxmoxUnmanagedTemplate(opts ProxmoxUnmanagedTemplateOptions) *v1alpha1.ProxmoxUnmanagedTemplate {
	return &v1alpha1.ProxmoxUnmanagedTemplate{
		Name: opts.Name,
		Spec: v1alpha1.ProxmoxUnmanagedTemplateSpec{
			Region: opts.Region,
			Tags:   opts.Tags,
		},
	}
}
