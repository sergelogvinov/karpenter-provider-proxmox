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

package goproxmox

type VMCloneRequest struct {
	Node        string `json:"node"`
	NewID       int    `json:"newid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Full        uint8  `json:"full,omitempty"`
	Storage     string `json:"storage,omitempty"`

	CPU          int    `json:"cpu,omitempty"`
	Memory       uint32 `json:"memory,omitempty"`
	DiskSize     string `json:"diskSize,omitempty"`
	Tags         string `json:"tags,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
}
