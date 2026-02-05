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

package vmconfig

import (
	"strings"
)

// VfioPciDevice represents a vfio-pci device found in cmdline
type VfioPciDevice struct {
	ID          string // hostpci ID (e.g., "hostpci0")
	HostAddress string // PCI host address (e.g., "0000:81:00.3")
}

// ParseVfioPciDevices parses cmdline arguments to find vfio-pci devices
// and extract their host addresses
// Example: "-device vfio-pci,host=0000:81:00.3,id=hostpci0"
func ParseVfioPciDevices(cmdlineArgs []string) []VfioPciDevice {
	var devices []VfioPciDevice

	for _, arg := range cmdlineArgs {
		if strings.Contains(arg, "vfio-pci") && strings.Contains(arg, "host=") && strings.Contains(arg, "id=") {
			var hostAddr, deviceID string

			for param := range strings.SplitSeq(arg, ",") {
				if v, ok := strings.CutPrefix(param, "host="); ok {
					hostAddr = v
				}

				if v, ok := strings.CutPrefix(param, "id="); ok {
					deviceID = v
				}

				if hostAddr != "" && deviceID != "" {
					break
				}
			}

			if hostAddr != "" && deviceID != "" {
				device := VfioPciDevice{
					ID:          deviceID,
					HostAddress: hostAddr,
				}

				devices = append(devices, device)
			}
		}
	}

	return devices
}
