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
  name: {{ $iface.Name }}
  mac_address: {{ $iface.MacAddr | lower | quote }}
{{- if ne $iface.MTU 0 }}
  mtu: {{ $iface.MTU }}
{{- end }}
  subnets:
{{- if or $iface.DHCPv4 $iface.DHCPv6 $iface.Address4 $iface.Address6 }}
  {{- if $iface.DHCPv4 }}
  - type: dhcp
  {{- else if $iface.Address4 }}{{- range $iface.Address4 }}
  - type: static
    address: {{ . | quote }}
    {{- if $iface.Gateway4 }}
    gateway: {{ $iface.Gateway4 | quote }}
    {{- end }}
  {{- end }}{{- end }}
  {{- end }}
  {{- if $iface.DHCPv6 }}
  - type: dhcp6
  {{- else if and $iface.Address6 $iface.Gateway6 }}{{- range $iface.Address6 }}
  - type: static6
    address: {{ . | quote }}
    {{- if $iface.Gateway6 }}
    gateway: {{ $iface.Gateway6 | quote }}
    {{- end }}
  {{- end }}
  {{- else if $iface.SLAAC }}{{- if $iface.NodeAddress6 }}
  - type: static6
    address: {{ $iface.NodeAddress6 | cidrslaac $iface.MacAddr | quote }}
    gateway: {{ $iface.NodeAddress6 | cidrhost | quote }}
  {{- else }}
  - type: ipv6_slaac
  {{- end }}{{- end }}
  {{- end }}
{{- if .NameServers }}
- type: nameserver
  address:
  {{- range .NameServers }}
  - {{ . | quote }}
  {{- end }}
  {{- with .SearchDomains }}
  search: {{- . | toYaml | nindent 2 }}
  {{- end }}
{{- end }}
`

	// DefaultNetworkV2 is default cloud-init network configuration version 2
	DefaultNetworkV2 = `network:
  version: 2
  renderer: networkd
  ethernets:
{{- range $iface := .Interfaces }}
    {{ $iface.Name }}:
      match:
        macaddress: {{ $iface.MacAddr | lower | quote }}
{{- if ne $iface.MTU 0 }}
      mtu: {{ $iface.MTU }}
{{- end }}
{{- if $iface.DHCPv4 }}
      dhcp4: {{ $iface.DHCPv4 }}
{{- end }}
{{- if $iface.DHCPv6 }}
      dhcp6: {{ $iface.DHCPv6 }}
{{- end }}
{{- if or $iface.Address4 $iface.Address6 }}
      addresses:
{{- range $iface.Address4 }}
      - {{ . | quote }}
{{- end }}
{{- range $iface.Address6 }}
      - {{ . | quote }}
{{- end }}
{{- if or $iface.Gateway4 $iface.Gateway6 }}
      routes:
{{- if $iface.Gateway4 }}
      - to: default
        via: {{ $iface.Gateway4 | quote }}
{{- end }}
{{- if $iface.Gateway6 }}
      - to: default
        via: {{ $iface.Gateway6 | quote }}
{{- end }}
{{- end }}
{{- else if $iface.SLAAC }}{{- if $iface.NodeAddress6 }}
      addresses:
      - {{ $iface.NodeAddress6 | cidrslaac $iface.MacAddr | quote }}
      routes:
      - to: default
        via: {{ $iface.NodeAddress6 | cidrhost | quote }}
  {{- end }}
{{- end }}
{{- if or $.NameServers $.SearchDomains }}
      nameservers:
{{- if $.NameServers }}
        addresses:
{{- range $.NameServers }}
        - {{ . | quote }}
{{- end }}{{- end }}
{{- if $.SearchDomains }}
        search:
{{- range $.SearchDomains }}
        - {{ . | quote }}
{{- end }}{{- end }}
{{- end }}
{{- end }}
`
)
