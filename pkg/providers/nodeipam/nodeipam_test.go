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

package nodeipam_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam/ipam"

	corev1 "k8s.io/api/core/v1"
)

// fakeCloudCapacity serves the Proxmox node networks keyed by region/zone.
type fakeCloudCapacity struct {
	cloudcapacity.Provider

	networks map[string]map[string]*cloudcapacity.NodeNetworkIfaceInfo
}

func (f *fakeCloudCapacity) Regions() []string {
	regions := []string{}
	for region := range f.networks {
		regions = append(regions, region)
	}

	return regions
}

func (f *fakeCloudCapacity) Zones(region string) []string {
	zones := []string{}
	for zone := range f.networks[region] {
		zones = append(zones, zone)
	}

	return zones
}

func (f *fakeCloudCapacity) GetNetwork(region string, zone string, _ ...func(*cloudcapacity.NodeNetworkIfaceInfo) bool) *cloudcapacity.NodeNetworkIfaceInfo {
	return f.networks[region][zone]
}

func newProvider(networks map[string]map[string]*cloudcapacity.NodeNetworkIfaceInfo) *nodeipam.DefaultProvider {
	return nodeipam.NewDefaultProvider(context.Background(), &fakeCloudCapacity{networks: networks})
}

// allocateAll allocates from 10.0.0.0/29 until the pool is exhausted.
func allocateAll(t *testing.T, p nodeipam.Provider) []string {
	t.Helper()

	allocated := []string{}

	for {
		ip, err := p.AllocateIP("10.0.0.0/29")
		if err != nil {
			assert.ErrorIs(t, err, ipam.ErrPoolExhausted)

			return allocated
		}

		allocated = append(allocated, ip.String())
	}
}

func TestUpdateNodeCIDR(t *testing.T) {
	p := newProvider(map[string]map[string]*cloudcapacity.NodeNetworkIfaceInfo{
		"region-1": {
			"node-1": {Ifaces: map[string]cloudcapacity.NetworkIfaceInfo{
				"vmbr0": {Address4: "10.0.0.3/29", Gateway4: "10.0.0.6"},
				"vmbr1": {Address6: "fd00::3/64"},
			}},
			"node-2": nil,
		},
	})

	assert.NoError(t, p.UpdateNodeCIDR(context.Background()))

	assert.Equal(t, []string{"10.0.0.2", "10.0.0.4", "10.0.0.5"}, allocateAll(t, p),
		"node address is occupied and its gateway is reserved")
}

func TestRegisterSubnet(t *testing.T) {
	p := newProvider(nil)

	assert.Error(t, p.RegisterSubnet("fd00::/96"), "only IPv4 is supported")
	assert.Error(t, p.RegisterSubnet("10.0.0.0"), "invalid CIDR")

	assert.NoError(t, p.RegisterSubnet("10.0.0.3/29"))
	assert.NoError(t, p.RegisterSubnet("10.0.0.0/29"), "registering the subnet again keeps its state")

	assert.Equal(t, []string{"10.0.0.2", "10.0.0.4", "10.0.0.5", "10.0.0.6"}, allocateAll(t, p))
}

func TestAllocateIP(t *testing.T) {
	p := newProvider(nil)

	_, err := p.AllocateIP("10.0.0.0/29")
	assert.Error(t, err, "subnet is not registered")

	assert.NoError(t, p.RegisterSubnet("10.0.0.0/29"))

	ip, err := p.AllocateIP("10.0.0.0/29")
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.2", ip.String())

	ip, err = p.AllocateIP("10.0.0.4/29")
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.5", ip.String(), "allocation starts after the hint")

	allocateAll(t, p)

	_, err = p.AllocateIP("10.0.0.4/29")
	assert.ErrorIs(t, err, ipam.ErrPoolExhausted)
	assert.ErrorContains(t, err, "10.0.0.0/29", "error names the subnet, not the hint")
}

func TestOccupyNodeIPs(t *testing.T) {
	node := &corev1.Node{Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: "10.0.0.2"},
		{Type: corev1.NodeExternalIP, Address: "192.168.1.2"},
		{Type: corev1.NodeHostName, Address: "10.0.0.3"},
		{Type: corev1.NodeInternalIP, Address: "fd00::2"},
	}}}

	p := newProvider(nil)
	assert.ErrorIs(t, p.OccupyNodeIPs(node), nodeipam.ErrNoSubnetFound)

	assert.NoError(t, p.RegisterSubnet("10.0.0.0/29"))
	assert.NoError(t, p.OccupyNodeIPs(node))

	assert.Equal(t, []string{"10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6"}, allocateAll(t, p),
		"only internal and external IPv4 addresses are occupied")
}

func TestReleaseIP(t *testing.T) {
	p := newProvider(nil)
	assert.ErrorIs(t, p.ReleaseIP("10.0.0.2/29"), nodeipam.ErrNoSubnetFound)

	assert.NoError(t, p.RegisterSubnet("10.0.0.0/29"))
	allocateAll(t, p)

	assert.NoError(t, p.ReleaseIP("10.0.0.4/29"))
	assert.ErrorIs(t, p.ReleaseIP("10.0.1.4/29"), nodeipam.ErrNoSubnetFound)
	assert.Error(t, p.ReleaseIP("10.0.0.4"), "invalid CIDR")

	assert.Equal(t, []string{"10.0.0.4"}, allocateAll(t, p))
}

func TestConcurrentRegisterAndAllocate(t *testing.T) {
	p := newProvider(nil)
	assert.NoError(t, p.RegisterSubnet("10.0.0.0/24"))

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		allocated = map[string]bool{}
	)

	for i := range 50 {
		wg.Add(2)

		go func() {
			defer wg.Done()

			assert.NoError(t, p.RegisterSubnet(fmt.Sprintf("10.1.%d.0/24", i)))
		}()

		go func() {
			defer wg.Done()

			ip, err := p.AllocateIP("10.0.0.0/24")
			assert.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()

			assert.False(t, allocated[ip.String()], "IP %s allocated twice", ip)
			allocated[ip.String()] = true
		}()
	}

	wg.Wait()

	assert.Len(t, allocated, 50)
}

func TestRegisterSubnetReusesPoolContainingTheNetwork(t *testing.T) {
	p := newProvider(nil)

	assert.NoError(t, p.RegisterSubnet("10.0.0.0/29"))
	assert.NoError(t, p.RegisterSubnet("10.0.0.10/24"), "network 10.0.0.0 is already in a pool")
	assert.NoError(t, p.RegisterSubnet("10.0.0.0/30"), "network 10.0.0.0 is already in a pool")

	assert.Equal(t, "NodeIpamProvider{subnets: 1, capacity: [CIDR: 10.0.0.0/29 occupied: 0, reserved: 3, free: 5]}", p.String())
}
