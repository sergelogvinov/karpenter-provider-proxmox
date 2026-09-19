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
	"os/exec"
	"strings"
)

// ClusterReady reports whether the local Proxmox cluster node currently has
// quorum, per `pvecm status`.
func ClusterReady(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "pvecm", "status")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to get proxmox cluster status: %w, output: %s", err, string(output))
	}

	return parseQuorate(string(output))
}

// parseQuorate extracts the "Quorate" value from `pvecm status` output.
func parseQuorate(output string) (bool, error) {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "Quorate") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		value := strings.TrimSpace(parts[1])

		return strings.EqualFold(value, "yes") || value == "1", nil
	}

	return false, fmt.Errorf("could not determine cluster quorum status from pvecm output")
}
