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
    content: {{ .Metadata.Hostname }}
  - path: /etc/kubernetes/kubelet.conf
    permissions: 0o600
    defer: true
    content: |
      {{- .KubeletConfiguration | toYamlPretty | nindent 6 }}
{{- if hasTag .Metadata.Tags "tag2" }}
  - path: /etc/kubernetes/kubelet-labels.conf
    permissions: 0o600
    defer: true
    content: |
      {{- join .Metadata.Tags "," | nindent 6 }}
{{- end }}
`
)

func TestUserData(t *testing.T) {
	assert := assert.New(t)

	region := "test-region"
	zone := "test-zone"
	data := struct {
		Metadata             cloudinit.MetaData
		KubeletConfiguration *instance.KubeletConfiguration
		Values               map[string]string
	}{
		Metadata: cloudinit.MetaData{
			Hostname:     "hostname-1",
			InstanceID:   "100",
			InstanceType: "t1.2VCPU-6GB",
			ProviderID:   provider.GetProviderID(region, 100),
			Region:       region,
			Zone:         zone,
			Tags:         []string{"tag1", "tag2"},
			NodeClass:    "node-class-1",
		},
		KubeletConfiguration: &instance.KubeletConfiguration{
			AllowedUnsafeSysctls:  []string{"kernel.msgmax", "kernel.shmmax"},
			TopologyManagerPolicy: "best-effort",
			ProviderID:            provider.GetProviderID(region, 100),
		},
		Values: map[string]string{
			"SSHAuthorizedKeys": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCu...,ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDk...",
		},
	}

	tests := []struct {
		name     string
		template string
		values   any
		result   string
	}{
		{
			name:     "Default",
			template: cloudinit.DefaultUserdata,
			values:   data,
			result: `#cloud-config
hostname: hostname-1
manage_etc_hosts: true
package_update: true
packages:
  - qemu-guest-agent
runcmd:
  - [ systemctl, enable, --now, qemu-guest-agent.service ]

users:
  - name: karpenter
    gecos: Kubernetes User
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [users]
    shell: /bin/bash
    ssh_authorized_keys:
      - ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCu...
      - ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDk...

write_files:
  - path: /etc/karpenter.yaml
    content: |
      metadata:
        hostname: hostname-1
        instanceid: "100"
        instanceuuid: ""
        instancetype: t1.2VCPU-6GB
        providerid: proxmox://test-region/100
        region: test-region
        zone: test-zone
        tags:
          - tag1
          - tag2
        nodeclass: node-class-1
      kubeletconfiguration:
        topologyManagerPolicy: best-effort
        allowedUnsafeSysctls:
          - kernel.msgmax
          - kernel.shmmax
        providerID: proxmox://test-region/100
      values:
        SSHAuthorizedKeys: ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCu...,ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDk...
    owner: root:root
`,
		},
		{
			name:     "CustomUserdata",
			template: Userdata,
			values:   data,
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
  - path: /etc/kubernetes/kubelet-labels.conf
    permissions: 0o600
    defer: true
    content: |
      tag1,tag2
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
