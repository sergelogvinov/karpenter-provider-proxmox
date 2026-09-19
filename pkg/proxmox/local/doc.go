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

// Package local provides direct, non-HTTP access to Proxmox QEMU guest
// configuration and lifecycle operations on the local hypervisor.
//
// Everything in this package shells out to pvecm/pvesh/qm and reads
// /etc/pve/qemu-server/*.conf directly, instead of calling the Proxmox REST
// API. It is used by cmd/proxmox-scheduler, which runs on the hypervisor
// itself and needs this information before (or without) a round trip to the
// cluster's API endpoint.
package local
