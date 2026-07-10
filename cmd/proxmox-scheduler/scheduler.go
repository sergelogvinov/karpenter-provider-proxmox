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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
	utilsys "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/sys"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/vmconfig"

	"k8s.io/utils/cpuset"
)

// VMInfo holds information for a VM
type VMInfo struct {
	VMID  int
	PID   int
	Name  string
	Cores int

	AffinitySet cpuset.CPUSet
	AssignedSet cpuset.CPUSet
}

// VMTracker holds the tracking information for all VMs
type VMTracker struct {
	// vms maps VM ID to VMInfo for all tracked VMs
	vms map[int]*VMInfo

	// sharedCPUs CPUs that are shared among VMs without affinity assignments
	sharedCPUs cpuset.CPUSet
	// Used CPUs of VM with affinity assignments
	usedCPUs cpuset.CPUSet

	mu sync.RWMutex
}

type SchedulerHandler struct {
	topology *topology.Topology
	tracker  *VMTracker

	logger logr.Logger
}

const (
	// pidFileExtension is the file extension for PID files
	pidFileExtension = ".pid"
)

func NewHandler(topology *topology.Topology, logger logr.Logger) *SchedulerHandler {
	return &SchedulerHandler{
		topology: topology,
		tracker: &VMTracker{
			vms: make(map[int]*VMInfo),
		},
		logger: logger,
	}
}

func (r *SchedulerHandler) Reconcile(ctx context.Context, sender reconciler.EventSender, event reconciler.Event) error {
	r.logger.V(4).Info("Processing event", "type", event.Type, "key", event.Key)

	if event.Type == reconciler.FileEvent {
		fsEvent, ok := event.Data.(fsnotify.Event)
		if ok {
			vmIDStr, ok := strings.CutSuffix(fsEvent.Name, pidFileExtension)
			if !ok { // Not a PID file, ignore
				return nil //nolint:nilerr
			}

			vmID, err := strconv.Atoi(filepath.Base(vmIDStr))
			if err != nil || vmID <= 0 { // Filename doesn't contain a valid VM ID, ignore
				return nil //nolint:nilerr
			}

			if fsEvent.Op == fsnotify.Remove {
				err = r.handleVMStop(ctx, vmID)
				if err != nil {
					r.logger.Error(err, "Failed to handle VM stop", "vmID", vmID)
				}

				return nil
			}

			pid, err := utilsys.GetPidFromFile(fsEvent.Name)
			if err != nil {
				return fmt.Errorf("failed to read PID from file %s: %w", fsEvent.Name, err)
			}

			err = r.handleVMStart(ctx, vmID, pid)
			if err != nil {
				r.logger.Error(err, "Failed to handle VM start", "vmID", vmID, "pid", pid)

				return err
			}

			return nil
		}
	}

	if event.Type == reconciler.SyncEvent {
		err := r.handleSyncEvent(ctx)
		if err != nil {
			r.logger.Error(err, "Failed to handle sync event")

			return err
		}

		return nil
	}

	return nil
}

// handleSyncEvent processes sync events to track VM information
func (r *SchedulerHandler) handleSyncEvent(_ context.Context) error {
	r.logger.V(1).Info("Starting VM tracking")

	runningVMs, err := r.getRunningVMs()
	if err != nil {
		return fmt.Errorf("failed to get running VMs: %w", err)
	}

	r.tracker.mu.Lock()
	r.tracker.usedCPUs = cpuset.New()
	r.tracker.vms = make(map[int]*VMInfo)
	r.tracker.mu.Unlock()

	for vmID, pid := range runningVMs {
		vmConfig, err := vmconfig.LoadVMConfig(vmID)
		if err != nil {
			r.logger.Error(err, "Failed to load VM config for running VM", "vmID", vmID)

			continue
		}

		if err := r.updateVMInfo(vmID, pid, vmConfig); err != nil {
			r.logger.Error(err, "Failed to update VM info", "vmID", vmID, "pid", pid)

			continue
		}
	}

	r.logVMStatus()

	return nil
}

// getRunningVMs scans the PID directory and returns a map of VM ID to PID for running VMs
func (r *SchedulerHandler) getRunningVMs() (map[int]int, error) {
	runningVMs := make(map[int]int)

	entries, err := os.ReadDir(*watchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read PID directory %s: %w", *watchPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), pidFileExtension) {
			continue
		}

		vmID, pid, err := getVMIDs(filepath.Join(*watchPath, entry.Name()))
		if err != nil {
			r.logger.Error(err, "Failed to get VM IDs from file event", "file", entry.Name())

			continue
		}

		if !utilsys.ProcessExists(pid) {
			continue
		}

		runningVMs[vmID] = pid
	}

	return runningVMs, nil
}

// logVMStatus logs the current status of all tracked VMs
func (r *SchedulerHandler) logVMStatus() {
	r.tracker.mu.RLock()
	defer r.tracker.mu.RUnlock()

	if r.topology == nil || len(r.tracker.vms) == 0 {
		return
	}

	allCPUs := r.topology.CPUDetails.CPUs()
	if allCPUs.Size() == 0 {
		return
	}

	r.logger.V(1).Info("Current VM status",
		"totalCPUs", allCPUs.Size(),
		"usedCPUs", r.tracker.usedCPUs.String(),
		"sharedCPUs", r.tracker.sharedCPUs.String(),
	)

	for vmID, vmInfo := range r.tracker.vms {
		r.logger.V(2).Info("VM info",
			"vmID", vmID,
			"name", vmInfo.Name,
			"cores", vmInfo.Cores,
			"affinity", vmInfo.AffinitySet.String(),
			"pid", vmInfo.PID)
	}
}

func getVMIDs(pidFile string) (vmID, pid int, err error) {
	vmIDStr, ok := strings.CutSuffix(pidFile, pidFileExtension)
	if !ok {
		return 0, 0, fmt.Errorf("not a proxmox PID file: %s", pidFile)
	}

	vmID, err = strconv.Atoi(filepath.Base(vmIDStr))
	if err != nil {
		return 0, 0, fmt.Errorf("not a proxmox VM PID file: %s", pidFile)
	}

	pid, err = utilsys.GetPidFromFile(pidFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read PID from file %s: %w", pidFile, err)
	}

	return vmID, pid, nil
}
