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

package main

import "strings"

const (
	// FeatureKarpenter enables integration with Karpenter
	FeatureKarpenter = "karpenter"
)

// FeatureFlags represents enabled feature flags
type FeatureFlags map[string]bool

// IsEnabled checks if a feature is enabled
func (f FeatureFlags) IsEnabled(feature string) bool {
	return f[feature]
}

func parseFeatureFlags(flagsStr string) FeatureFlags {
	flags := make(FeatureFlags)
	if flagsStr == "" {
		return flags
	}

	for flag := range strings.SplitSeq(flagsStr, ",") {
		flag = strings.TrimSpace(flag)
		if flag != "" {
			flags[flag] = true
		}
	}

	return flags
}
