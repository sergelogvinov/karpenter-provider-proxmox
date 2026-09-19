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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQuorate(t *testing.T) {
	testCases := []struct {
		name      string
		output    string
		expected  bool
		expectErr bool
	}{
		{
			name: "quorate yes",
			output: "Cluster information\n" +
				"-------------------\n" +
				"Name:             mycluster\n\n" +
				"Quorum information\n" +
				"------------------\n" +
				"Quorate:               Yes\n",
			expected: true,
		},
		{
			name:     "quorate numeric one",
			output:   "Quorate:                  1\n",
			expected: true,
		},
		{
			name:     "not quorate",
			output:   "Quorate:               No\n",
			expected: false,
		},
		{
			name:     "not quorate numeric zero",
			output:   "Quorate:               0\n",
			expected: false,
		},
		{
			name:      "no quorate line",
			output:    "Cluster information\n-------------------\nName:             mycluster\n",
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ready, err := parseQuorate(tc.output)

			if tc.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expected, ready)
		})
	}
}
