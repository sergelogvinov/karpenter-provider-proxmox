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

package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/go-proxmox-local/fakelocal"
)

// recordingHandler is a SchedulerHandler that records the VM events it is told.
type recordingHandler struct {
	*SchedulerHandler

	mu     sync.Mutex
	events []string
}

func (r *recordingHandler) VMStarted(ctx context.Context, vmID, pid int) error {
	r.record(fmt.Sprintf("started %d", vmID))

	return r.SchedulerHandler.VMStarted(ctx, vmID, pid)
}

func (r *recordingHandler) VMStopped(ctx context.Context, vmID int) {
	r.record(fmt.Sprintf("stopped %d", vmID))

	r.SchedulerHandler.VMStopped(ctx, vmID)
}

func (r *recordingHandler) VMsRunning(ctx context.Context, running map[int]int) {
	r.record(fmt.Sprintf("running %d", len(running)))

	r.SchedulerHandler.VMsRunning(ctx, running)
}

func (r *recordingHandler) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, event)
}

func (r *recordingHandler) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.events)
}

// TestWatchVMsReportsVMStartOnce checks that qemu creating a VM's PID file and then
// writing the PID into it is one VM start: the scheduler tracks the VM once, not once
// per file event.
func TestWatchVMsReportsVMStartOnce(t *testing.T) {
	tp := liveTopology(t)

	f := fakelocal.New(t, fakelocal.WithGuest(100, "name: shared-vm\ncores: 1\n"))
	client := f.Client()
	h := &recordingHandler{SchedulerHandler: newHandler(t, client, tp, WithSharedPolicy(SharedPolicyNone))}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- watchVMs(ctx, client, h) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done, "watchVMs returns cleanly on shutdown")
	})

	// The PID directory is watched once the initial sync has run.
	require.Eventually(t, func() bool {
		return slices.Contains(h.recorded(), "running 0")
	}, 5*time.Second, 10*time.Millisecond)

	pid := spawnFakeQemuThread(t)

	// SetRunning creates <vmid>.pid and then writes the PID into it, as qemu does.
	f.SetRunning(100, pid)

	require.Eventually(t, func() bool {
		_, ok := h.tracker.get(100)

		return ok
	}, 5*time.Second, 10*time.Millisecond, "the VM start is tracked")

	// Events are handled in order: once the stop is seen, both file events of the
	// start have been.
	f.SetStopped(100)

	require.Eventually(t, func() bool {
		return slices.Contains(h.recorded(), "stopped 100")
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{"running 0", "started 100", "stopped 100"}, h.recorded())
}
