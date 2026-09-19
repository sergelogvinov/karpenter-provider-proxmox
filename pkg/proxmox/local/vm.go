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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// qemuServerDir is the directory PVE stores per-VM QEMU config files in.
// It is a var (not a const) so tests can point it at a temp directory.
var qemuServerDir = "/etc/pve/qemu-server"

// configPath returns the on-disk config file path for the given VM ID.
func configPath(vmID int) string {
	return filepath.Join(qemuServerDir, fmt.Sprintf("%d.conf", vmID))
}

// stripPending drops PVE's "[PENDING]" section (uncommitted config changes)
// from raw config file contents, keeping only the active configuration.
func stripPending(data []byte) []byte {
	if idx := strings.Index(string(data), "[PENDING]"); idx != -1 {
		return data[:idx]
	}

	return data
}

// GetVMConfig reads and parses the QEMU guest config file for the given VM
// ID directly from qemuServerDir on the local hypervisor.
func GetVMConfig(vmID int) (*Config, error) {
	path := configPath(vmID)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read VM config file %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(stripPending(data), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse VM config for VM %d: %w", vmID, err)
	}

	return cfg, nil
}

// GetVMConfigByFilter scans every QEMU guest config file under
// qemuServerDir and returns the ID/config of the first VM matched by any of
// the supplied filter functions. With no filters, it returns the first VM
// found. It returns ErrVirtualMachineNotFound if nothing matches.
func GetVMConfigByFilter(filters ...func(*Config) (bool, error)) (int, *Config, error) {
	entries, err := os.ReadDir(qemuServerDir)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read qemu-server directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		vmIDStr, ok := strings.CutSuffix(entry.Name(), ".conf")
		if !ok {
			continue
		}

		vmID, err := strconv.Atoi(vmIDStr)
		if err != nil {
			continue // Skip non-numeric filenames
		}

		cfg, err := GetVMConfig(vmID)
		if err != nil {
			continue // Skip VMs that can't be read
		}

		if len(filters) == 0 {
			return vmID, cfg, nil
		}

		for _, filter := range filters {
			match, err := filter(cfg)
			if err != nil {
				return 0, nil, fmt.Errorf("filter function error for VM %d: %w", vmID, err)
			}

			if match {
				return vmID, cfg, nil
			}
		}
	}

	return 0, nil, ErrVirtualMachineNotFound
}

// GetNextID asks the local hypervisor for the next available VM/CT ID in
// the cluster, via `pvesh get /cluster/nextid`.
func GetNextID(ctx context.Context) (int, error) {
	cmd := exec.CommandContext(ctx, "pvesh", "get", "/cluster/nextid")

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get next VM ID: %w", err)
	}

	vmID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse next VM ID %q: %w", string(output), err)
	}

	return vmID, nil
}

// commandArgs builds the `qm <action> <vmID> --key value ...` argument list
// for the given options.
func commandArgs(action string, vmID int, options map[string]any) []string {
	args := make([]string, 0, 2+2*len(options))
	args = append(args, action, strconv.Itoa(vmID))

	for key, value := range options {
		args = append(args, "--"+key, fmt.Sprintf("%v", value))
	}

	return args
}

// CreateVM creates a new QEMU guest with the given ID and options directly
// via the local `qm create` CLI.
func CreateVM(ctx context.Context, vmID int, options map[string]any) error {
	cmd := exec.CommandContext(ctx, "qm", commandArgs("create", vmID, options)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create VM %d: %w, output: %s", vmID, err, string(output))
	}

	return nil
}

// DeleteVM destroys the QEMU guest with the given ID via the local
// `qm destroy` CLI.
func DeleteVM(ctx context.Context, vmID int) error {
	cmd := exec.CommandContext(ctx, "qm", "destroy", strconv.Itoa(vmID))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete VM %d: %w, output: %s", vmID, err, string(output))
	}

	return nil
}

// UpdateVM applies options to an existing QEMU guest via the local `qm set`
// CLI, skipping any option whose value already matches the current on-disk
// config.
func UpdateVM(ctx context.Context, vmID int, options map[string]any) error {
	path := configPath(vmID)

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read VM config file %s: %w", path, err)
	}

	current := map[string]any{}
	if err := yaml.Unmarshal(stripPending(data), current); err != nil {
		return fmt.Errorf("failed to parse VM config for VM %d: %w", vmID, err)
	}

	changed := filterChangedOptions(current, options)
	if len(changed) == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, "qm", commandArgs("set", vmID, changed)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update VM %d: %w, output: %s", vmID, err, string(output))
	}

	return nil
}

// filterChangedOptions returns the subset of desired whose value differs
// from (or is absent from) current.
func filterChangedOptions(current, desired map[string]any) map[string]any {
	changed := make(map[string]any, len(desired))

	for key, value := range desired {
		if v, ok := current[key]; ok && v == value {
			continue
		}

		changed[key] = value
	}

	return changed
}
