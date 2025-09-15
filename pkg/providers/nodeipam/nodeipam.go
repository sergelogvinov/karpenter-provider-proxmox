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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	ipam "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam/ipam"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type Provider interface {
	UpdateNodeCIDR(ctx context.Context) error
	AllocateOrOccupyCIDR(subnet string) error
	ReleaseCIDR(subnet string) error

	OccupyNodeIPs(node *corev1.Node) error
	OccupyIP(subnet string) (net.IP, error)
	ReleaseNodeIPs(node *corev1.Node) error
	ReleaseIP(ip string) error

	String() string
}

// DefaultProvider is the provider that manages node ipam state.
type DefaultProvider struct {
	kubeClient            kubernetes.Interface
	cloudCapacityProvider cloudcapacity.Provider

	// proxmoxZoneCIDRMask []int
	subnets []*ipam.IPPool
}

func NewDefaultProvider(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	cloudCapacityProvider cloudcapacity.Provider,
) *DefaultProvider {
	return &DefaultProvider{
		kubeClient:            kubeClient,
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
					p.AllocateOrOccupyCIDR(iface.Address4)
				}

				if iface.Gateway4 != "" {
					for i := range p.subnets {
						if p.subnets[i] == nil {
							continue
						}

						ip := net.ParseIP(iface.Gateway4)
						if p.subnets[i].Contains(ip) {
							p.subnets[i].Occupy(ip)
						}
					}
				}
			}
		}
	}

	return nil
}

func (p *DefaultProvider) AllocateOrOccupyCIDR(subnet string) error {
	ip, cidr, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}

	if cidr.IP.To4() == nil {
		return fmt.Errorf("only IPv4 is supported")
	}

	var ipPool *ipam.IPPool

	for i := range p.subnets {
		if p.subnets[i] == nil {
			continue
		}

		if p.subnets[i].ContainsCIDR(cidr) {
			ipPool = p.subnets[i]

			break
		}
	}

	if ipPool == nil {
		ipPool, err = ipam.ParseCIDR(subnet)
		if err != nil {
			return err
		}

		p.subnets = append(p.subnets, ipPool)
	}

	if !ip.Equal(cidr.IP) {
		ipPool.Occupy(ip)
	}

	return nil
}

func (p *DefaultProvider) ReleaseCIDR(subnet string) error {
	_, cidr, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}

	for i := range p.subnets {
		if p.subnets[i] == nil {
			continue
		}

		if p.subnets[i].EqualCIDR(cidr) {
			p.subnets = append(p.subnets[:i], p.subnets[i+1:]...)

			return nil
		}
	}

	return nil
}

func (p *DefaultProvider) OccupyNodeIPs(node *corev1.Node) error {
	if len(p.subnets) == 0 {
		return fmt.Errorf("no subnets available for IPAM")
	}

	return p.updateNodeIPs(node, func(subnet *ipam.IPPool, ip net.IP) error {
		subnet.Occupy(ip)

		return nil
	})
}

func (s *DefaultProvider) OccupyIP(subnet string) (net.IP, error) {
	ip, cidr, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, err
	}

	cidr.IP = ip

	for i := range s.subnets {
		if s.subnets[i] == nil {
			continue
		}

		if s.subnets[i].ContainsCIDR(cidr) {
			ip := s.subnets[i].Next(cidr)
			if ip == nil {
				return nil, fmt.Errorf("no available IPs in subnet %s", cidr.String())
			}

			return ip, nil
		}
	}

	return nil, fmt.Errorf("no subnet found for cidr %s", cidr.String())
}

func (p *DefaultProvider) ReleaseNodeIPs(node *corev1.Node) error {
	if len(p.subnets) == 0 {
		return fmt.Errorf("no subnets available for IPAM")
	}

	return p.updateNodeIPs(node, func(subnet *ipam.IPPool, ip net.IP) error {
		return subnet.Release(ip)
	})
}

func (p *DefaultProvider) ReleaseIP(ip string) error {
	return nil
}

func (p *DefaultProvider) String() string {
	capacity := make([]string, len(p.subnets))
	for i := range p.subnets {
		if p.subnets[i] != nil {
			capacity[i] = p.subnets[i].String()
		}
	}

	return fmt.Sprintf("NodeIpamProvider{subnets: %d, capacity: %v}", len(p.subnets), capacity)
}

func (p *DefaultProvider) updateNodeIPs(node *corev1.Node, f func(subnet *ipam.IPPool, ip net.IP) error) error {
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP && addr.Type != corev1.NodeExternalIP {
			continue
		}

		ip := net.ParseIP(addr.Address)
		if ip == nil || ip.To4() == nil {
			continue
		}

		for i := range p.subnets {
			if p.subnets[i] == nil {
				continue
			}

			if p.subnets[i].Contains(ip) {
				if err := f(p.subnets[i], ip); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
