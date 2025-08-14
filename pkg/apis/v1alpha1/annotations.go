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
	// Version of the hash for ProxmoxNodeClass
	ProxmoxNodeClassHashVersion = "v1"

	// AnnotationProxmoxNodeClassHash is the annotation key for the hash of the ProxmoxNodeClass
	AnnotationProxmoxNodeClassHash = apis.Group + "/proxmoxnodeclass-hash"

	// AnnotationProxmoxNodeClassHashVersion is the annotation key for the version of the hash function
	AnnotationProxmoxNodeClassHashVersion = apis.Group + "/proxmoxnodeclass-hash-version"
)
