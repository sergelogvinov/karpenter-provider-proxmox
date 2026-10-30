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

package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	ccmConfig "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/config"
)

func TestReadCloudConfig(t *testing.T) {
	cfg, err := ccmConfig.ReadCloudConfig(nil)
	assert.Nil(t, err)
	assert.NotNil(t, cfg)

	// Empty config
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
`))
	assert.Nil(t, err)
	assert.NotNil(t, cfg)

	// Wrong config
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
  test: false
`))

	assert.NotNil(t, err)
	assert.NotNil(t, cfg)

	// Non full config
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
- url: abcd
  region: cluster-1
`))

	assert.NotNil(t, err)
	assert.NotNil(t, cfg)

	// Valid config with one cluster
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
  - url: https://example.com
    insecure: false
    token_id: "user!token-id"
    token_secret: "secret"
    region: cluster-1
`))
	assert.Nil(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 1, len(cfg.Clusters))

	// Valid config with one cluster (username/password), implicit default provider
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
  - url: https://example.com
    insecure: false
    username: "user@pam"
    password: "secret"
    region: cluster-1
`))
	assert.Nil(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 1, len(cfg.Clusters))
	assert.Equal(t, ccmConfig.ProviderDefault, cfg.Features.Provider)

	// Valid config with one cluster (username/password), explicit provider default
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
features:
  provider: 'default'
clusters:
  - url: https://example.com
    insecure: false
    username: "user@pam"
    password: "secret"
    region: cluster-1
`))
	assert.Nil(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 1, len(cfg.Clusters))
	assert.Equal(t, ccmConfig.ProviderDefault, cfg.Features.Provider)

	// Valid config with one cluster (username/password), explicit provider capmox
	cfg, err = ccmConfig.ReadCloudConfig(strings.NewReader(`
features:
  provider: 'capmox'
clusters:
  - url: https://example.com
    insecure: false
    username: "user@pam"
    password: "secret"
    region: cluster-1
`))
	assert.Nil(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 1, len(cfg.Clusters))
	assert.Equal(t, ccmConfig.ProviderCapmox, cfg.Features.Provider)
}

func TestReadCloudConfigFromFile(t *testing.T) {
	cfg, err := ccmConfig.ReadCloudConfigFromFile("testdata/cloud-config.yaml")
	assert.NotNil(t, err)
	assert.EqualError(t, err, "error reading testdata/cloud-config.yaml: open testdata/cloud-config.yaml: no such file or directory")
	assert.NotNil(t, cfg)

	cfg, err = ccmConfig.ReadCloudConfigFromFile("../../../hack/proxmox-config.yaml")
	assert.Nil(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 2, len(cfg.Clusters))
}

// TestPrivileges checks that a region is root only when its credentials log in as root@pam with a password:
// an API token, even one of root@pam, and any other user are not root (ADR 0003). The pool client prefers
// username and password over a token when both are set, so token files alongside root@pam's password are root.
func TestPrivileges(t *testing.T) {
	cfg, err := ccmConfig.ReadCloudConfig(strings.NewReader(`
clusters:
  - url: https://root.example.com
    username: "root@pam"
    password: "secret"
    region: root-password
  - url: https://token.example.com
    token_id: "root@pam!karpenter"
    token_secret: "secret"
    region: root-token
  - url: https://user.example.com
    username: "karpenter@pve"
    password: "secret"
    region: other-user
  - url: https://pve.example.com
    username: "root@pve"
    password: "secret"
    region: root-other-realm
  - url: https://mixed-id.example.com
    username: "root@pam"
    password: "secret"
    token_id_file: "/run/secrets/token_id"
    region: root-password-token-id-file
  - url: https://mixed-secret.example.com
    username: "root@pam"
    password: "secret"
    token_secret_file: "/run/secrets/token_secret"
    region: root-password-token-secret-file
  - url: https://mixed-both.example.com
    username: "root@pam"
    password: "secret"
    token_id_file: "/run/secrets/token_id"
    token_secret_file: "/run/secrets/token_secret"
    region: root-password-token-files
  - url: https://user-files.example.com
    username: "karpenter@pve"
    password: "secret"
    token_id_file: "/run/secrets/token_id"
    token_secret_file: "/run/secrets/token_secret"
    region: other-user-token-files
`))
	assert.NoError(t, err)

	privileges := cfg.Privileges()

	assert.True(t, privileges.Root("root-password"))
	assert.False(t, privileges.Root("root-token"), "a root@pam API token may not set root-only options")
	assert.False(t, privileges.Root("other-user"))
	assert.False(t, privileges.Root("root-other-realm"), "only root@pam is root")
	assert.True(t, privileges.Root("root-password-token-id-file"), "the pool logs in with the password, not the token")
	assert.True(t, privileges.Root("root-password-token-secret-file"), "the pool logs in with the password, not the token")
	assert.True(t, privileges.Root("root-password-token-files"), "the pool logs in with the password, not the token")
	assert.False(t, privileges.Root("other-user-token-files"), "the pool logs in as karpenter@pve")
	assert.False(t, privileges.Root("unknown"), "a region without credentials is not root")
}
