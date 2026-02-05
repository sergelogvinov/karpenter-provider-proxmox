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

package sys

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func GetPidFromFile(filePath string) (int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}

	pidStr := strings.TrimSpace(string(data))

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, err
	}

	if pid <= 0 {
		return 0, fmt.Errorf("invalid PID %d in file %s", pid, filePath)
	}

	return pid, nil
}

func ProcessExists(pid int) bool {
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
		return false
	}

	if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err != nil {
		return false
	}

	return true
}

func GetProcessCmdline(pid int) ([]string, error) {
	cmdlineFile := fmt.Sprintf("/proc/%d/cmdline", pid)
	data, err := os.ReadFile(cmdlineFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read cmdline for PID %d: %w", pid, err)
	}

	args := strings.Split(string(data), "\x00")
	if len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}

	return args, nil
}

// GetProcessThreads returns a list of thread IDs for the given process
// If filter is not empty, only threads with matching comm name are returned
func GetProcessThreads(pid int, filter string) ([]int, error) {
	taskDir := fmt.Sprintf("/proc/%d/task", pid)

	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read task directory %s: %w", taskDir, err)
	}

	var threads []int

	for _, entry := range entries {
		if entry.IsDir() {
			if tid, err := strconv.Atoi(entry.Name()); err == nil {
				if tid <= 0 {
					continue
				}

				if filter != "" {
					commFile := fmt.Sprintf("/proc/%d/task/%d/comm", pid, tid)
					commData, commErr := os.ReadFile(commFile)
					if commErr != nil {
						continue // Skip if can't read comm file
					}

					commName := strings.TrimSpace(string(commData))
					if !strings.Contains(commName, filter) {
						continue // Skip if name doesn't match filter
					}
				}

				threads = append(threads, tid)
			}
		}
	}

	return threads, nil
}
