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

package options

import (
	"fmt"
	"math"
	"strconv"

	"go.uber.org/multierr"
)

// Validate checks required controller configuration and parses the CPU allocation ratio.
func (o *Options) Validate() error {
	return multierr.Combine(
		o.validateRequiredFields(),
		o.validateCPUOvercommitRatio(),
	)
}

// validateRequiredFields checks the cloud configuration and node policy are supplied.
func (o *Options) validateRequiredFields() error {
	if o.CloudConfigPath == "" {
		return fmt.Errorf("missing required flag %s or env var %s", cloudConfigFlagName, cloudConfigEnvVarName)
	}

	if o.NodePolicy == "" {
		return fmt.Errorf("node policy must be one of: static, simple, simple-with-cpu-overcommit")
	}

	return nil
}

// validateCPUOvercommitRatio requires explicit selection of the overcommit policy.
func (o *Options) validateCPUOvercommitRatio() error {
	ratio, err := strconv.ParseFloat(o.cpuOvercommitRatioRaw, 64)
	if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 1 {
		return fmt.Errorf("%s must be a finite number greater than or equal to 1", cpuOvercommitRatioFlagName)
	}
	if ratio != 1 && o.NodePolicy != "simple-with-cpu-overcommit" {
		return fmt.Errorf("%s greater than 1 requires the simple-with-cpu-overcommit node policy", cpuOvercommitRatioFlagName)
	}

	o.CPUOvercommitRatio = ratio

	return nil
}
