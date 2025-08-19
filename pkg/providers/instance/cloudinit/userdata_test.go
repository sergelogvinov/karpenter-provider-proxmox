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

package cloudinit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/cloudinit"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instance/provider"
)

const (
	Userdata = `#cloud-config
package_update: true
write_files:
  - path: /etc/hostname
    permissions: 0o600
    defer: true
    content: {{ .Hostname }}
  - path: /etc/kubernetes/kubelet.conf
    permissions: 0o600
    defer: true
    content: |
      {{- .KubeletConfiguration | toYamlPretty | nindent 6 }}
`
)

func TestUserData(t *testing.T) {
	assert := assert.New(t)

	region := "test-region"
	zone := "test-zone"
	metadata := struct {
		cloudinit.MetaData

		KubeletConfiguration *instance.KubeletConfiguration
	}{
		MetaData: cloudinit.MetaData{
			Hostname:     "hostname-1",
			InstanceID:   "100",
			InstanceType: "t1.2VCPU-6GB",
			ProviderID:   provider.GetProviderID(region, 100),
			Region:       region,
			Zone:         zone,
		},
		KubeletConfiguration: &instance.KubeletConfiguration{
			AllowedUnsafeSysctls:  []string{"kernel.msgmax", "kernel.shmmax"},
			TopologyManagerPolicy: "best-effort",
			ProviderID:            provider.GetProviderID(region, 100),
		},
	}

	tests := []struct {
		name     string
		template string
		values   interface{}
		result   string
	}{
		{
			name:     "Default",
			template: cloudinit.DefaultUserdata,
			values:   metadata,
			result:   "#cloud-config",
		},
		{
			name:     "CustomUserdata",
			template: Userdata,
			values:   metadata,
			result: `#cloud-config
package_update: true
write_files:
  - path: /etc/hostname
    permissions: 0o600
    defer: true
    content: hostname-1
  - path: /etc/kubernetes/kubelet.conf
    permissions: 0o600
    defer: true
    content: |
      topologyManagerPolicy: best-effort
      allowedUnsafeSysctls:
        - kernel.msgmax
        - kernel.shmmax
      providerID: proxmox://test-region/100
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := cloudinit.ExecuteTemplate(tt.template, tt.values)
			assert.NoError(err)
			assert.Equal(data, tt.result)
		})
	}
}
