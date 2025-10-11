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

	"k8s.io/klog/v2"
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

			pxClient, err := goproxmox.NewAPIClient(ctx, cfg.URL, options...)
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

// CheckClusters checks if the Proxmox connection is working.
func (c *ProxmoxPool) CheckClusters(ctx context.Context) error {
	for region, pxClient := range c.clients {
		if _, err := pxClient.Version(ctx); err != nil {
			return fmt.Errorf("failed to initialized proxmox client in region %s, error: %v", region, err)
		}

		pxCluster, err := pxClient.Cluster(ctx)
		if err != nil {
			return fmt.Errorf("failed to get cluster info in region %s, error: %v", region, err)
		}

		// Check if we can have permission to list VMs
		vms, err := pxCluster.Resources(ctx, "vm")
		if err != nil {
			return fmt.Errorf("failed to get list of VMs in region %s, error: %v", region, err)
		}

		if len(vms) > 0 {
			klog.V(4).InfoS("Proxmox cluster has VMs", "region", region, "count", len(vms))
		} else {
			klog.InfoS("Proxmox cluster has no VMs, or check the account permission", "region", region)
		}
	}

	return nil
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

	vm, err := px.FindVMByID(ctx, uint64(vmid)) //nolint: unconvert
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

// FindVMByNode find a VM by kubernetes node resource in all Proxmox clusters.
// func (c *ProxmoxPool) FindVMByNode(ctx context.Context, node *v1.Node) (*pxapi.VmRef, string, error) {
// 	for region, px := range c.clients {
// 		vmrs, err := px.GetVmRefsByName(ctx, node.Name)
// 		if err != nil {
// 			if strings.Contains(err.Error(), "not found") {
// 				continue
// 			}

// 			return nil, "", err
// 		}

// 		for _, vmr := range vmrs {
// 			config, err := px.GetVmConfig(ctx, vmr)
// 			if err != nil {
// 				return nil, "", err
// 			}

// 			if c.GetVMUUID(config) == node.Status.NodeInfo.SystemUUID {
// 				return vmr, region, nil
// 			}
// 		}
// 	}

// 	return nil, "", fmt.Errorf("vm '%s' not found", node.Name)
// }

// // FindVMByName find a VM by name in all Proxmox clusters.
// func (c *ProxmoxPool) FindVMByName(ctx context.Context, name string) (*pxapi.VmRef, string, error) {
// 	for region, px := range c.clients {
// 		vmr, err := px.GetVmRefByName(ctx, name)
// 		if err != nil {
// 			if strings.Contains(err.Error(), "not found") {
// 				continue
// 			}

// 			return nil, "", err
// 		}

// 		return vmr, region, nil
// 	}

// 	return nil, "", fmt.Errorf("vm '%s' not found", name)
// }

// // FindVMByUUID find a VM by uuid in all Proxmox clusters.
// func (c *ProxmoxPool) FindVMByUUID(ctx context.Context, uuid string) (*pxapi.VmRef, string, error) {
// 	for region, px := range c.clients {
// 		vms, err := px.GetResourceList(ctx, "vm")
// 		if err != nil {
// 			return nil, "", fmt.Errorf("error get resources %v", err)
// 		}

// 		for vmii := range vms {
// 			vm, ok := vms[vmii].(map[string]interface{})
// 			if !ok {
// 				return nil, "", fmt.Errorf("failed to cast response to map, vm: %v", vm)
// 			}

// 			if vm["type"].(string) != "qemu" { //nolint:errcheck
// 				continue
// 			}

// 			vmr := pxapi.NewVmRef(int(vm["vmid"].(float64))) //nolint:errcheck
// 			vmr.SetNode(vm["node"].(string))                 //nolint:errcheck
// 			vmr.SetVmType("qemu")

// 			config, err := px.GetVmConfig(ctx, vmr)
// 			if err != nil {
// 				return nil, "", err
// 			}

// 			if config["smbios1"] != nil {
// 				if c.getSMBSetting(config, "uuid") == uuid {
// 					return vmr, region, nil
// 				}
// 			}
// 		}
// 	}

// 	return nil, "", fmt.Errorf("vm with uuid '%s' not found", uuid)
// }

// // GetVMName returns the VM name.
// func (c *ProxmoxPool) GetVMName(vmInfo map[string]interface{}) string {
// 	if vmInfo["name"] != nil {
// 		return vmInfo["name"].(string) //nolint:errcheck
// 	}

// 	return ""
// }

// // GetVMUUID returns the VM UUID.
// func (c *ProxmoxPool) GetVMUUID(vmInfo map[string]interface{}) string {
// 	if vmInfo["smbios1"] != nil {
// 		return c.getSMBSetting(vmInfo, "uuid")
// 	}

// 	return ""
// }

// // GetVMSKU returns the VM instance type name.
// func (c *ProxmoxPool) GetVMSKU(vmInfo map[string]interface{}) string {
// 	if vmInfo["smbios1"] != nil {
// 		return c.getSMBSetting(vmInfo, "sku")
// 	}

// 	return ""
// }

// func (c *ProxmoxPool) getSMBSetting(vmInfo map[string]interface{}, name string) string {
// 	smbios, ok := vmInfo["smbios1"].(string)
// 	if !ok {
// 		return ""
// 	}

// 	for _, l := range strings.Split(smbios, ",") {
// 		if l == "" || l == "base64=1" {
// 			continue
// 		}

// 		parsedParameter, err := url.ParseQuery(l)
// 		if err != nil {
// 			return ""
// 		}

// 		for k, v := range parsedParameter {
// 			if k == name {
// 				decodedString, err := base64.StdEncoding.DecodeString(v[0])
// 				if err != nil {
// 					decodedString = []byte(v[0])
// 				}

// 				return string(decodedString)
// 			}
// 		}
// 	}

// 	return ""
// }

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
