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

package piddir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/fakelocal"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
)

// recorder is a Handler that records what it is told, failing starts with startErr.
type recorder struct {
	started  [][2]int
	stopped  []int
	running  []map[int]int
	startErr error
}

func (r *recorder) VMStarted(_ context.Context, vmID, pid int) error {
	r.started = append(r.started, [2]int{vmID, pid})

	return r.startErr
}

func (r *recorder) VMStopped(_ context.Context, vmID int) {
	r.stopped = append(r.stopped, vmID)
}

func (r *recorder) VMsRunning(_ context.Context, running map[int]int) {
	r.running = append(r.running, running)
}

// writePIDFile writes a PID file name holding content into dir, returning its path.
func writePIDFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func fileEvent(path string, op fsnotify.Op) reconciler.Event {
	return reconciler.Event{Type: reconciler.FileEvent, Key: path, Data: fsnotify.Event{Name: path, Op: op}}
}

func syncEvent() reconciler.Event {
	return reconciler.Event{Type: reconciler.SyncEvent, Key: "sync", Data: time.Now()}
}

func TestFileEventStartsAndStopsVM(t *testing.T) {
	dir := t.TempDir()
	path := writePIDFile(t, dir, "101.pid", "4242\n")

	rec := &recorder{}
	d := New(fakelocal.New(t).Client().Qemu(), rec, logr.Discard())

	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Create)))
	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Write)))
	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Remove)))

	assert.Equal(t, [][2]int{{101, 4242}, {101, 4242}}, rec.started, "a create and a write are both starts")
	assert.Equal(t, []int{101}, rec.stopped)
}

func TestFileEventIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()

	rec := &recorder{}
	d := New(fakelocal.New(t).Client().Qemu(), rec, logr.Discard())

	for _, name := range []string{"101.qmp", "abc.pid", "0.pid", "-5.pid"} {
		path := writePIDFile(t, dir, name, "4242")

		require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Create)), name)
		require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Remove)), name)
	}

	require.NoError(t, d.Reconcile(t.Context(), nil, reconciler.Event{Type: reconciler.FileEvent, Data: "not an fsnotify event"}))

	assert.Empty(t, rec.started)
	assert.Empty(t, rec.stopped)
}

// TestFileEventRetriesUnreadablePID checks that a PID file not yet written, or gone,
// is an error the reconciler retries, never a start.
func TestFileEventRetriesUnreadablePID(t *testing.T) {
	dir := t.TempDir()

	rec := &recorder{}
	d := New(fakelocal.New(t).Client().Qemu(), rec, logr.Discard())

	empty := writePIDFile(t, dir, "101.pid", "")
	assert.Error(t, d.Reconcile(t.Context(), nil, fileEvent(empty, fsnotify.Create)))

	zero := writePIDFile(t, dir, "102.pid", "0")
	assert.Error(t, d.Reconcile(t.Context(), nil, fileEvent(zero, fsnotify.Create)))

	assert.Error(t, d.Reconcile(t.Context(), nil, fileEvent(filepath.Join(dir, "103.pid"), fsnotify.Write)))

	assert.Empty(t, rec.started)
}

func TestFileEventReturnsStartError(t *testing.T) {
	dir := t.TempDir()
	path := writePIDFile(t, dir, "101.pid", "4242")

	errNotReady := errors.New("no CPU threads yet")
	rec := &recorder{startErr: errNotReady}
	d := New(fakelocal.New(t).Client().Qemu(), rec, logr.Discard())

	assert.ErrorIs(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Create)), errNotReady)
}

// TestSyncListsRunningVMs checks that a sync tells the handler only the VMs whose
// QEMU process runs, never a stopped VM, a template or one whose PID was reused.
func TestSyncListsRunningVMs(t *testing.T) {
	f := fakelocal.New(t,
		fakelocal.WithGuest(101, "name: running\n"),
		fakelocal.WithGuest(102, "name: stopped\n"),
		fakelocal.WithGuest(103, "name: template\ntemplate: 1\n"),
		fakelocal.WithGuest(104, "name: reused-pid\n"),
		fakelocal.WithRunning(101, 4101),
		fakelocal.WithRunning(103, 4103),
		fakelocal.WithRunning(999, 4104),
	)

	// 104's PID file names the process that now runs VM 999.
	runDir := f.Client().RunDir()
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "104.pid"), []byte("4104\n"), 0o600))

	rec := &recorder{}
	d := New(f.Client().Qemu(), rec, logr.Discard())

	require.NoError(t, d.Reconcile(t.Context(), nil, syncEvent()))

	assert.Equal(t, []map[int]int{{101: 4101}}, rec.running)
}

// TestSyncFailsOnUnreadableConfig checks that a VM whose config cannot be read
// fails the sync, so it is retried, rather than read as stopped.
func TestSyncFailsOnUnreadableConfig(t *testing.T) {
	f := fakelocal.New(t,
		fakelocal.WithGuest(101, "name: running\n"),
		fakelocal.WithRunning(101, 4101),
	)

	// A config path that reads as a directory fails the read.
	target := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(f.Client().ConfigDir(), "qemu-server", "102.conf")))

	rec := &recorder{}
	d := New(f.Client().Qemu(), rec, logr.Discard())

	err := d.Reconcile(t.Context(), nil, syncEvent())

	var listErr *qemu.ListError
	require.ErrorAs(t, err, &listErr)
	assert.Contains(t, listErr.Skipped, 102)
	assert.Empty(t, rec.running, "a failed sync never says which VMs run")
}

func TestSyncFailsWithoutDirectory(t *testing.T) {
	client, err := local.New(local.WithRoot(t.TempDir()))
	require.NoError(t, err)

	rec := &recorder{}
	d := New(client.Qemu(), rec, logr.Discard())

	assert.Error(t, d.Reconcile(t.Context(), nil, syncEvent()))
	assert.Empty(t, rec.running, "a failed scan never says that no VM runs")
}
