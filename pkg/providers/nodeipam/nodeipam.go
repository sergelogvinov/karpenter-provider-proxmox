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

package nodeipam

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	ipam "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam/ipam"

	corev1 "k8s.io/api/core/v1"
)

type Provider interface {
	// UpdateNodeCIDR registers the subnets of the Proxmox nodes, occupies the node addresses and reserves their gateways.
	UpdateNodeCIDR(ctx context.Context) error
	// RegisterSubnet creates the IP pool for the CIDR unless a pool already contains its network address,
	// and occupies the address if the CIDR names a host in that pool.
	RegisterSubnet(cidr string) error
	// AllocateIP allocates the next free address in the subnet, starting after the address of the CIDR.
	AllocateIP(cidr string) (net.IP, error)

	OccupyNodeIPs(node *corev1.Node) error
	ReleaseIP(cidr string) error

	String() string
}

// DefaultProvider is the provider that manages node ipam state.
type DefaultProvider struct {
	cloudCapacityProvider cloudcapacity.Provider

	mu      sync.RWMutex
	subnets []*ipam.IPPool
}

func NewDefaultProvider(
	ctx context.Context,
	cloudCapacityProvider cloudcapacity.Provider,
) *DefaultProvider {
	return &DefaultProvider{
		cloudCapacityProvider: cloudCapacityProvider,
	}
}

func (p *DefaultProvider) UpdateNodeCIDR(ctx context.Context) error {
	for _, region := range p.cloudCapacityProvider.Regions() {
		for _, zones := range p.cloudCapacityProvider.Zones(region) {
			n := p.cloudCapacityProvider.GetNetwork(region, zones)
			if n == nil {
				continue
			}

			for _, iface := range n.Ifaces {
				if iface.Address4 != "" {
					p.RegisterSubnet(iface.Address4)
				}

				if iface.Gateway4 != "" {
					ip := net.ParseIP(iface.Gateway4)
					if pool := p.poolFor(ip); pool != nil {
						pool.Reserve(ip)
					}
				}
			}
		}
	}

	return nil
}

func (p *DefaultProvider) RegisterSubnet(cidr string) error {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	if ipNet.IP.To4() == nil {
		return fmt.Errorf("only IPv4 is supported")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pool := p.lookup(ipNet.IP)
	if pool == nil {
		pool, err = ipam.NewIPPool(cidr)
		if err != nil {
			return err
		}

		p.subnets = append(p.subnets, pool)
	}

	pool.Occupy(ip)

	return nil
}

func (p *DefaultProvider) AllocateIP(cidr string) (net.IP, error) {
	hint, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	pool := p.poolFor(hint)
	if pool == nil {
		return nil, fmt.Errorf("no subnet found for cidr %s", cidr)
	}

	ip, err := pool.Allocate(hint)
	if err != nil {
		return nil, fmt.Errorf("subnet %s: %w", pool.IPNet.String(), err)
	}

	return ip, nil
}

func (p *DefaultProvider) OccupyNodeIPs(node *corev1.Node) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.subnets) == 0 {
		return ErrNoSubnetFound
	}

	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP && addr.Type != corev1.NodeExternalIP {
			continue
		}

		ip := net.ParseIP(addr.Address)
		if ip == nil || ip.To4() == nil {
			continue
		}

		if pool := p.lookup(ip); pool != nil {
			pool.Occupy(ip)
		}
	}

	return nil
}

func (p *DefaultProvider) ReleaseIP(cidr string) error {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	pool := p.poolFor(ip)
	if pool == nil {
		return ErrNoSubnetFound
	}

	return pool.Release(ip)
}

func (p *DefaultProvider) String() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	capacity := make([]string, len(p.subnets))
	for i := range p.subnets {
		capacity[i] = p.subnets[i].String()
	}

	return fmt.Sprintf("NodeIpamProvider{subnets: %d, capacity: %v}", len(p.subnets), capacity)
}

// poolFor returns the IP pool that contains the IP, or nil.
func (p *DefaultProvider) poolFor(ip net.IP) *ipam.IPPool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.lookup(ip)
}

// lookup is poolFor for callers that already hold the lock.
func (p *DefaultProvider) lookup(ip net.IP) *ipam.IPPool {
	for _, pool := range p.subnets {
		if pool.Contains(ip) {
			return pool
		}
	}

	return nil
}
