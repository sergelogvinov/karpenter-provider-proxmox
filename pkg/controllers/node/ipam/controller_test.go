package ipam

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/nodeipam"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeProvider implements nodeipam.Provider for controller tests
type fakeProvider struct {
	occupyErr     error
	releaseCalled bool
}

func (f *fakeProvider) UpdateNodeCIDR(ctx context.Context) error                     { return nil }
func (f *fakeProvider) AllocateOrOccupyCIDR(subnet string) error                     { return nil }
func (f *fakeProvider) ReleaseCIDR(subnet string) error                              { return nil }
func (f *fakeProvider) OccupyNodeIPs(node *corev1.Node) error                        { return f.occupyErr }
func (f *fakeProvider) OccupyIP(subnet string) (net.IP, error)                       { return nil, nil }
func (f *fakeProvider) ReleaseNodeIPs(node *corev1.Node) error                       { f.releaseCalled = true; return nil }
func (f *fakeProvider) ReleaseIP(ip string) error                                    { return nil }
func (f *fakeProvider) String() string                                               { return "fake" }

func TestReconcile_NodeDeleted_ReleasesIPs(t *testing.T) {
	fp := &fakeProvider{}
	c := NewController(nil, fp)

	now := metav1.NewTime(time.Now())
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			DeletionTimestamp: &now,
		},
	}

	res, err := c.Reconcile(context.Background(), node)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter)
	assert.True(t, fp.releaseCalled, "expected ReleaseNodeIPs to be called when node is deleting")
}

func TestReconcile_NoSubnetFound_RequeuesNoError(t *testing.T) {
	fp := &fakeProvider{occupyErr: nodeipam.ErrNoSubnetFound}
	c := NewController(nil, fp)

	node := &corev1.Node{} // not deleting
	res, err := c.Reconcile(context.Background(), node)
	assert.NoError(t, err)
	assert.Equal(t, templateRepeatPeriod, res.RequeueAfter, "should requeue after templateRepeatPeriod on ErrNoSubnetFound")
}

func TestReconcile_ProviderError_PropagatesAndRequeues(t *testing.T) {
	myErr := errors.New("boom")
	fp := &fakeProvider{occupyErr: myErr}
	c := NewController(nil, fp)

	node := &corev1.Node{}
	res, err := c.Reconcile(context.Background(), node)
	assert.Error(t, err)
	assert.Equal(t, templateRepeatPeriod, res.RequeueAfter, "should requeue after templateRepeatPeriod on generic error")
}

func TestReconcile_Success_NoRequeue(t *testing.T) {
	fp := &fakeProvider{occupyErr: nil}
	c := NewController(nil, fp)

	node := &corev1.Node{}
	res, err := c.Reconcile(context.Background(), node)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter, "should not requeue on success")
}