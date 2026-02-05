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

package vmconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/vmconfig"
)

func TestParseVfioPciDevices(t *testing.T) {
	testCases := []struct {
		name     string
		cmdline  []string
		expected []vmconfig.VfioPciDevice
	}{
		{
			name:     "no args",
			cmdline:  []string{},
			expected: nil,
		},
		{
			name:     "no vfio-pci devices",
			cmdline:  []string{"-m", "2048", "-smp", "2"},
			expected: nil,
		},
		{
			name:    "single vfio-pci device",
			cmdline: []string{"-m", "2048", "-smp", "2", "-device", "vfio-pci,host=0000:81:00.3,id=hostpci0,bus=ich9-pcie-port-1,addr=0x0,rombar=0"},
			expected: []vmconfig.VfioPciDevice{
				{HostAddress: "0000:81:00.3", ID: "hostpci0"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			devices := vmconfig.ParseVfioPciDevices(tc.cmdline)
			assert.Equal(t, tc.expected, devices)
		})
	}
}
