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

package ipam_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam/ipam"
)

func TestContains(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		ip     string
		expect bool
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			ip:     "192.168.1.2",
			expect: true,
		},
		{
			name:   "IPv4-outside",
			cidr:   "192.168.1.0/24",
			ip:     "192.168.2.1",
			expect: false,
		},
		{
			name:   "IPv6",
			cidr:   "2a01:4f8:10:20::/96",
			ip:     "2a01:4f8:10:20::1",
			expect: true,
		},
		{
			name:   "IPv6-outside",
			cidr:   "2a01:4f8:10:20:30::/96",
			ip:     "2a01:4f8:10:20:30:40::ff01",
			expect: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.NewIPPool(tt.cidr)
			assert.NoError(t, err)

			assert.Equal(t, tt.expect, ipPool.Contains(net.ParseIP(tt.ip)))
		})
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		expect string
	}{
		{
			name:   "IPv4 skips network and first host",
			cidr:   "192.168.1.0/24",
			expect: "192.168.1.2",
		},
		{
			name:   "IPv6 skips only network",
			cidr:   "2a01:4f8:10:20::/96",
			expect: "2a01:4f8:10:20::1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.NewIPPool(tt.cidr)
			assert.NoError(t, err)

			ip, err := ipPool.Allocate(nil)
			assert.NoError(t, err)
			assert.Equal(t, tt.expect, ip.String())
		})
	}
}

func TestAllocateExhaustsBeforeBroadcast(t *testing.T) {
	ipPool, err := ipam.NewIPPool("192.168.1.0/29")
	assert.NoError(t, err)

	allocated := []string{}

	for range 5 {
		ip, err := ipPool.Allocate(nil)
		assert.NoError(t, err)

		allocated = append(allocated, ip.String())
	}

	assert.Equal(t, []string{"192.168.1.2", "192.168.1.3", "192.168.1.4", "192.168.1.5", "192.168.1.6"}, allocated)

	ip, err := ipPool.Allocate(nil)
	assert.ErrorIs(t, err, ipam.ErrPoolExhausted)
	assert.Nil(t, ip)
}

func TestAllocateAfterHint(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		hint   string
		expect []string
	}{
		{
			name:   "IPv4 starts after hint",
			cidr:   "192.168.1.0/24",
			hint:   "192.168.1.22",
			expect: []string{"192.168.1.23", "192.168.1.24"},
		},
		{
			name:   "IPv4 wraps past broadcast",
			cidr:   "192.168.1.0/24",
			hint:   "192.168.1.253",
			expect: []string{"192.168.1.254", "192.168.1.2"},
		},
		{
			name:   "IPv4 hint is the network address",
			cidr:   "192.168.1.0/24",
			hint:   "192.168.1.0",
			expect: []string{"192.168.1.2", "192.168.1.3"},
		},
		{
			name:   "IPv4 hint outside the pool starts from the beginning",
			cidr:   "192.168.1.0/24",
			hint:   "10.0.0.5",
			expect: []string{"192.168.1.2", "192.168.1.3"},
		},
		{
			name:   "IPv6 starts after hint",
			cidr:   "2a01:4f8:10:20::/112",
			hint:   "2a01:4f8:10:20::ff80",
			expect: []string{"2a01:4f8:10:20::ff81", "2a01:4f8:10:20::ff82"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.NewIPPool(tt.cidr)
			assert.NoError(t, err)

			allocated := []string{}

			for range tt.expect {
				ip, err := ipPool.Allocate(net.ParseIP(tt.hint))
				assert.NoError(t, err)

				allocated = append(allocated, ip.String())
			}

			assert.Equal(t, tt.expect, allocated)
		})
	}
}

func TestOccupy(t *testing.T) {
	ipPool, err := ipam.NewIPPool("192.168.1.0/24")
	assert.NoError(t, err)

	assert.True(t, ipPool.Occupy(net.ParseIP("192.168.1.5")))
	assert.False(t, ipPool.Occupy(net.ParseIP("192.168.1.5")), "already occupied")

	assert.False(t, ipPool.Occupy(net.ParseIP("192.168.1.0")), "network address is reserved")
	assert.False(t, ipPool.Occupy(net.ParseIP("192.168.1.1")), "first host is reserved")
	assert.False(t, ipPool.Occupy(net.ParseIP("192.168.1.255")), "broadcast is reserved")
	assert.False(t, ipPool.Occupy(net.ParseIP("192.168.2.5")), "outside the pool")

	ip, err := ipPool.Allocate(net.ParseIP("192.168.1.4"))
	assert.NoError(t, err)
	assert.Equal(t, "192.168.1.6", ip.String())
}

func TestRelease(t *testing.T) {
	ipPool, err := ipam.NewIPPool("192.168.1.0/30")
	assert.NoError(t, err)

	ip, err := ipPool.Allocate(nil)
	assert.NoError(t, err)

	assert.NoError(t, ipPool.Release(ip))
	assert.NoError(t, ipPool.Release(net.ParseIP("192.168.1.1")), "releasing a reserved address is a no-op")
	assert.Error(t, ipPool.Release(net.ParseIP("192.168.2.2")), "outside the pool")

	ip, err = ipPool.Allocate(nil)
	assert.NoError(t, err)
	assert.Equal(t, "192.168.1.2", ip.String(), "released address is allocatable again")

	_, err = ipPool.Allocate(nil)
	assert.ErrorIs(t, err, ipam.ErrPoolExhausted, "reserved address stays out of the pool")
}

func TestReserve(t *testing.T) {
	ipPool, err := ipam.NewIPPool("192.168.1.0/29")
	assert.NoError(t, err)

	gateway := net.ParseIP("192.168.1.6")

	assert.True(t, ipPool.Occupy(gateway))
	assert.True(t, ipPool.Reserve(gateway), "reserve wins over occupied")
	assert.False(t, ipPool.Reserve(net.ParseIP("192.168.2.6")), "outside the pool")

	assert.NoError(t, ipPool.Release(gateway))
	assert.False(t, ipPool.Occupy(gateway), "released gateway stays reserved")

	allocated := []string{}

	for {
		ip, err := ipPool.Allocate(nil)
		if err != nil {
			assert.ErrorIs(t, err, ipam.ErrPoolExhausted)

			break
		}

		allocated = append(allocated, ip.String())
	}

	assert.Equal(t, []string{"192.168.1.2", "192.168.1.3", "192.168.1.4", "192.168.1.5"}, allocated)
}

func TestNewIPPool(t *testing.T) {
	tests := []struct {
		name      string
		cidr      string
		expectErr bool
	}{
		{name: "IPv4 /30 has one allocatable address", cidr: "192.168.1.0/30"},
		{name: "IPv4 /31", cidr: "192.168.1.0/31", expectErr: true},
		{name: "IPv4 /32", cidr: "192.168.1.1/32", expectErr: true},
		{name: "IPv6 /96", cidr: "2a01:4f8:10:20::/96"},
		{name: "IPv6 /127", cidr: "2a01:4f8:10:20::/127"},
		{name: "IPv6 /128", cidr: "2a01:4f8:10:20::1/128", expectErr: true},
		{name: "IPv6 /64 is too large", cidr: "2a01:4f8:10:20::/64", expectErr: true},
		{name: "invalid", cidr: "192.168.1.0", expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ipam.NewIPPool(tt.cidr)
			if tt.expectErr {
				assert.Error(t, err)

				return
			}

			assert.NoError(t, err)
		})
	}
}

func TestSize(t *testing.T) {
	ipPool, err := ipam.NewIPPool("192.168.1.0/29")
	assert.NoError(t, err)

	assert.Equal(t, 0, ipPool.Size())
	assert.Equal(t, "CIDR: 192.168.1.0/29 occupied: 0, reserved: 3, free: 5", ipPool.String())

	ipPool.Occupy(net.ParseIP("192.168.1.2"))
	ipPool.Occupy(net.ParseIP("192.168.1.3"))
	ipPool.Reserve(net.ParseIP("192.168.1.6"))

	assert.Equal(t, 2, ipPool.Size())
	assert.Equal(t, "CIDR: 192.168.1.0/29 occupied: 2, reserved: 4, free: 2", ipPool.String())
}

func TestAllocateIPv6SmallestPool(t *testing.T) {
	ipPool, err := ipam.NewIPPool("2a01:4f8:10:20::/127")
	assert.NoError(t, err)

	ip, err := ipPool.Allocate(nil)
	assert.NoError(t, err)
	assert.Equal(t, "2a01:4f8:10:20::1", ip.String())

	_, err = ipPool.Allocate(nil)
	assert.ErrorIs(t, err, ipam.ErrPoolExhausted)
}

func TestReserveIPv6(t *testing.T) {
	ipPool, err := ipam.NewIPPool("2a01:4f8:10:20::/126")
	assert.NoError(t, err)

	gateway := net.ParseIP("2a01:4f8:10:20::1")

	assert.True(t, ipPool.Reserve(gateway))
	assert.False(t, ipPool.Occupy(gateway), "gateway is reserved")
	assert.False(t, ipPool.Reserve(net.ParseIP("2a01:4f8:10:21::1")), "outside the pool")

	allocated := []string{}

	for {
		ip, err := ipPool.Allocate(nil)
		if err != nil {
			assert.ErrorIs(t, err, ipam.ErrPoolExhausted)

			break
		}

		allocated = append(allocated, ip.String())
	}

	assert.Equal(t, []string{"2a01:4f8:10:20::2", "2a01:4f8:10:20::3"}, allocated)
}
