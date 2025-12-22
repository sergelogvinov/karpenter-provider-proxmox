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

package locks

import "sync"

// Locks is a structure that protects to multiple VMs changes.
type Locks struct {
	locks sync.Map
}

// NewLocks creates a new instance of Locks.
func NewLocks() *Locks {
	return &Locks{}
}

// Lock method locks a VM by its name.
func (v *Locks) Lock(name string) {
	actual, _ := v.locks.LoadOrStore(name, &sync.Mutex{})
	actual.(*sync.Mutex).Lock()
}

// Unlock method unlocks a VM by its name.
func (v *Locks) Unlock(name string) {
	if actual, ok := v.locks.Load(name); ok {
		actual.(*sync.Mutex).Unlock()
	}
}
