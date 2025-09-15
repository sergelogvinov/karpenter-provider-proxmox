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

package ip

import (
	"fmt"
	"net"

	gocidr "github.com/apparentlymart/go-cidr/cidr"
)

// CIDRHost returns the IP address of the given host number in the given CIDR.
func CIDRHost(cidr string, hostnum ...int) (string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}

	if len(hostnum) == 0 {
		return ip.String(), nil
	}

	ip, err = gocidr.Host(ipnet, hostnum[0])
	if err != nil {
		return "", err
	}

	return ip.String(), nil
}

// Slaac returns the SLAAC address for the given MAC address in the given IPv6 CIDR.
func Slaac(mac string, cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}

	hw, err := net.ParseMAC(mac)
	if err != nil {
		return "", err
	}

	ones, _ := ipnet.Mask.Size()
	if ones > 112 {
		return "", fmt.Errorf("slaac generator requires a mask of /64 to /112")
	}

	eui64 := net.IPv6zero
	copy(eui64, ipnet.IP.To16())

	copy(eui64[8:11], hw[0:3])
	copy(eui64[13:16], hw[3:6])
	eui64[11] = 0xFF
	eui64[12] = 0xFE
	eui64[8] ^= 0x02

	l := ones / 8
	for i := 15; i >= l; i-- {
		ipnet.IP[i] = eui64[i]
	}

	return ipnet.String(), nil
}
