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

package instancetype

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseInstanceTypeNameRoundTrips checks that parseInstanceTypeName reads back what instanceTypeName makes.
func TestParseInstanceTypeNameRoundTrips(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		family        string
		cpu, memoryGB int
	}{
		{"n1", 30, 126},
		{"n4", 1, 1},
		{"e2", 2, 4},
		{"gpu-x1", 64, 512},
	} {
		name := instanceTypeName(tc.family, tc.cpu, tc.memoryGB)

		family, cpu, memoryGB, ok := parseInstanceTypeName(name)
		require.True(t, ok, name)
		require.Equal(t, tc.family, family, name)
		require.Equal(t, tc.cpu, cpu, name)
		require.Equal(t, tc.memoryGB, memoryGB, name)
	}
}

// TestParseInstanceTypeNameRejectsOtherNames checks that only names instanceTypeName makes, with at least one vCPU
// and one GB, are parsed.
func TestParseInstanceTypeNameRejectsOtherNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"n1",
		"n1.large",
		".30VCPU-126GB",
		"n1.030VCPU-126GB",
		"n1.+30VCPU-126GB",
		"n1.30VCPU-126GBx",
		"n1.30VCPU-126GB.x",
		"n1.0VCPU-126GB",
		"n1.30VCPU-0GB",
		"n1.-2VCPU-126GB",
	} {
		_, _, _, ok := parseInstanceTypeName(name)
		require.False(t, ok, name)
	}
}
