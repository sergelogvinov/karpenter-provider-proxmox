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

package goproxmox

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/luthermonson/go-proxmox"
)

type VMCloneRequest struct {
	Node        string `json:"node"`
	NewID       int    `json:"newid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Full        uint8  `json:"full,omitempty"`
	Storage     string `json:"storage,omitempty"`

	CPU          int    `json:"cpu,omitempty"`
	Memory       uint32 `json:"memory,omitempty"`
	DiskSize     string `json:"diskSize,omitempty"`
	Tags         string `json:"tags,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
}

type VMCPU struct {
	Flags *[]string `json:"flags,omitempty"`
	Type  string    `json:"cputype,omitempty"`
}

func (r *VMCPU) UnmarshalString(s string) error {
	return unmarshal(s, r)
}

func (r *VMCPU) ToString() (string, error) {
	return marshal(r)
}

type VMSMBIOS struct {
	Base64       *proxmox.IntOrBool `json:"base64,omitempty" `
	Family       string             `json:"family,omitempty"`
	Manufacturer string             `json:"manufacturer,omitempty"`
	Product      string             `json:"product,omitempty"`
	Serial       string             `json:"serial,omitempty"`
	SKU          string             `json:"sku,omitempty"`
	UUID         string             `json:"uuid,omitempty"`
	Version      string             `json:"version,omitempty"`
}

func (r *VMSMBIOS) UnmarshalString(s string) error {
	return unmarshal(s, r)
}

func (r *VMSMBIOS) ToString() (string, error) {
	return marshal(r)
}

type VMNetworkDevice struct {
	Virtio     string             `json:"virtio,omitempty"`
	Bridge     string             `json:"bridge,omitempty"`
	Firewall   *proxmox.IntOrBool `json:"firewall,omitempty"`
	LinkDown   *proxmox.IntOrBool `json:"link_down,omitempty"`
	MACAddress string             `json:"macaddr,omitempty"`
	MTU        *int               `json:"mtu,omitempty"`
	Model      string             `json:"model"`
	Queues     *int               `json:"queues,omitempty"`
	Tag        *int               `json:"tag,omitempty"`
	Trunks     []int              `json:"trunks,omitempty"`
}

func (r *VMNetworkDevice) UnmarshalString(s string) error {
	return unmarshal(s, r)
}

func (r *VMNetworkDevice) ToString() (string, error) {
	return marshal(r)
}

type VMCloudInitIPConfig struct {
	GatewayIPv4 string `json:"gw,omitempty"`
	GatewayIPv6 string `json:"gw6,omitempty"`
	IPv4        string `json:"ip,omitempty"`
	IPv6        string `json:"ip6,omitempty"`
}

func (r *VMCloudInitIPConfig) UnmarshalString(s string) error {
	return unmarshal(s, r)
}

func NewIntOrBool(b bool) *proxmox.IntOrBool {
	res := proxmox.IntOrBool(b)

	return &res
}

func marshal(v interface{}) (string, error) {
	values := []string{}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	for i := range rv.NumField() {
		f := rv.Field(i)

		if !f.CanInterface() {
			continue
		}

		tag := rv.Type().Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}

		fieldName := strings.Split(tag, ",")[0]
		fieldValue := ""

		if f.IsValid() {
			switch f.Kind() { //nolint:exhaustive
			case reflect.Bool:
				fieldValue = fmt.Sprintf("%t", f.Bool())
			case reflect.String:
				fieldValue = strings.TrimSpace(f.String())
				if fieldValue == "" {
					continue
				}
			case reflect.Slice:
				if f.Len() == 0 {
					continue
				}

				switch f.Type().Elem().Kind() { //nolint:exhaustive
				case reflect.String:
					for i, v := range f.Interface().([]string) {
						if i > 0 {
							fieldValue += ";"
						}

						fieldValue += v
					}
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					for i := range f.Len() {
						if i > 0 {
							fieldValue += ";"
						}

						fieldValue += fmt.Sprintf("%d", f.Index(i).Int())
					}
				default:
					return "", fmt.Errorf("unsupported slice type %s", f.Kind())
				}
			case reflect.Ptr:
				if f.IsNil() {
					continue
				}

				switch f.Type().Elem().Kind() { //nolint:exhaustive
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					fieldValue = fmt.Sprintf("%d", f.Elem().Int())
				case reflect.Bool:
					fieldValue = "0"

					if f.Elem().Bool() {
						fieldValue = "1"
					}
				default:
					return "", fmt.Errorf("unsupported pointer type %s", f.Type().Elem().Kind())
				}

			default:
				return "", fmt.Errorf("unsupported field %s: %s", fieldName, f.Kind())
			}
		}

		values = append(values, fmt.Sprintf("%s=%v", fieldName, fieldValue))
	}

	return strings.Join(values, ","), nil
}

func unmarshal(s string, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("unmarshal expects a non-nil pointer")
	}

	ps := rv.Elem()
	if ps.Kind() != reflect.Struct {
		return fmt.Errorf("unmarshal expects a struct")
	}

	psCount := ps.NumField()

	pairs := strings.Split(s, ",")
	for _, p := range pairs {
		v := strings.Split(strings.TrimSpace(p), "=")

		if len(v) == 2 {
			for i := range psCount {
				f := ps.Field(i)
				if !f.CanInterface() {
					continue
				}

				tag := ps.Type().Field(i).Tag.Get("json")
				if tag == "" || tag == "-" {
					continue
				}

				fieldName := strings.ToLower(strings.Split(tag, ",")[0])
				if strings.EqualFold(fieldName, v[0]) {
					if f.IsValid() {
						switch f.Kind() { //nolint:exhaustive
						case reflect.Bool:
							f.SetBool(v[1] == "true")
						case reflect.String:
							f.SetString(strings.TrimSpace(v[1]))
						case reflect.Ptr:
							switch f.Type().Elem().Kind() { //nolint:exhaustive
							case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
								var intValue int

								_, err := fmt.Sscanf(v[1], "%d", &intValue)
								if err != nil {
									return fmt.Errorf("failed to parse int value: %w", err)
								}

								x := reflect.New(f.Type().Elem())
								x.Elem().SetInt(int64(intValue))
								f.Set(x)
							case reflect.Bool:
								boolValue := v[1] == "true" || v[1] == "1"

								x := reflect.New(f.Type().Elem())
								x.Elem().SetBool(boolValue)
								f.Set(x)
							default:
								return fmt.Errorf("unsupported pointer type %s", f.Type().Elem().Kind())
							}

						default:
							return fmt.Errorf("unsupported field %s: %s", v[0], f.Kind())
						}
					}
				}
			}
		}
	}

	return nil
}
