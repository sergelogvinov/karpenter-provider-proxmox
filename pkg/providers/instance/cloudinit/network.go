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
	// DefaultNetworkV1 is default cloud-init network configuration version 1
	DefaultNetworkV1 = `version: 1
config:
{{- range $iface := .Interfaces }}
- type: physical
  name: {{ $iface.Name | quote }}
  mac_address: {{ $iface.MacAddr | quote }}
{{- if ne $iface.MTU 0 }}
  mtu: {{ $iface.MTU }}
{{- end }}
{{- if or $iface.DHCPv4 $iface.DHCPv6 $iface.Address4 $iface.Address6 }}
  subnets:
  {{- if $iface.DHCPv4 }}
  - type: dhcp
  {{- else if $iface.Address4 }}{{- range $iface.Address4 }}
  - type: static
    address: {{ . | quote }}
    gateway: {{ $iface.Gateway4 | quote }}
  {{- end }}{{- end }}
  {{- end }}
  {{- if $iface.DHCPv6 }}
  - type: dhcp6
  {{- else if $iface.Address6 }}{{- range $iface.Address6 }}
  - type: static6
    address: {{ . | quote }}
    gateway: {{ $iface.Gateway6 | quote }}
  {{- end }}{{- end }}
  {{- end }}
{{- if .NameServers }}
- type: nameserver
  address:
  {{- range .NameServers }}
  - {{ . | quote }}
  {{- end }}
  {{- if .SearchDomains }}
  search:
  {{- range .SearchDomains }}
  - {{ . | quote }}
  {{- end }}
  {{- end }}
{{- end }}
`

	// DefaultNetworkV2 is default cloud-init network configuration version 2
	DefaultNetworkV2 = `version: 2
config: []`
)
