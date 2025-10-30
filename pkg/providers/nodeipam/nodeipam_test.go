package nodeipam

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	ipam "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam/ipam"

	corev1 "k8s.io/api/core/v1"
)

func TestOccupyNodeIPs_NoSubnets_ReturnsErrNoSubnetFound(t *testing.T) {
	p := &DefaultProvider{}
	node := &corev1.Node{}
	err := p.OccupyNodeIPs(node)
	assert.Equal(t, ErrNoSubnetFound, err)
}

func TestReleaseNodeIPs_NoSubnets_ReturnsErrNoSubnetFound(t *testing.T) {
	p := &DefaultProvider{}
	node := &corev1.Node{}
	err := p.ReleaseNodeIPs(node)
	assert.Equal(t, ErrNoSubnetFound, err)
}

func TestOccupyIP_SelectsFromMatchingSubnet(t *testing.T) {
	// Prepare provider with a 192.168.1.0/24 pool
	ipPool, err := ipam.ParseCIDR("192.168.1.0/24")
	assert.NoError(t, err)
	p := &DefaultProvider{
		subnets: []*ipam.IPPool{ipPool},
	}

	ip, err := p.OccupyIP("192.168.1.22/24")
	assert.NoError(t, err)
	assert.NotNil(t, ip)
	assert.Equal(t, "192.168.1.22", ip.String(), "should allocate an IP from the specified subnet")
}

func TestOccupyIP_NoMatchingSubnet_ReturnsError(t *testing.T) {
	p := &DefaultProvider{} // no subnets
	ip, err := p.OccupyIP("10.0.0.0/24")
	assert.Error(t, err)
	assert.Nil(t, ip)
}

func TestOccupyAndReleaseNodeIPs_UpdatesPoolUsage(t *testing.T) {
	// Pool contains 192.168.1.0/24; node has an IP in that subnet
	ipPool, err := ipam.ParseCIDR("192.168.1.0/24")
	assert.NoError(t, err)

	p := &DefaultProvider{
		subnets: []*ipam.IPPool{ipPool},
	}

	node := &corev1.Node{
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "192.168.1.10"},
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},           // outside subnet, ignored
				{Type: corev1.NodeExternalIP, Address: "2001:db8::1"},        // IPv6, ignored by updateNodeIPs
				{Type: corev1.NodeExternalIP, Address: "192.168.1.200"},      // inside subnet, counted
			},
		},
	}

	// Occupy should mark 2 IPv4 addresses in the pool
	err = p.OccupyNodeIPs(node)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, ipPool.Size(), 2, "expected at least two occupied addresses from the node")

	// Release should clear those addresses
	err = p.ReleaseNodeIPs(node)
	assert.NoError(t, err)
	assert.Equal(t, 0, ipPool.Size(), "expected pool to be empty after release")
}