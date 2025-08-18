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

type MetaData struct {
	Hostname     string
	InstanceID   string
	InstanceUUID string
	InstanceType string
	ProviderID   string
	Region       string
	Zone         string
}

type NetworkConfig struct {
	Interfaces    []InterfaceConfig
	SearchDomains []string `yaml:"search_domains,omitempty"`
	NameServers   []string `yaml:"name_servers,omitempty"`
}

type InterfaceConfig struct {
	Name     string   `yaml:"name"`
	MacAddr  string   `yaml:"mac_address,omitempty"`
	DHCPv4   bool     `yaml:"dhcp4,omitempty"`
	DHCPv6   bool     `yaml:"dhcp6,omitempty"`
	Address4 []string `yaml:"addresses4,omitempty"`
	Address6 []string `yaml:"addresses6,omitempty"`
	Gateway4 string   `yaml:"gateway4,omitempty"`
	Gateway6 string   `yaml:"gateway6,omitempty"`
	MTU      uint32   `yaml:"mtu,omitempty"`
}
