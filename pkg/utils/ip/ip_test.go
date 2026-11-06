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

package ip

import (
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlaac(t *testing.T) {
	tests := []struct {
		mac      string
		cidr     string
		expected string
	}{
		{
			mac:      "00:1A:2B:3C:4D:5E",
			cidr:     "2001:db8:1::/64",
			expected: "2001:db8:1:0:21a:2bff:fe3c:4d5e/64",
		},
		{
			mac:      "bc:24:11:00:00:01",
			cidr:     "2001:db8:2::/64",
			expected: "2001:db8:2:0:be24:11ff:fe00:1/64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			res, err := Slaac(tt.mac, tt.cidr)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, res)
		})
	}

	assert.Equal(t, net.IP(make([]byte, net.IPv6len)), net.IPv6zero, "Slaac must not modify net.IPv6zero")
}

func TestSlaacConcurrent(t *testing.T) {
	macs := []string{"bc:24:11:00:00:01", "bc:24:11:ff:ff:ff"}

	expected := make([]string, len(macs))
	for i, mac := range macs {
		res, err := Slaac(mac, "2001:db8:1::/64")
		require.NoError(t, err)

		expected[i] = res
	}

	var wg sync.WaitGroup

	for range 50 {
		for i, mac := range macs {
			wg.Go(func() {
				res, err := Slaac(mac, "2001:db8:1::/64")
				assert.NoError(t, err)
				assert.Equal(t, expected[i], res)
			})
		}
	}

	wg.Wait()
}
