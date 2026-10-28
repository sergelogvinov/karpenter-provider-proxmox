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

package instancetemplate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pxpool "github.com/sergelogvinov/go-proxmox-pool"
	"github.com/sergelogvinov/go-proxmox-rest/fakeapi"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/storage"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetemplate"
)

const region = "region-1"

// fakeCloudCapacity serves storages by name.
type fakeCloudCapacity struct {
	cloudcapacity.Provider

	storages map[string]*cloudcapacity.NodeStorageCapacityInfo
}

func (f *fakeCloudCapacity) GetStorage(_ string, name string, filter ...func(*cloudcapacity.NodeStorageCapacityInfo) bool) *cloudcapacity.NodeStorageCapacityInfo {
	info, ok := f.storages[name]
	if !ok {
		return nil
	}

	for _, fn := range filter {
		if !fn(info) {
			return nil
		}
	}

	return info
}

// newProvider returns a provider for one region backed by an in-memory
// Proxmox cluster.
func newProvider(t *testing.T, cluster *fakeapi.Cluster, capacity cloudcapacity.Provider) *instancetemplate.DefaultProvider {
	t.Helper()

	pool, err := pxpool.NewProxmoxPool([]*pxpool.ClusterConfig{{
		Region:      region,
		URL:         "https://127.0.0.1:8006/api2/json",
		TokenID:     "user@pve!token",
		TokenSecret: "secret",
	}})
	require.NoError(t, err)

	pool.Set(region, cluster.Client(t))

	return instancetemplate.NewDefaultProvider(context.Background(), pool, capacity)
}

// TestDeleteTwoImageStoragesInOneZone deletes two templates of one zone whose
// image sits on two import storages there. Delete used to lock the zone once
// per storage and hold every lock until it returned, so the second lock never
// succeeded.
func TestDeleteTwoImageStoragesInOneZone(t *testing.T) {
	t.Parallel()

	const (
		zone    = "pve-1"
		imageID = "noble-123.raw"
	)

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	for _, name := range []string{"local", "nfs"} {
		cluster.Node(zone).AddStorage(name, "dir", fakeapi.WithVolume(storage.Volume{VolID: name + ":import/" + imageID}))
	}

	capacity := &fakeCloudCapacity{storages: map[string]*cloudcapacity.NodeStorageCapacityInfo{
		"local": {Name: "local", Region: region, Capabilities: []string{"import", "images"}, Zones: []string{zone}},
		"nfs":   {Name: "nfs", Region: region, Capabilities: []string{"import", "images"}, Zones: []string{zone}},
	}}

	p := newProvider(t, cluster, capacity)

	templateClass := &v1alpha1.ProxmoxTemplate{}
	templateClass.Spec.StorageIDs = []string{"local", "nfs"}

	placements := []v1alpha1.TemplatePlacement{
		{Region: region, Zone: zone, TemplateID: 1000},
		{Region: region, Zone: zone, TemplateID: 1001},
	}

	type deleted struct {
		remaining []v1alpha1.TemplatePlacement
		err       error
	}

	done := make(chan deleted, 1)

	go func() {
		remaining, err := p.Delete(context.Background(), templateClass, imageID, placements)
		done <- deleted{remaining, err}
	}()

	select {
	case res := <-done:
		require.NoError(t, res.err)
		assert.Empty(t, res.remaining)
	case <-time.After(10 * time.Second):
		t.Fatal("Delete deadlocked on the zone lock")
	}

	assert.Equal(t, v1alpha1.ProxmoxTemplateStatus{}, templateClass.Status, "Delete must not touch status")

	client := cluster.Client(t)
	for _, name := range []string{"local", "nfs"} {
		content, err := client.Nodes(zone).Storage().Content(name).List(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, content, "image left on %s", name)
	}
}

// TestCreateRegionWithoutCommonZoneIsUnresolved resolves both storages of a
// region but finds no zone that has both. The region's zones are unknown, so
// it must not show up as targeted with no zones, which would make every VM
// template there stale.
func TestCreateRegionWithoutCommonZoneIsUnresolved(t *testing.T) {
	t.Parallel()

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes("pve-1", "pve-2"))

	capacity := &fakeCloudCapacity{storages: map[string]*cloudcapacity.NodeStorageCapacityInfo{
		"local": {Name: "local", Region: region, Capabilities: []string{"import"}, Zones: []string{"pve-1"}},
		"lvm":   {Name: "lvm", Region: region, Capabilities: []string{"images"}, Zones: []string{"pve-2"}},
	}}

	p := newProvider(t, cluster, capacity)

	templateClass := &v1alpha1.ProxmoxTemplate{}
	templateClass.Name = "talos"
	templateClass.Spec.StorageIDs = []string{"local", "lvm"}

	result, err := p.Create(context.Background(), templateClass)
	require.NoError(t, err)

	assert.Empty(t, result.Placements)
	assert.NotContains(t, result.Targets, region)
	assert.Equal(t, []string{region}, result.Unresolved)
}

// placedZone is the zone setupPlacedTemplate places the VM template in.
const placedZone = "pve-1"

// setupPlacedTemplate returns a ProxmoxTemplate whose status places its VM
// template 1000 in placedZone, with the image already imported there. The test
// seeds the VM template itself.
func setupPlacedTemplate(t *testing.T) (*fakeapi.Cluster, *instancetemplate.DefaultProvider, *v1alpha1.ProxmoxTemplate) {
	t.Helper()

	const zone = placedZone

	templateClass := &v1alpha1.ProxmoxTemplate{}
	templateClass.Name = "talos"
	templateClass.Spec.SourceImage = &v1alpha1.SourceImage{URL: "https://example.com/talos.raw.xz", ImageName: "talos.raw"}
	templateClass.Spec.StorageIDs = []string{"local"}
	templateClass.SetTemplatePlacements([]v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: 1000}})

	cluster := fakeapi.NewCluster(t, fakeapi.WithNodes(zone))
	cluster.Node(zone).AddStorage("local", "dir", fakeapi.WithVolume(storage.Volume{VolID: "local:import/" + templateClass.GetImageID()}))

	capacity := &fakeCloudCapacity{storages: map[string]*cloudcapacity.NodeStorageCapacityInfo{
		"local": {Name: "local", Region: region, Capabilities: []string{"import", "images"}, Zones: []string{zone}},
	}}

	return cluster, newProvider(t, cluster, capacity), templateClass
}

// TestCreateReusesPlacedTemplateMissingFromCache creates a template whose VM
// template is in status but not yet listed as a template by cluster/resources,
// which happens for a few seconds after Proxmox creates it. Create must reuse
// it instead of creating a second one and leaking the first.
func TestCreateReusesPlacedTemplateMissingFromCache(t *testing.T) {
	t.Parallel()

	const zone = placedZone

	cluster, p, templateClass := setupPlacedTemplate(t)
	cluster.Node(zone).AddVM(1000, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: " + templateClass.Hash()})
	cluster.FailRequests("GET /cluster/resources")

	result, err := p.Create(context.Background(), templateClass)
	require.NoError(t, err)

	assert.Equal(t, []v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: 1000}}, result.Placements)
	assert.Empty(t, result.Created)

	_, err = cluster.Client(t).Nodes(zone).Qemu().Config(context.Background(), 1001, nil)
	assert.Error(t, err, "Create made a second VM template")
}

// TestCreateReplacesOutdatedPlacedTemplateMissingFromCache creates a template
// whose outdated VM template is in status but missing from the cache. Create
// must replace it under the same vmid, not leave it behind.
func TestCreateReplacesOutdatedPlacedTemplateMissingFromCache(t *testing.T) {
	t.Parallel()

	const zone = placedZone

	cluster, p, templateClass := setupPlacedTemplate(t)
	cluster.Node(zone).AddVM(1000, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: outdated"})
	cluster.FailRequests("GET /cluster/resources")

	result, err := p.Create(context.Background(), templateClass)
	require.NoError(t, err)

	placement := v1alpha1.TemplatePlacement{Region: region, Zone: zone, TemplateID: 1000}
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, result.Placements)
	assert.Equal(t, []v1alpha1.TemplatePlacement{placement}, result.Created)

	cfg, err := cluster.Client(t).Nodes(zone).Qemu().Config(context.Background(), 1000, nil)
	require.NoError(t, err)
	assert.Contains(t, cfg.Description, "Hash: "+templateClass.Hash())

	_, err = cluster.Client(t).Nodes(zone).Qemu().Config(context.Background(), 1001, nil)
	assert.Error(t, err, "Create made a second VM template")
}

// TestCreateReusesPlacedTemplateBesideCachedOutdatedOne creates a template
// whose cache lists an outdated VM template in the zone but not the up to
// date one status places there. Create must delete the outdated one and reuse
// the placed one, not rebuild the outdated one and leak the placed one.
func TestCreateReusesPlacedTemplateBesideCachedOutdatedOne(t *testing.T) {
	t.Parallel()

	const zone = placedZone

	cluster, p, templateClass := setupPlacedTemplate(t)
	cluster.Node(zone).AddVM(900, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: outdated"})
	require.NoError(t, p.SyncInstanceTemplates(context.Background()))

	cluster.Node(zone).AddVM(1000, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: " + templateClass.Hash()})
	cluster.FailRequests("GET /cluster/resources")

	result, err := p.Create(context.Background(), templateClass)
	require.NoError(t, err)

	assert.Equal(t, []v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: 1000}}, result.Placements)
	assert.Empty(t, result.Created)

	client := cluster.Client(t)
	_, err = client.Nodes(zone).Qemu().Config(context.Background(), 900, nil)
	assert.Error(t, err, "outdated VM template 900 was kept")

	_, err = client.Nodes(zone).Qemu().Config(context.Background(), 1000, nil)
	assert.NoError(t, err, "placed VM template 1000 was deleted")
}

// TestCreateKeepsPlacedTemplateOverCachedDuplicate creates a template whose
// cache lists an up to date VM template in the zone, leaked by an earlier
// pass, but not the one status places there. Create must keep the placed one
// and delete the duplicate, not keep the duplicate and leak the placed one.
func TestCreateKeepsPlacedTemplateOverCachedDuplicate(t *testing.T) {
	t.Parallel()

	const zone = placedZone

	cluster, p, templateClass := setupPlacedTemplate(t)
	cluster.Node(zone).AddVM(900, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: " + templateClass.Hash()})
	require.NoError(t, p.SyncInstanceTemplates(context.Background()))

	cluster.Node(zone).AddVM(1000, &qemu.Config{Name: "talos", Template: new(true), Description: "Hash: " + templateClass.Hash()})
	cluster.FailRequests("GET /cluster/resources")

	result, err := p.Create(context.Background(), templateClass)
	require.NoError(t, err)

	assert.Equal(t, []v1alpha1.TemplatePlacement{{Region: region, Zone: zone, TemplateID: 1000}}, result.Placements)
	assert.Empty(t, result.Created)

	client := cluster.Client(t)
	_, err = client.Nodes(zone).Qemu().Config(context.Background(), 900, nil)
	assert.Error(t, err, "duplicate VM template 900 was kept")

	_, err = client.Nodes(zone).Qemu().Config(context.Background(), 1000, nil)
	assert.NoError(t, err, "placed VM template 1000 was deleted")
}

// TestCreateDeletesTemplateOfFailedTask creates a VM template whose create
// task fails after Proxmox made the guest. Create must delete that guest.
func TestCreateDeletesTemplateOfFailedTask(t *testing.T) {
	t.Parallel()

	const zone = placedZone

	cluster, p, templateClass := setupPlacedTemplate(t)
	templateClass.SetTemplatePlacements(nil)
	cluster.FailRequests("POST /nodes/{node}/qemu", fakeapi.AsFailedTask())

	result, err := p.Create(context.Background(), templateClass)
	require.Error(t, err)
	assert.Empty(t, result.Placements)

	_, err = cluster.Client(t).Nodes(zone).Qemu().Config(context.Background(), 1000, nil)
	assert.Error(t, err, "VM template of the failed create task was kept")
}
