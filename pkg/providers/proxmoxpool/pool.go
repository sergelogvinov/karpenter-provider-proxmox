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

// Package proxmoxpool provides a pool of Proxmox API GO clients
package proxmoxpool

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"

	goproxmox "github.com/sergelogvinov/go-proxmox"
)

// ProxmoxCluster defines a Proxmox cluster configuration.
type ProxmoxCluster struct {
	URL             string `yaml:"url"`
	Insecure        bool   `yaml:"insecure,omitempty"`
	TokenID         string `yaml:"token_id,omitempty"`
	TokenIDFile     string `yaml:"token_id_file,omitempty"`
	TokenSecret     string `yaml:"token_secret,omitempty"`
	TokenSecretFile string `yaml:"token_secret_file,omitempty"`
	Username        string `yaml:"username,omitempty"`
	Password        string `yaml:"password,omitempty"`
	Region          string `yaml:"region,omitempty"`
}

// ProxmoxPool is a Proxmox client pool of proxmox clusters.
type ProxmoxPool struct {
	clients map[string]*goproxmox.APIClient
}

// NewProxmoxPool creates a new Proxmox cluster client.
func NewProxmoxPool(ctx context.Context, config []*ProxmoxCluster) (*ProxmoxPool, error) {
	clusters := len(config)
	if clusters > 0 {
		clients := make(map[string]*goproxmox.APIClient, clusters)

		for _, cfg := range config {
			options := []proxmox.Option{proxmox.WithUserAgent("Karpenter v1.0")}

			if cfg.Insecure {
				httpTr := &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				}

				options = append(options, proxmox.WithHTTPClient(&http.Client{Transport: httpTr}))
			}

			if cfg.TokenID == "" && cfg.TokenIDFile != "" {
				var err error

				cfg.TokenID, err = readValueFromFile(cfg.TokenIDFile)
				if err != nil {
					return nil, err
				}
			}

			if cfg.TokenSecret == "" && cfg.TokenSecretFile != "" {
				var err error

				cfg.TokenSecret, err = readValueFromFile(cfg.TokenSecretFile)
				if err != nil {
					return nil, err
				}
			}

			if cfg.Username != "" && cfg.Password != "" {
				options = append(options, proxmox.WithCredentials(&proxmox.Credentials{
					Username: cfg.Username,
					Password: cfg.Password,
				}))
			} else if cfg.TokenID != "" && cfg.TokenSecret != "" {
				options = append(options, proxmox.WithAPIToken(cfg.TokenID, cfg.TokenSecret))
			}

			pxClient, err := goproxmox.NewAPIClient(cfg.URL, options...)
			if err != nil {
				return nil, err
			}

			clients[cfg.Region] = pxClient
		}

		return &ProxmoxPool{
			clients: clients,
		}, nil
	}

	return nil, ErrClustersNotFound
}

// GetRegions returns supported regions.
func (c *ProxmoxPool) GetRegions() []string {
	regions := make([]string, 0, len(c.clients))

	for region := range c.clients {
		regions = append(regions, region)
	}

	return regions
}

// GetProxmoxCluster returns a Proxmox cluster client in a given region.
func (c *ProxmoxPool) GetProxmoxCluster(region string) (*goproxmox.APIClient, error) {
	if c.clients[region] != nil {
		return c.clients[region], nil
	}

	return nil, ErrRegionNotFound
}

func (c *ProxmoxPool) GetVMByIDInRegion(ctx context.Context, region string, vmid uint64) (*proxmox.ClusterResource, error) {
	px, err := c.GetProxmoxCluster(region)
	if err != nil {
		return nil, err
	}

	vm, err := px.GetVMByID(ctx, uint64(vmid)) //nolint: unconvert
	if err != nil {
		return nil, err
	}

	return vm, nil
}

func (c *ProxmoxPool) DeleteVMByIDInRegion(ctx context.Context, region string, vm *proxmox.ClusterResource) error {
	px, err := c.GetProxmoxCluster(region)
	if err != nil {
		return err
	}

	return px.DeleteVMByID(ctx, vm.Node, int(vm.VMID))
}

func readValueFromFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file '%s': %w", path, err)
	}

	return strings.TrimSpace(string(content)), nil
}
