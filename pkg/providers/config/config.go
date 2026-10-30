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

// Package config is the configuration for the cloud provider.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
)

// Provider specifies the provider. Can be 'default' or 'capmox'
type Provider string

// ProviderDefault is the default provider
const ProviderDefault Provider = "default"

// ProviderCapmox is the Provider for capmox
const ProviderCapmox Provider = "capmox"

// ClustersConfig is proxmox multi-cluster cloud config.
type ClustersConfig struct {
	Features struct {
		Provider Provider `yaml:"provider,omitempty"`
	} `yaml:"features,omitempty"`
	Clusters []*pxpool.ClusterConfig `yaml:"clusters,omitempty"`
}

// ReadCloudConfig reads cloud config from a reader.
func ReadCloudConfig(config io.Reader) (ClustersConfig, error) {
	cfg := ClustersConfig{}

	if config != nil {
		if err := yaml.NewDecoder(config).Decode(&cfg); err != nil {
			return ClustersConfig{}, err
		}
	}

	for idx, c := range cfg.Clusters {
		if c.Username != "" && c.Password != "" {
			if c.TokenID != "" || c.TokenSecret != "" {
				return ClustersConfig{}, fmt.Errorf("cluster #%d: token_id and token_secret are not allowed when username and password are set", idx+1)
			}
		} else if c.TokenID == "" || c.TokenSecret == "" {
			return ClustersConfig{}, fmt.Errorf("cluster #%d: either username and password or token_id and token_secret are required", idx+1)
		}

		if c.Region == "" {
			return ClustersConfig{}, fmt.Errorf("cluster #%d: region is required", idx+1)
		}

		if c.URL == "" || !strings.HasPrefix(c.URL, "http") {
			return ClustersConfig{}, fmt.Errorf("cluster #%d: url is required", idx+1)
		}
	}

	if cfg.Features.Provider == "" {
		cfg.Features.Provider = ProviderDefault
	}

	return cfg, nil
}

// RootUser is the only Proxmox login that may set the root-only VM options
// (affinity, hugepages, keephugepages, args). An API token of it may not.
const RootUser = "root@pam"

// Privileges tells, per region, whether the controller logs in as root.
type Privileges map[string]bool

// Root reports whether the region's credentials log in as root@pam itself.
// A region without credentials is not root.
func (p Privileges) Root(region string) bool {
	return p[region]
}

// Privileges returns each region's privilege, decided from the login the pool
// client makes with its credentials: username root@pam with a password is root;
// any API token, a root@pam token included, or any other user is not (ADR 0003).
// The pool client logs in with username and password whenever both are set and
// only otherwise with a token, so token_id_file or token_secret_file next to
// root@pam's password still logs in as root.
func (c ClustersConfig) Privileges() Privileges {
	privileges := make(Privileges, len(c.Clusters))

	for _, cl := range c.Clusters {
		privileges[cl.Region] = cl.Username == RootUser && cl.Password != ""
	}

	return privileges
}

// ReadCloudConfigFromFile reads cloud config from a file.
func ReadCloudConfigFromFile(file string) (ClustersConfig, error) {
	f, err := os.Open(filepath.Clean(file))
	if err != nil {
		return ClustersConfig{}, fmt.Errorf("error reading %s: %v", file, err)
	}
	defer f.Close() // nolint: errcheck

	return ReadCloudConfig(f)
}
