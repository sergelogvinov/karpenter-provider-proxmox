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

// Config is the subset of a Proxmox QEMU guest's raw configuration file
// (/etc/pve/qemu-server/<vmid>.conf) this repo's scheduler reads and writes
// directly on the hypervisor. It intentionally carries only the fields
// actually consumed here, not a full mirror of every PVE config key.
type Config struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tags        string `yaml:"tags"`
	Affinity    string `yaml:"affinity"`
	Cores       int    `yaml:"cores"`
	Memory      int    `yaml:"memory"`

	HostPCI0 string `yaml:"hostpci0"`
	HostPCI1 string `yaml:"hostpci1"`
	HostPCI2 string `yaml:"hostpci2"`
	HostPCI3 string `yaml:"hostpci3"`
	HostPCI4 string `yaml:"hostpci4"`
	HostPCI5 string `yaml:"hostpci5"`
	HostPCI6 string `yaml:"hostpci6"`
	HostPCI7 string `yaml:"hostpci7"`
	HostPCI8 string `yaml:"hostpci8"`
	HostPCI9 string `yaml:"hostpci9"`
}

// MergeHostPCIs returns the non-empty hostpciN entries of this config, keyed
// by their config key (e.g. "hostpci0").
func (c *Config) MergeHostPCIs() map[string]string {
	all := map[string]string{
		"hostpci0": c.HostPCI0,
		"hostpci1": c.HostPCI1,
		"hostpci2": c.HostPCI2,
		"hostpci3": c.HostPCI3,
		"hostpci4": c.HostPCI4,
		"hostpci5": c.HostPCI5,
		"hostpci6": c.HostPCI6,
		"hostpci7": c.HostPCI7,
		"hostpci8": c.HostPCI8,
		"hostpci9": c.HostPCI9,
	}

	devices := make(map[string]string, len(all))

	for key, value := range all {
		if value != "" {
			devices[key] = value
		}
	}

	return devices
}
