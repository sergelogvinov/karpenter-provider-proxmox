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

package settings

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadNodeSettingsFromFile loads node settings from a file.
func LoadNodeSettingsFromFile(name, region, zone string) (*NodeSettings, error) {
	if name == "" {
		return nil, nil
	}

	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read node settings file %s: %w", name, err)
	}

	config := NodeSettingsConfig{}

	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal node settings %s: %w", name, err)
	}

	if c, ok := config[region]; ok {
		if params, ok := c[zone]; ok {
			return &params, nil
		}

		if params, ok := c["*"]; ok {
			return &params, nil
		}
	}

	return nil, nil
}
