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
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
)

const (
	// pidFileExtension is the file extension for PID files
	pidFileExtension = ".pid"
)

type SchedulerHandler struct {
	t      *topology.CPUTopology
	logger logr.Logger
}

func NewHandler(t *topology.CPUTopology, logger logr.Logger) *SchedulerHandler {
	return &SchedulerHandler{
		t:      t,
		logger: logger,
	}
}

func (r *SchedulerHandler) Reconcile(ctx context.Context, sender reconciler.EventSender, event reconciler.Event) error {
	r.logger.V(2).Info("Processing event", "type", event.Type, "key", event.Key)

	if event.Type == reconciler.FileEvent {
		fsEvent, ok := event.Data.(fsnotify.Event)
		if ok {
			r.logger.V(2).Info("File event details", "name", fsEvent.Name, "op", fsEvent.Op)

			vmIDStr, ok := strings.CutSuffix(fsEvent.Name, pidFileExtension)
			if !ok {
				r.logger.V(2).Info("Ignoring non proxmox PID file", "file", fsEvent.Name)

				return nil
			}

			if fsEvent.Op&fsnotify.Remove == fsnotify.Remove {
				r.logger.V(1).Info("Ignoring PID file removal event", "file", fsEvent.Name)

				return nil
			}

			vmID, err := strconv.Atoi(filepath.Base(vmIDStr))
			if err != nil {
				r.logger.V(1).Info("Ignoring PID file with non-integer name", "file", fsEvent.Name)

				// Non proxmox VM PID files
				return nil //nolint:nilerr
			}

			pid, err := utilsys.GetPidFromFile(fsEvent.Name)
			if err != nil {
				r.logger.Error(err, "Failed to read PID from file", "file", fsEvent.Name)

				return err
			}

			err = r.handleVMStart(ctx, vmID, pid)
			if err != nil {
				r.logger.Error(err, "Failed to handle VM start", "vmID", vmID, "pid", pid)

				return err
			}

			return nil
		}
	}

	return nil
}
