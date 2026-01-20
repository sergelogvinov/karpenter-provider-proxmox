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

func TestParseCIDR(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		expect string
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			expect: "CIDR: 192.168.1.0/24 used-map: 0, total: 255",
		},
		{
			name:   "IPv6",
			cidr:   "2a01:4f8:10:20::/96",
			expect: "CIDR: 2a01:4f8:10:20::/96 used-map: 0, total: 4294967295",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)
			assert.Equal(t, tt.expect, ipPool.String())
		})
	}
}

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
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			assert.Equal(t, tt.expect, ipPool.Contains(net.ParseIP(tt.ip)))
		})
	}
}

func TestOccupy(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		occupy []string
		expect string
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			occupy: []string{"192.168.1.1", "192.168.1.2", "192.168.1.7"},
			expect: "CIDR: 192.168.1.0/24 used-map: 1000011, total: 255",
		},
		{
			name:   "IPv4-22",
			cidr:   "192.168.1.22/24",
			occupy: []string{"192.168.1.1", "192.168.1.3", "192.168.1.7"},
			expect: "CIDR: 192.168.1.0/24 used-map: 1000101, total: 255",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			for _, ip := range tt.occupy {
				ok := ipPool.Occupy(net.ParseIP(ip))
				assert.True(t, ok)
			}

			assert.Equal(t, tt.expect, ipPool.String())
		})
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		expect string
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			expect: "192.168.1.1",
		},
		{
			name:   "IPv4-22",
			cidr:   "192.168.1.22/24",
			expect: "192.168.1.23",
		},
		{
			name:   "IPv6",
			cidr:   "2a01:4f8:10:20::/96",
			expect: "2a01:4f8:10:20::1",
		},
		{
			name:   "IPv6-112",
			cidr:   "2a01:4f8:10:20::ff80/112",
			expect: "2a01:4f8:10:20::ff81",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			ip, ipNet, err := net.ParseCIDR(tt.cidr)
			assert.NoError(t, err)
			ipPool.Occupy(ip)

			ipNet.IP = ip

			assert.Equal(t, tt.expect, ipPool.Next(ipNet).String())
		})
	}
}

func TestRelease(t *testing.T) {
	tests := []struct {
		name   string
		cidr   string
		expect string
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			expect: "CIDR: 192.168.1.0/24 used-map: 0, total: 255",
		},
		{
			name:   "IPv4-22",
			cidr:   "192.168.1.22/24",
			expect: "CIDR: 192.168.1.0/24 used-map: 0, total: 255",
		},
		{
			name:   "IPv6",
			cidr:   "2a01:4f8:10:20::/96",
			expect: "CIDR: 2a01:4f8:10:20::/96 used-map: 0, total: 4294967295",
		},
		{
			name:   "IPv6-112",
			cidr:   "2a01:4f8:10:20::ff80/112",
			expect: "CIDR: 2a01:4f8:10:20::/112 used-map: 0, total: 65535",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			ip, _, err := net.ParseCIDR(tt.cidr)
			assert.NoError(t, err)
			ipPool.Occupy(ip)

			assert.NoError(t, ipPool.Release(ip))
			assert.Equal(t, tt.expect, ipPool.String())
		})
	}
}

func TestHostIndex(t *testing.T) {
	tests := []struct {
		name      string
		cidr      string
		host      string
		expect    int
		expectErr bool
	}{
		{
			name:   "IPv4",
			cidr:   "192.168.1.0/24",
			host:   "192.168.1.2",
			expect: 1,
		},
		{
			name:      "IPv4-outside",
			cidr:      "192.168.1.0/24",
			host:      "192.168.2.1",
			expect:    0,
			expectErr: true,
		},
		{
			name:   "IPv6",
			cidr:   "2a01:4f8:10a:2f45::/96",
			host:   "2a01:4f8:10a:2f45::1",
			expect: 0,
		},
		{
			name:   "IPv6-255",
			cidr:   "2a01:4f8:10a:2f45::/96",
			host:   "2a01:4f8:10a:2f45::ff",
			expect: 254,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipPool, err := ipam.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			i, err := ipPool.HostIndex(net.ParseIP(tt.host))
			if tt.expectErr {
				assert.Error(t, err)

				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expect, i)
		})
	}
}
