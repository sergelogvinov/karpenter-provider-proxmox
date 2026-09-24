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

// Package lifecycle covers the common node-provisioning lifecycle: against
// an already-configured ProxmoxNodeClass/ProxmoxUnmanagedTemplate (see
// docs/deploy/nodepool.yaml), create a NodePool and confirm a workload
// scheduled onto it comes up, and scales, for real.
package lifecycle

import (
	"os"
	"testing"

	"github.com/sergelogvinov/karpenter-provider-proxmox/test/e2e/framework"
)

func TestMain(m *testing.M) {
	os.Exit(framework.TestMain(m))
}
