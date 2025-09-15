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

package ipam

import (
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"

	gocidr "github.com/apparentlymart/go-cidr/cidr"
)

type IPPool struct {
	sync.RWMutex

	// IPNet is the IP network for the pool.
	IPNet *net.IPNet
	// maxIPs is the maximum number of IPs that can be allocated in the pool.
	maxIPs int
	// pool holds the allocated IPs bits in the pool.
	pool big.Int
}

func ParseCIDR(s string) (*IPPool, error) {
	var maxIPs int

	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, errors.New("Invalid CIDR format")
	}

	ones, bits := ipNet.Mask.Size()

	switch bits {
	case 32:
		maxIPs = 1 << (int(32) - ones)
	case 128:
		maxIPs = 1 << (int(128) - ones)
	default:
		return nil, errors.New("Only IPv4 and IPv6 are supported")
	}

	maxIPs -= 1 // Exclude network address for IP allocation

	return &IPPool{IPNet: ipNet, maxIPs: maxIPs}, nil
}

func (p *IPPool) IsEmpty() bool {
	p.RLock()
	defer p.RUnlock()

	return p.pool.BitLen() == 0
}

func (p *IPPool) Size() int {
	p.RLock()
	defer p.RUnlock()

	size := 0

	for i := 0; i <= p.maxIPs; i++ {
		if p.pool.Bit(i) == 1 {
			size++
		}
	}

	return size
}

func (p *IPPool) EqualCIDR(other *net.IPNet) bool {
	return p.IPNet.IP.Equal(other.IP) && (p.IPNet.Mask.String() == other.Mask.String())
}

func (p *IPPool) String() string {
	return fmt.Sprintf("CIDR: %s used-map: %s, total: %d", p.IPNet.String(), p.pool.Text(2), p.maxIPs)
}

func (p *IPPool) Contains(ip net.IP) bool {
	return p.IPNet.Contains(ip)
}

func (p *IPPool) ContainsCIDR(ip *net.IPNet) bool {
	return p.IPNet.Contains(ip.IP)
}

func (p *IPPool) Occupy(ip net.IP) bool {
	p.Lock()
	defer p.Unlock()

	if p.IPNet.IP.Equal(ip) {
		return false
	}

	inx, err := p.HostIndex(ip)
	if err != nil {
		return false
	}

	if p.pool.Bit(inx) == 0 {
		p.pool.SetBit(&p.pool, inx, 1)

		return true
	}

	return false
}

func (p *IPPool) Next(cidr ...*net.IPNet) net.IP {
	p.Lock()
	defer p.Unlock()

	var (
		candidate int
		err       error
	)

	if len(cidr) > 0 && !p.IPNet.IP.Equal(cidr[0].IP) {
		candidate, err = p.HostIndex(cidr[0].IP)
		if err != nil {
			candidate = 0
		}
	}

	for range p.maxIPs {
		if p.pool.Bit(candidate) == 0 {
			break
		}

		candidate = (candidate + 1) % p.maxIPs
	}

	p.pool.SetBit(&p.pool, candidate, 1)

	ip, err := gocidr.Host(p.IPNet, candidate+1)
	if err != nil {
		return nil
	}

	return ip
}

func (p *IPPool) Release(ip net.IP) error {
	p.Lock()
	defer p.Unlock()

	if p.IPNet.IP.Equal(ip) {
		return nil
	}

	inx, err := p.HostIndex(ip)
	if err != nil {
		return err
	}

	if p.pool.Bit(inx) == 1 {
		p.pool.SetBit(&p.pool, inx, 0)
	}

	return nil
}

func (p *IPPool) HostIndex(ip net.IP) (int, error) {
	if !p.IPNet.Contains(ip) {
		return -1, fmt.Errorf("IP %s is not in the CIDR %s", ip.String(), p.IPNet.String())
	}

	c := big.NewInt(0).SetBytes(ip.To16())
	b := big.NewInt(0).SetBytes(p.IPNet.IP.To16())

	index := big.NewInt(0).Sub(c, b)
	if index.Int64() > int64(p.maxIPs) {
		return -1, fmt.Errorf("IP %s index %d is out of range for CIDR %s", ip.String(), index.Int64(), p.IPNet.String())
	}

	return int(index.Int64() - 1), nil
}
