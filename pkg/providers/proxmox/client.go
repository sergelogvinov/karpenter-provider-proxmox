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

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/luthermonson/go-proxmox"
	"github.com/patrickmn/go-cache"

	"k8s.io/utils/ptr"
)

// APIClient Proxmox API client object.
type APIClient struct {
	*proxmox.Client

	lastVmID *cache.Cache
}

// NewAPIClient initializes a GO-Proxmox API client.
func NewAPIClient(ctx context.Context, url string, options ...proxmox.Option) (*APIClient, error) {
	client := proxmox.NewClient(url, options...)

	// _, err := client.Version(ctx)
	// if err != nil {
	// 	return nil, fmt.Errorf("unable to initialize proxmox api client: %w", err)
	// }

	return &APIClient{
		Client:   client,
		lastVmID: cache.New(5*time.Minute, 10*time.Minute),
	}, nil
}

// FindVMByID tries to find a VM by its ID on the whole cluster.
func (c *APIClient) FindVMByID(ctx context.Context, vmID uint64) (*proxmox.ClusterResource, error) {
	cluster, err := c.Cluster(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot get cluster status: %w", err)
	}

	vmResources, err := cluster.Resources(ctx, "vm")
	if err != nil {
		return nil, fmt.Errorf("could not list vm resources: %w", err)
	}

	for _, vm := range vmResources {
		if vm.VMID == vmID {
			return vm, nil
		}
	}

	return nil, ErrVirtualMachineNotFound
}

// FindVMTemplateByName tries to find a VMID by its name
func (c *APIClient) FindVMTemplateByName(ctx context.Context, zone, name string) (vmID int, err error) {
	cluster, err := c.Cluster(ctx)
	if err != nil {
		return 0, fmt.Errorf("cannot get cluster status: %w", err)
	}

	vmResources, err := cluster.Resources(ctx, "vm")
	if err != nil {
		return 0, fmt.Errorf("could not list vm resources: %w", err)
	}

	for _, vm := range vmResources {
		if vm.Template == 0 {
			continue
		}

		if vm.Name == name {
			vmID = int(vm.VMID)
		}

		if vm.Node == zone && vm.Name == name {
			return int(vm.VMID), nil
		}
	}

	if vmID == 0 {
		return 0, ErrVirtualMachineTemplateNotFound
	}

	return vmID, nil
}

func (c *APIClient) GetNextID(ctx context.Context, vmid int) (int, error) {
	var ret string

	if _, found := c.lastVmID.Get(strconv.Itoa(vmid)); found {
		return c.GetNextID(ctx, vmid+1)
	}

	data := make(map[string]interface{})
	data["vmid"] = vmid

	if err := c.Client.GetWithParams(ctx, "/cluster/nextid", data, &ret); err != nil {
		if strings.HasPrefix(err.Error(), "bad request: 400 ") {
			return c.GetNextID(ctx, vmid+1)
		}

		return 0, err
	}

	c.lastVmID.SetDefault(strconv.Itoa(vmid), struct{}{})

	return strconv.Atoi(ret)
}

func (c *APIClient) StartVMByID(ctx context.Context, nodeName string, vmID int) (*proxmox.VirtualMachine, error) {
	node, err := c.Node(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("unable to find node with name %s: %w", nodeName, err)
	}

	vm, err := node.VirtualMachine(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("unable to find vm with id %d: %w", vmID, err)
	}

	if _, err := vm.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start vm %d: %v", vmID, err)
	}

	return vm, nil
}

func (c *APIClient) DeleteVMByID(ctx context.Context, nodeName string, vmID int) error {
	node, err := c.Node(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", nodeName, err)
	}

	vm, err := node.VirtualMachine(ctx, vmID)
	if err != nil {
		return fmt.Errorf("unable to find vm with id %d: %w", vmID, err)
	}

	if vm.IsRunning() {
		if _, err := vm.Stop(ctx); err != nil {
			return fmt.Errorf("failed to stop vm %d: %v", vmID, err)
		}
	}

	if _, err := vm.Delete(ctx); err != nil {
		return fmt.Errorf("cannot delete vm with id %d: %w", vmID, err)
	}

	c.lastVmID.SetDefault(strconv.Itoa(vmID), struct{}{})

	return nil
}

func (c *APIClient) CreateVM(ctx context.Context, node string, vm map[string]interface{}) error {
	var upid proxmox.UPID

	if err := c.Post(ctx, fmt.Sprintf("/nodes/%s/qemu", node), &vm, &upid); nil != err {
		return fmt.Errorf("unable to create virtual machine: %w", err)
	}

	task := proxmox.NewTask(upid, c.Client)
	if err := task.WaitFor(ctx, 5*60); err != nil {
		return fmt.Errorf("unable to create virtual machine: %w", err)
	}

	if task.IsFailed {
		return fmt.Errorf("unable to create virtual machine: %s", task.ExitStatus)
	}

	return nil
}

func (c *APIClient) CloneVM(ctx context.Context, templateID int, options VMCloneRequest) (newid int, err error) {
	node, err := c.Node(ctx, options.Node)
	if err != nil {
		return 0, fmt.Errorf("unable to find node with name %s: %w", options.Node, err)
	}

	vmTemplate, err := node.VirtualMachine(ctx, templateID)
	if err != nil {
		return 0, fmt.Errorf("unable to find vm with id %d: %w", templateID, err)
	}

	vmCloneOptions := proxmox.VirtualMachineCloneOptions{
		NewID:       options.NewID,
		Description: options.Description,
		Full:        options.Full,
		Name:        options.Name,
		Storage:     options.Storage,
	}

	newid, _, err = vmTemplate.Clone(ctx, &vmCloneOptions)
	if err != nil {
		return 0, fmt.Errorf("failed to clone vm template %d: %v", templateID, err)
	}

	vm, err := node.VirtualMachine(ctx, newid)
	if err != nil {
		return 0, fmt.Errorf("failed to get vm %d: %v", newid, err)
	}

	if _, err = vm.ResizeDisk(ctx, "scsi0", options.DiskSize); err != nil {
		return 0, fmt.Errorf("failed to resize disk for vm %d: %v", newid, err)
	}

	var vmOptions []proxmox.VirtualMachineOption
	if options.CPU != 0 {
		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "cores", Value: fmt.Sprintf("%d", options.CPU)})
	}
	if options.Memory != 0 {
		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "memory", Value: fmt.Sprintf("%d", options.Memory)})
	}

	if options.Tags != "" {
		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "tags", Value: options.Tags})
	}

	if vm.VirtualMachineConfig != nil {
		smbios1 := VMSMBIOS{}
		smbios1.UnmarshalString(vm.VirtualMachineConfig.SMBios1)

		smbios1.SKU = base64.StdEncoding.EncodeToString([]byte(options.InstanceType))
		smbios1.Serial = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("h=%s;i=%d", options.Name, newid)))
		smbios1.Base64 = NewIntOrBool(true)

		v, err := smbios1.ToString()
		if err != nil {
			return 0, fmt.Errorf("failed to marshal smbios1: %w", err)
		}

		vmOptions = append(vmOptions, proxmox.VirtualMachineOption{Name: "smbios1", Value: v})
	}

	vmOptions = applyInstanceOptimization(vm, options, vmOptions)

	if len(vmOptions) > 0 {
		_, err := vm.Config(ctx, vmOptions...)
		if err != nil {
			return 0, fmt.Errorf("unable to configure vm: %w", err)
		}
	}

	return newid, err
}

func (c *APIClient) CreateVMFirewallRules(ctx context.Context, vmID int, nodeName string, rules []*proxmox.FirewallRule) error {
	node, err := c.Node(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", nodeName, err)
	}

	vm, err := node.VirtualMachine(ctx, vmID)
	if err != nil {
		return fmt.Errorf("unable to find vm with id %d: %w", vmID, err)
	}

	if len(rules) > 0 {
		vmOptions, err := vm.FirewallOptionGet(ctx)
		if err != nil {
			return fmt.Errorf("failed to get firewall options for vm %d: %v", vmID, err)
		}

		if vmOptions == nil {
			vmOptions = &proxmox.FirewallVirtualMachineOption{
				Enable:    false,
				Dhcp:      true,
				Ipfilter:  false,
				PolicyIn:  "DROP",
				PolicyOut: "ACCEPT",
			}
		}

		vmOptions.Enable = true
		vmOptions.PolicyIn = "DROP"
		if err := vm.FirewallOptionSet(ctx, vmOptions); err != nil {
			return fmt.Errorf("failed to set firewall options for vm %d: %v", vmID, err)
		}

		for _, rule := range rules {
			if err := vm.FirewallRulesCreate(ctx, rule); err != nil {
				return fmt.Errorf("failed to set firewall rule for vm %d: %v", vmID, err)
			}
		}
	}

	return nil
}

func (c *APIClient) UpdateVMFirewallRules(ctx context.Context, vmID int, nodeName string, rules []*proxmox.FirewallRule) error {
	node, err := c.Node(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("unable to find node with name %s: %w", nodeName, err)
	}

	vm, err := node.VirtualMachine(ctx, vmID)
	if err != nil {
		return fmt.Errorf("unable to find vm with id %d: %w", vmID, err)
	}

	oldRules, err := vm.FirewallGetRules(ctx)
	if err != nil {
		return fmt.Errorf("failed to get firewall rules for vm %d: %v", vmID, err)
	}

	n := len(oldRules)
	if n < len(rules) {
		n = len(rules)
	}

	for i := range n {
		switch {
		case i < len(oldRules) && i < len(rules) && !reflect.DeepEqual(oldRules[i], rules[i]):
			if err := vm.FirewallRulesUpdate(ctx, rules[i]); err != nil {
				return fmt.Errorf("failed to update firewall rule for vm %d: %v", vmID, err)
			}
		case i < len(oldRules) && i >= len(rules):
			if err := vm.FirewallRulesDelete(ctx, i); err != nil {
				return fmt.Errorf("failed to delete old firewall rule for vm %d: %v", vmID, err)
			}
		case i >= len(oldRules) && i < len(rules):
			if err := vm.FirewallRulesCreate(ctx, rules[i]); err != nil {
				return fmt.Errorf("failed to create new firewall rule for vm %d: %v", vmID, err)
			}
		}
	}

	return nil
}

func applyInstanceOptimization(vm *proxmox.VirtualMachine, options VMCloneRequest, vmOptions []proxmox.VirtualMachineOption) []proxmox.VirtualMachineOption {
	if vm.VirtualMachineConfig != nil {
		nets := vm.VirtualMachineConfig.MergeNets()

		for d, net := range nets {
			iface := VMNetworkDevice{}
			if err := iface.UnmarshalString(net); err != nil {
				return nil
			}

			iface.Queues = ptr.To(options.CPU)

			v, err := iface.ToString()
			if err != nil {
				return nil
			}

			vmOptions = append(vmOptions, proxmox.VirtualMachineOption{
				Name:  d,
				Value: v,
			})
		}
	}

	return vmOptions
}
