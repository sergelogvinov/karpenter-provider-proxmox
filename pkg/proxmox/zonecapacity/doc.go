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

// Package zonecapacity works out what one zone can hold and where its VMs' CPUs go.
// VMs come in as raw config fields; the package classifies them, parses their
// description tokens and keeps one policy-free record per VM. The node policy is
// applied only when a caller reserves, fits or asks what is available.
package zonecapacity
