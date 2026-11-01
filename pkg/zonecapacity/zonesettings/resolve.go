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

package zonesettings

import (
	"fmt"

	"github.com/sergelogvinov/go-proxmox-rest/nodes"
	"github.com/sergelogvinov/go-proxmox-rest/nodes/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/nodecapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity/settings"
)

// hostReservedMemory is the memory kept for the host itself when the zone has no
// hugepage pool.
const hostReservedMemory = 1024 * 1024 * 1024 // 1GiB

// Resolve returns a zone's settings from the node status, the node-capacity guest's
// config and the node settings file's entry for the zone, each overriding the one
// before. A nil guest or file is a layer the zone doesn't have.
func Resolve(status *nodes.Status, guest *qemu.Config, file *settings.NodeSettings) (settings.NodeSettings, error) {
	zone := settings.NodeSettings{}

	guess, err := guessFromStatus(status)
	if err != nil {
		return zone, fmt.Errorf("guessing from the node status: %w", err)
	}

	if guess != nil {
		zone = *guess
	}

	zone.ReservedMemory = hostReservedMemory

	if guest != nil {
		if err := applyGuest(&zone, guest); err != nil {
			return zone, err
		}
	}

	if file != nil {
		applyFile(&zone, file)
	}

	return zone, nil
}

// applyGuest applies what the node-capacity guest says of its zone. The guest
// describes the real host, so NUMA nodes it knows replace the guessed ones whole.
func applyGuest(zone *settings.NodeSettings, cfg *qemu.Config) error {
	guest, err := nodecapacity.Decode(cfg)
	if err != nil {
		return fmt.Errorf("failed to read the node-capacity guest: %w", err)
	}

	// A hugepage pool takes no host reserve: the host's own memory lies outside it.
	zone.HugePageSize = guest.HugePageSize
	if guest.HugePageSize > 0 {
		zone.ReservedMemory = 0
	}

	if guest.Sockets != 0 {
		zone.NumSockets = guest.Sockets
	}

	if guest.Threads != 0 {
		zone.NumThreads = guest.Threads
	}

	if guest.UncoreCaches != 0 {
		zone.NumUncoreCaches = guest.UncoreCaches
	}

	switch {
	case len(guest.NUMANodes) != 0:
		zone.NUMANodes = guest.NUMANodes
	case !guest.CPUs.IsEmpty():
		zone.NUMANodes = settings.NUMANodes{
			0: {CPUs: guest.CPUs.String(), MemSize: guest.Memory},
		}
	}

	return nil
}

// applyFile applies the node settings file's entry for the zone: each field it
// sets wins, and NUMA nodes it sets replace the others whole.
func applyFile(zone *settings.NodeSettings, file *settings.NodeSettings) {
	if file.NumSockets != 0 {
		zone.NumSockets = file.NumSockets
	}

	if file.NumThreads != 0 {
		zone.NumThreads = file.NumThreads
	}

	if file.NumUncoreCaches != 0 {
		zone.NumUncoreCaches = file.NumUncoreCaches
	}

	if len(file.ReservedCPUs) != 0 {
		zone.ReservedCPUs = file.ReservedCPUs
	}

	if file.ReservedMemory != 0 {
		zone.ReservedMemory = file.ReservedMemory
	}

	if len(file.NUMANodes) != 0 {
		zone.NUMANodes = file.NUMANodes
	}

	if file.HugePageSize != 0 {
		zone.HugePageSize = file.HugePageSize
	}
}
