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
	"strconv"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
)

// deadPID is never a real process.
const deadPID = 1<<31 - 1

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
	d := New(dir, rec, logr.Discard())

	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Create)))
	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Write)))
	require.NoError(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Remove)))

	assert.Equal(t, [][2]int{{101, 4242}, {101, 4242}}, rec.started, "a create and a write are both starts")
	assert.Equal(t, []int{101}, rec.stopped)
}

func TestFileEventIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()

	rec := &recorder{}
	d := New(dir, rec, logr.Discard())

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
	d := New(dir, rec, logr.Discard())

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
	d := New(dir, rec, logr.Discard())

	assert.ErrorIs(t, d.Reconcile(t.Context(), nil, fileEvent(path, fsnotify.Create)), errNotReady)
}

// TestSyncListsLiveVMs checks that a sync tells the handler only the VMs whose
// process exists, skipping every file that is not a readable PID file.
func TestSyncListsLiveVMs(t *testing.T) {
	dir := t.TempDir()
	live := os.Getpid()

	writePIDFile(t, dir, "101.pid", strconv.Itoa(live))
	writePIDFile(t, dir, "102.pid", strconv.Itoa(deadPID))
	writePIDFile(t, dir, "103.pid", "garbage")
	writePIDFile(t, dir, "0.pid", strconv.Itoa(live))
	writePIDFile(t, dir, "104.qmp", strconv.Itoa(live))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "105.pid"), 0o700))

	rec := &recorder{}
	d := New(dir, rec, logr.Discard())

	require.NoError(t, d.Reconcile(t.Context(), nil, syncEvent()))

	assert.Equal(t, []map[int]int{{101: live}}, rec.running)
}

func TestSyncFailsWithoutDirectory(t *testing.T) {
	rec := &recorder{}
	d := New(filepath.Join(t.TempDir(), "missing"), rec, logr.Discard())

	assert.Error(t, d.Reconcile(t.Context(), nil, syncEvent()))
	assert.Empty(t, rec.running, "a failed scan never says that no VM runs")
}
