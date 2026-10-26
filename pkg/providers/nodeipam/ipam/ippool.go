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
	"math/bits"
	"net"
	"sync"

	gocidr "github.com/apparentlymart/go-cidr/cidr"
)

// ErrPoolExhausted is returned when the pool has no free addresses left.
var ErrPoolExhausted = errors.New("no available IPs in the pool")

type IPPool struct {
	sync.RWMutex

	// IPNet is the IP network for the pool.
	IPNet *net.IPNet
	// size is the number of addresses in the network, including reserved ones.
	size int
	// reserved holds the bits of addresses that are never allocated.
	reserved big.Int
	// occupied holds the bits of addresses currently in use.
	occupied big.Int
}

// NewIPPool creates an IP pool for the CIDR with its reserved addresses already set.
func NewIPPool(s string) (*IPPool, error) {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, errors.New("invalid CIDR format")
	}

	ones, bits := ipNet.Mask.Size()

	switch {
	case bits == 32 && ones > 30:
		return nil, fmt.Errorf("CIDR %s has no allocatable addresses", ipNet.String())
	case bits == 128 && ones > 127:
		return nil, fmt.Errorf("CIDR %s has no allocatable addresses", ipNet.String())
	case bits == 128 && ones < 96:
		return nil, fmt.Errorf("CIDR %s is too large, the minimum IPv6 prefix is /96", ipNet.String())
	}

	p := &IPPool{IPNet: ipNet, size: 1 << (bits - ones)}

	// Network address
	p.reserved.SetBit(&p.reserved, 0, 1)

	if bits == 32 {
		// First host address, reserved for technical needs
		p.reserved.SetBit(&p.reserved, 1, 1)
		// Broadcast address
		p.reserved.SetBit(&p.reserved, p.size-1, 1)
	}

	return p, nil
}

// Size returns the number of occupied addresses.
func (p *IPPool) Size() int {
	p.RLock()
	defer p.RUnlock()

	return countBits(&p.occupied)
}

func (p *IPPool) String() string {
	p.RLock()
	defer p.RUnlock()

	occupied := countBits(&p.occupied)
	reserved := countBits(&p.reserved)

	return fmt.Sprintf("CIDR: %s occupied: %d, reserved: %d, free: %d", p.IPNet.String(), occupied, reserved, p.size-occupied-reserved)
}

func (p *IPPool) EqualCIDR(other *net.IPNet) bool {
	return p.IPNet.IP.Equal(other.IP) && (p.IPNet.Mask.String() == other.Mask.String())
}

func (p *IPPool) Contains(ip net.IP) bool {
	return p.IPNet.Contains(ip)
}

func (p *IPPool) ContainsCIDR(ip *net.IPNet) bool {
	return p.IPNet.Contains(ip.IP)
}

// Occupy marks the IP as in use. It returns false if the IP is reserved, already occupied or outside the pool.
func (p *IPPool) Occupy(ip net.IP) bool {
	p.Lock()
	defer p.Unlock()

	inx, err := p.hostIndex(ip)
	if err != nil {
		return false
	}

	if p.reserved.Bit(inx) == 1 || p.occupied.Bit(inx) == 1 {
		return false
	}

	p.occupied.SetBit(&p.occupied, inx, 1)

	return true
}

// Reserve excludes the IP from allocation, even if it is currently occupied.
// It returns false if the IP is outside the pool.
func (p *IPPool) Reserve(ip net.IP) bool {
	p.Lock()
	defer p.Unlock()

	inx, err := p.hostIndex(ip)
	if err != nil {
		return false
	}

	p.occupied.SetBit(&p.occupied, inx, 0)
	p.reserved.SetBit(&p.reserved, inx, 1)

	return true
}

// Allocate occupies the next free address after the hint, or from the start of the pool if hint is nil.
func (p *IPPool) Allocate(hint net.IP) (net.IP, error) {
	p.Lock()
	defer p.Unlock()

	candidate := 0

	if hint != nil {
		if inx, err := p.hostIndex(hint); err == nil {
			candidate = (inx + 1) % p.size
		}
	}

	for range p.size {
		if p.reserved.Bit(candidate) == 0 && p.occupied.Bit(candidate) == 0 {
			p.occupied.SetBit(&p.occupied, candidate, 1)

			return gocidr.Host(p.IPNet, candidate)
		}

		candidate = (candidate + 1) % p.size
	}

	return nil, ErrPoolExhausted
}

// Release returns the IP to the pool. Releasing a reserved IP is a no-op.
func (p *IPPool) Release(ip net.IP) error {
	p.Lock()
	defer p.Unlock()

	inx, err := p.hostIndex(ip)
	if err != nil {
		return err
	}

	p.occupied.SetBit(&p.occupied, inx, 0)

	return nil
}

// hostIndex returns the offset of the IP from the network address.
func (p *IPPool) hostIndex(ip net.IP) (int, error) {
	if !p.IPNet.Contains(ip) {
		return -1, fmt.Errorf("IP %s is not in the CIDR %s", ip.String(), p.IPNet.String())
	}

	c := big.NewInt(0).SetBytes(ip.To16())
	b := big.NewInt(0).SetBytes(p.IPNet.IP.To16())

	return int(big.NewInt(0).Sub(c, b).Int64()), nil
}

func countBits(b *big.Int) int {
	n := 0
	for _, w := range b.Bits() {
		n += bits.OnesCount(uint(w))
	}

	return n
}
