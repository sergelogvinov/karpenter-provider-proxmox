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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"

	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
)

// pidFileExtension is the extension of a VM's PID file, named <vmid>.pid.
const pidFileExtension = ".pid"

// Handler is told which virtual machines start, stop and run.
type Handler interface {
	// VMStarted is called when VM vmID's PID file is created or written. The process
	// may already be gone. An error has the event retried.
	VMStarted(ctx context.Context, vmID, pid int) error
	// VMStopped is called when VM vmID's PID file is removed.
	VMStopped(ctx context.Context, vmID int)
	// VMsRunning is called on every sync with each running VM (ID to PID), and
	// only when every VM's state is known.
	VMsRunning(ctx context.Context, running map[int]int)
}

// Dir is the PID directory: a reconciler.Handler that reads the directory's file
// events, and on a sync the running VMs, as virtual machine events for a Handler.
type Dir struct {
	client  *qemu.Client
	handler Handler
	logger  logr.Logger
}

var _ reconciler.Handler = (*Dir)(nil)

// New returns the PID directory of client, whose run dir must be the directory
// the reconciler watches, telling handler what it finds.
func New(client *qemu.Client, handler Handler, logger logr.Logger) *Dir {
	return &Dir{client: client, handler: handler, logger: logger}
}

// Reconcile turns a file event of a PID file into a start or stop, and a sync event
// into the running VMs. Any other file and any other event is ignored. A failed
// sync tells the handler nothing and is retried.
func (d *Dir) Reconcile(ctx context.Context, _ reconciler.EventSender, event reconciler.Event) error {
	d.logger.V(4).Info("Processing event", "type", event.Type, "key", event.Key)

	switch event.Type {
	case reconciler.FileEvent:
		fsEvent, ok := event.Data.(fsnotify.Event)
		if !ok {
			return nil
		}

		return d.fileEvent(ctx, fsEvent)

	case reconciler.SyncEvent:
		running, err := d.running(ctx)
		if err != nil {
			d.logger.Error(err, "Failed to handle sync event")

			return err
		}

		d.handler.VMsRunning(ctx, running)
	}

	return nil
}

func (d *Dir) fileEvent(ctx context.Context, event fsnotify.Event) error {
	vmID, ok := vmIDOf(event.Name)
	if !ok {
		return nil
	}

	if event.Op == fsnotify.Remove {
		d.handler.VMStopped(ctx, vmID)

		return nil
	}

	pid, err := readPID(event.Name)
	if err != nil {
		return fmt.Errorf("failed to read PID from file %s: %w", event.Name, err)
	}

	if err := d.handler.VMStarted(ctx, vmID, pid); err != nil {
		d.logger.Error(err, "Failed to handle VM start", "vmID", vmID, "pid", pid)

		return err
	}

	return nil
}

// running returns the running VMs, as VM ID to PID. It fails, rather than leave
// a VM out, when some VM's config could not be read: that VM may well run.
func (d *Dir) running(ctx context.Context) (map[int]int, error) {
	guests, err := d.client.List(ctx, qemu.ListFilter{Status: qemu.VMStatusRunning, SkipTemplates: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list running VMs: %w", err)
	}

	running := make(map[int]int, len(guests))

	for _, guest := range guests {
		running[guest.VMID] = guest.PID
	}

	return running, nil
}

// vmIDOf returns the VM ID a PID file is named after; false if name is not
// <vmid>.pid with a positive VM ID.
func vmIDOf(name string) (int, bool) {
	base, ok := strings.CutSuffix(filepath.Base(name), pidFileExtension)
	if !ok {
		return 0, false
	}

	vmID, err := strconv.Atoi(base)
	if err != nil || vmID <= 0 {
		return 0, false
	}

	return vmID, true
}

// readPID returns the positive PID a PID file holds.
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}

	if pid <= 0 {
		return 0, fmt.Errorf("invalid PID %d in file %s", pid, path)
	}

	return pid, nil
}
