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

import "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis"

const (
	// ProxmoxNodeClassHashVersion is the version of the hash for ProxmoxNodeClass
	ProxmoxNodeClassHashVersion = "v1"

	// ProxmoxTemplateClassHashVersion is the version of the hash for ProxmoxNodeTemplateClass
	ProxmoxTemplateClassHashVersion = "v1"

	// AnnotationProxmoxNodeClassHash is the annotation key for the hash of the ProxmoxNodeClass
	AnnotationProxmoxNodeClassHash = apis.Group + "/proxmoxnodeclass-hash"

	// AnnotationProxmoxNodeClassHashVersion is the annotation key for the version of the hash function
	AnnotationProxmoxNodeClassHashVersion = apis.Group + "/proxmoxnodeclass-hash-version"

	// AnnotationProxmoxNodeClassPool is the annotation key for the ProxmoxNodeClass pool
	AnnotationProxmoxNodeClassPool = apis.Group + "/proxmoxnodeclass-pool"

	// AnnotationProxmoxTemplateHash is the annotation key for the hash of the ProxmoxTemplateClasses
	AnnotationProxmoxTemplateHash = apis.Group + "/proxmoxtemplate-hash"

	// AnnotationProxmoxTemplateHashVersion is the annotation key for the version of the hash function
	AnnotationProxmoxTemplateHashVersion = apis.Group + "/proxmoxtemplate-hash-version"

	// AnnotationProxmoxTemplateInPlaceUpdateHash is the annotation key for the hash of the in-place update
	AnnotationProxmoxTemplateInPlaceUpdateHash = apis.Group + "/proxmoxtemplateinplaceupdate-hash"

	// AnnotationProxmoxCloudInitStatus is the annotation key for the status of the ProxmoxCloudInit
	AnnotationProxmoxCloudInitStatus = apis.Group + "/proxmoxcloudinit-status"

	// AnnotationProxmoxCloudInitToken is the annotation key for the kubelet bootstrap token id
	AnnotationProxmoxCloudInitToken = apis.Group + "/proxmoxcloudinit-token"

	// AnnotationProxmoxNodeInPlaceUpdateHash is the annotation key for the hash of the in-place update
	AnnotationProxmoxNodeInPlaceUpdateHash = apis.Group + "/proxmoxnodeinplaceupdate-hash"
)
