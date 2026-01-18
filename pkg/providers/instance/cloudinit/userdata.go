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

const (
	DefaultUserdata = `#cloud-config
hostname: {{ .Metadata.Hostname }}
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
    {{- with get .Values "SSHAuthorizedKeys" }}
    ssh_authorized_keys:
      {{- toYaml (split . ",") | nindent 6 }}
    {{- end }}

write_files:
  {{- with .Resources.Hugepages2Mi }}
  - path: /etc/sysctl.d/99-hugepages.conf
    content: |
      vm.nr_hugepages = {{ . }}
  {{- end }}
  - path: /etc/karpenter.yaml
    content: |
      {{- . | toYamlPretty | nindent 6  }}
    owner: root:root
`
)
