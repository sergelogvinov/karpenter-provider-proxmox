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

import "github.com/pkg/errors"

var (
	// ErrVirtualMachineNotFound is returned when a virtual machine is not found.
	ErrVirtualMachineNotFound = errors.New("VM machine not found")
	// ErrVirtualMachineTemplateNotFound is returned when a virtual machine template is not found.
	ErrVirtualMachineTemplateNotFound = errors.New("VM template not found")
)
