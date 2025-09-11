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

package cloudinit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFuncs(t *testing.T) {
	tests := []struct {
		name   string
		tpl    string
		expect string
	}{
		{
			name:   "default",
			tpl:    `ipv6: {{ "2001:db8:1::/64" | cidrslaac "00:1A:2B:3C:4D:5E" }}`,
			expect: `ipv6: 2001:db8:1:0:21a:2bff:fe3c:4d5e/64`,
		},
		{
			name:   "mask-80",
			tpl:    `ipv6: {{ "2001:db8:1:2:3::/80" | cidrslaac "00:1A:2B:3C:4D:5E" }}`,
			expect: `ipv6: 2001:db8:1:2:3:2bff:fe3c:4d5e/80`,
		},
		{
			name:   "mask-88",
			tpl:    `ipv6: {{ "2001:db8:1:2:3::/88" | cidrslaac "00:1A:2B:3C:4D:5E" }}`,
			expect: `ipv6: 2001:db8:1:2:3:ff:fe3c:4d5e/88`,
		},
		{
			name:   "mask-112",
			tpl:    `ipv6: {{ "2001:db8:1:2:3::/112" | cidrslaac "00:1A:2B:3C:4D:5E" }}`,
			expect: `ipv6: 2001:db8:1:2:3::4d5e/112`,
		},
		{
			name:   "cidrhost",
			tpl:    `ipv4: {{ "192.168.1.5/24" | cidrhost }}`,
			expect: `ipv4: 192.168.1.5`,
		},
		{
			name:   "cidrhost with host number",
			tpl:    `ipv4: {{ cidrhost "192.168.1.5/24" 15 }}`,
			expect: `ipv4: 192.168.1.15`,
		},
	}

	for _, tt := range tests {
		res, err := ExecuteTemplate(tt.tpl, nil)

		assert.NoError(t, err)
		assert.Equal(t, tt.expect, res, tt.name)
	}
}
