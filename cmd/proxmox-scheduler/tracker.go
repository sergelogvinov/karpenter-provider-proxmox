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
	"maps"
	"slices"
	"sync"

	"k8s.io/utils/cpuset"
)

// trackedVM is what the scheduler knows of a running VM beyond its zone record:
// its process, and the cpuset last applied to its threads.
type trackedVM struct {
	pid int
	// applied is the cpuset taskset last set on the VM's threads; empty when none was.
	applied cpuset.CPUSet
}

// VMTracker holds the process and applied cpuset of every running VM.
type VMTracker struct {
	vms map[int]trackedVM

	mu sync.RWMutex
}

func newVMTracker() *VMTracker {
	return &VMTracker{vms: map[int]trackedVM{}}
}

// track records VM id running as process pid. A fresh track, or one with a new PID,
// forgets the applied cpuset: it says nothing about the new process. It reports
// whether the VM was tracked before with another PID.
func (t *VMTracker) track(id, pid int, fresh bool) (restarted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	prev, ok := t.vms[id]
	restarted = ok && prev.pid != pid

	vm := trackedVM{pid: pid, applied: cpuset.New()}
	if ok && !restarted && !fresh {
		vm.applied = prev.applied
	}

	t.vms[id] = vm

	return restarted
}

// forget drops VM id, returning what was tracked for it.
func (t *VMTracker) forget(id int) (trackedVM, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	vm, ok := t.vms[id]
	delete(t.vms, id)

	return vm, ok
}

// forgetPID drops VM id only while it still runs as process pid, so a caller holding
// a stale PID never drops the process that replaced it. It reports whether it dropped.
func (t *VMTracker) forgetPID(id, pid int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if vm, ok := t.vms[id]; !ok || vm.pid != pid {
		return false
	}

	delete(t.vms, id)

	return true
}

// get returns what is tracked for VM id.
func (t *VMTracker) get(id int) (trackedVM, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	vm, ok := t.vms[id]

	return vm, ok
}

// notIn returns the tracked VMs that running lacks, in ID order.
func (t *VMTracker) notIn(running map[int]int) []int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	gone := []int{}

	for _, id := range slices.Sorted(maps.Keys(t.vms)) {
		if _, ok := running[id]; !ok {
			gone = append(gone, id)
		}
	}

	return gone
}

// all returns a copy of every tracked VM.
func (t *VMTracker) all() map[int]trackedVM {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return maps.Clone(t.vms)
}

// appliedCPUs returns every VM's applied cpuset, for zonecapacity.Zone.Plan.
func (t *VMTracker) appliedCPUs() map[int]cpuset.CPUSet {
	t.mu.RLock()
	defer t.mu.RUnlock()

	applied := make(map[int]cpuset.CPUSet, len(t.vms))

	for id, vm := range t.vms {
		if !vm.applied.IsEmpty() {
			applied[id] = vm.applied
		}
	}

	return applied
}

// setApplied records that cpus were applied to VM id's process pid. It is a no-op
// if the VM has stopped or restarted with another PID since.
func (t *VMTracker) setApplied(id, pid int, cpus cpuset.CPUSet) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if vm, ok := t.vms[id]; ok && vm.pid == pid {
		vm.applied = cpus
		t.vms[id] = vm
	}
}
