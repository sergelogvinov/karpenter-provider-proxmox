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

// Package framework provides the plumbing (clients, waiters, object builders)
// shared by the karpenter-provider-proxmox end-to-end tests.
//
// The suite never deploys the controller itself: the target cluster
// (selected via KUBECONFIG) is expected to already run it, with the
// ProxmoxTemplate/ProxmoxUnmanagedTemplate/ProxmoxNodeClass CRDs installed.
package framework

import (
	"log"
	"os"
	"strings"
	"time"
)

// Config holds the e2e suite configuration, read from environment variables.
type Config struct {
	// Kubeconfig is the path to the kubeconfig of the cluster under test.
	// Empty means "use client-go's default loading rules" (KUBECONFIG env,
	// then ~/.kube/config).
	Kubeconfig string

	// NamePrefix names created by the suite are named "<prefix>-<random>"
	// and removed on cleanup.
	NamePrefix string

	// Timeout is the default per-wait timeout (resource Ready, resource
	// gone, ...).
	Timeout time.Duration

	// ProxmoxConfig, when set, points at a cloud-config.yaml the suite can
	// use to talk to the Proxmox API directly (the same file format
	// pkg/providers/config.ReadCloudConfigFromFile reads for the
	// controller itself, see hack/proxmox-config.yaml) to verify state
	// independently of what Kubernetes reports - e.g. how many zones a
	// region currently has. Tests that need it skip (t.Skip) when it's
	// unset.
	ProxmoxConfig string

	// Region, when set, pins the ProxmoxTemplate created by the suite to a
	// single Proxmox region instead of leaving spec.region empty (which
	// makes the controller target every configured region). Left unset,
	// tests that need a single, predictable region fall back to the first
	// region reported by the pool built from ProxmoxConfig.
	Region string

	// TemplateSourceImageURL/TemplateImageName/TemplateStorageIDs/
	// TemplateBridge parameterize the ProxmoxTemplate the template
	// scenario creates - see docs/nodetemplateclass.md.
	TemplateSourceImageURL string
	TemplateImageName      string
	TemplateStorageIDs     []string
	TemplateBridge         string
}

// LoadConfig builds a Config from environment variables.
func LoadConfig() Config {
	cfg := Config{
		Kubeconfig:             os.Getenv("KUBECONFIG"),
		NamePrefix:             getEnvDefault("E2E_NAME_PREFIX", "e2e"),
		ProxmoxConfig:          os.Getenv("E2E_PROXMOX_CONFIG"),
		Region:                 os.Getenv("E2E_REGION"),
		Timeout:                getEnvDurationDefault("E2E_TIMEOUT", 5*time.Minute),
		TemplateSourceImageURL: getEnvDefault("E2E_TEMPLATE_IMAGE_URL", "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"),
		TemplateImageName:      getEnvDefault("E2E_TEMPLATE_IMAGE_NAME", "e2e-ubuntu-amd64.qcow2"),
		TemplateBridge:         getEnvDefault("E2E_TEMPLATE_BRIDGE", "vmbr0"),
	}

	for id := range strings.SplitSeq(getEnvDefault("E2E_TEMPLATE_STORAGE_IDS", "local"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			cfg.TemplateStorageIDs = append(cfg.TemplateStorageIDs, id)
		}
	}

	return cfg
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func getEnvDurationDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}

		log.Printf("e2e: invalid %s=%q (%v), using default %s", key, v, err, def)
	}

	return def
}
