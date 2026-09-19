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

package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripPending(t *testing.T) {
	testCases := []struct {
		name     string
		data     string
		expected string
	}{
		{
			name:     "no pending section",
			data:     "name: test-vm\ncores: 4\n",
			expected: "name: test-vm\ncores: 4\n",
		},
		{
			name:     "pending section stripped",
			data:     "name: test-vm\ncores: 4\n[PENDING]\ncores: 8\n",
			expected: "name: test-vm\ncores: 4\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, string(stripPending([]byte(tc.data))))
		})
	}
}

func TestFilterChangedOptions(t *testing.T) {
	testCases := []struct {
		name     string
		current  map[string]any
		desired  map[string]any
		expected map[string]any
	}{
		{
			name:     "all unchanged",
			current:  map[string]any{"cores": 4, "name": "test-vm"},
			desired:  map[string]any{"cores": 4},
			expected: map[string]any{},
		},
		{
			name:     "changed value",
			current:  map[string]any{"cores": 4},
			desired:  map[string]any{"cores": 8},
			expected: map[string]any{"cores": 8},
		},
		{
			name:     "missing from current",
			current:  map[string]any{"cores": 4},
			desired:  map[string]any{"cores": 4, "tags": "karpenter"},
			expected: map[string]any{"tags": "karpenter"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, filterChangedOptions(tc.current, tc.desired))
		})
	}
}

func TestConfigMergeHostPCIs(t *testing.T) {
	cfg := &Config{
		HostPCI0: "0000:81:00.0,pcie=1",
		HostPCI2: "0000:82:00.0,pcie=1",
	}

	assert.Equal(t, map[string]string{
		"hostpci0": "0000:81:00.0,pcie=1",
		"hostpci2": "0000:82:00.0,pcie=1",
	}, cfg.MergeHostPCIs())

	assert.Empty(t, (&Config{}).MergeHostPCIs())
}

func withQemuServerDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	original := qemuServerDir
	qemuServerDir = dir

	t.Cleanup(func() {
		qemuServerDir = original
	})

	return dir
}

func writeVMConfig(t *testing.T, dir string, vmID int, content string) {
	t.Helper()

	path := filepath.Join(dir, filepath.Base(configPath(vmID)))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestGetVMConfig(t *testing.T) {
	dir := withQemuServerDir(t)

	writeVMConfig(t, dir, 100, "name: test-vm\ncores: 4\nmemory: 4096\ntags: karpenter;test\naffinity: 0-3\nhostpci0: 0000:81:00.0,pcie=1\n[PENDING]\ncores: 8\n")

	cfg, err := GetVMConfig(100)
	require.NoError(t, err)

	assert.Equal(t, "test-vm", cfg.Name)
	assert.Equal(t, 4, cfg.Cores)
	assert.Equal(t, 4096, cfg.Memory)
	assert.Equal(t, "karpenter;test", cfg.Tags)
	assert.Equal(t, "0-3", cfg.Affinity)
	assert.Equal(t, "0000:81:00.0,pcie=1", cfg.HostPCI0)

	_, err = GetVMConfig(999)
	require.Error(t, err)
}

func TestGetVMConfigByFilter(t *testing.T) {
	dir := withQemuServerDir(t)

	writeVMConfig(t, dir, 100, "name: other-vm\ntags: unrelated\n")
	writeVMConfig(t, dir, 101, "name: node-capacity\ntags: karpenter\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notavm.conf"), []byte("name: broken\n"), 0o600))

	filterByName := func(name string) func(*Config) (bool, error) {
		return func(c *Config) (bool, error) {
			return c.Name == name, nil
		}
	}

	vmID, cfg, err := GetVMConfigByFilter(filterByName("node-capacity"))
	require.NoError(t, err)
	assert.Equal(t, 101, vmID)
	assert.Equal(t, "node-capacity", cfg.Name)

	_, _, err = GetVMConfigByFilter(filterByName("does-not-exist"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVirtualMachineNotFound))

	vmID, _, err = GetVMConfigByFilter()
	require.NoError(t, err)
	assert.Contains(t, []int{100, 101}, vmID)
}
