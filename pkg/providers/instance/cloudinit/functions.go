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

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"text/template"

	gocidr "github.com/apparentlymart/go-cidr/cidr"

	goYaml "sigs.k8s.io/yaml/goyaml.v3"
)

var genericMap = map[string]interface{}{
	"default":      defaultFunc,
	"coalesce":     coalesce,
	"ternary":      ternary,
	"toJson":       toJson,
	"toPrettyJson": toPrettyJson,
	"toYaml":       toYaml,
	"toYamlPretty": toYamlPretty,

	// String functions:
	"indent":     indent,
	"nindent":    nindent,
	"quote":      quote,
	"upper":      strings.ToUpper,
	"lower":      strings.ToLower,
	"trim":       strings.TrimSpace,
	"trimSuffix": func(a, b string) string { return strings.TrimSuffix(b, a) },
	"trimPrefix": func(a, b string) string { return strings.TrimPrefix(b, a) },

	"replace":         func(o, n, s string) string { return strings.ReplaceAll(s, o, n) },
	"regexFind":       regexFind,
	"regexFindString": regexFindString,
	"regexReplaceAll": regexReplaceAll,

	// String slice functions:
	"get":      get,
	"getValue": getValue,

	// Encoding functions:
	"b64enc": base64encode,
	"b64dec": base64decode,

	// Flow Control functions:
	"empty":     empty,
	"contains":  func(substr string, str string) bool { return strings.Contains(str, substr) },
	"hasPrefix": func(substr string, str string) bool { return strings.HasPrefix(str, substr) },
	"hasSuffix": func(substr string, str string) bool { return strings.HasSuffix(str, substr) },
	"hasKey":    hasKey,

	// Network functions:
	"cidrhost":  cidrhost, // cidrhost "10.12.112.0/20" 16 -> 10.12.112.16
	"cidrslaac": slaac,    // "2001:db8:1::/64" | slaac "00:1A:2B:3C:4D:5E" -> 2001:db8:1::21a:2bff:fe3c:4d5e
}

// ExecuteTemplate executes a template with the given data.
func ExecuteTemplate(tmpl string, data interface{}) (string, error) {
	t, err := template.New("cloudinit").Funcs(genericFuncMap()).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse template %q: %w", tmpl, err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// genericFuncMap returns a copy of the basic function map as a map[string]interface{}.
func genericFuncMap() map[string]interface{} {
	gfm := make(map[string]interface{}, len(genericMap))
	for k, v := range genericMap {
		gfm[k] = v
	}

	return gfm
}

// Source from https://github.com/Masterminds/sprig/blob/master/defaults.go with some modifications
//
// Checks whether `given` is set, and returns default if not set.
func defaultFunc(d any, given ...any) any {
	if empty(given) || empty(given[0]) {
		return d
	}

	return given[0]
}

func strval(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// empty returns true if the given value has the zero value for its type.
func empty(given any) bool {
	g := reflect.ValueOf(given)

	return !g.IsValid() || g.IsZero()
}

// coalesce returns the first non-empty value.
func coalesce(v ...any) any {
	for _, val := range v {
		if !empty(val) {
			return val
		}
	}

	return nil
}

// ternary returns the first value if the last value is true, otherwise returns the second value.
func ternary(vt any, vf any, v bool) any {
	if v {
		return vt
	}

	return vf
}

// toJson returns the JSON encoding of the given value.
func toJson(v any) string {
	output, _ := json.Marshal(v) //nolint: errchkjson

	return string(output)
}

// toPrettyJson returns the pretty-printed JSON encoding of the given value.
func toPrettyJson(v any) string {
	output, _ := json.MarshalIndent(v, "", "  ") //nolint: errchkjson

	return string(output)
}

// toYaml returns the YAML encoding of the given value.
func toYaml(v any) string {
	var output bytes.Buffer

	encoder := goYaml.NewEncoder(&output)
	encoder.Encode(v)

	return strings.TrimSuffix(output.String(), "\n")
}

// toYamlPretty returns the pretty-printed YAML encoding of the given value.
func toYamlPretty(v any) string {
	var output bytes.Buffer

	encoder := goYaml.NewEncoder(&output)
	encoder.SetIndent(2)
	encoder.Encode(v)

	return strings.TrimSuffix(output.String(), "\n")
}

// hasKey returns true if the given map has the given key.
func hasKey(m map[string]interface{}, key string) bool {
	if empty(m) {
		return false
	}

	_, ok := m[key]

	return ok
}

// quote returns a string representation of the given values, quoted.
func quote(str ...any) string {
	out := make([]string, 0, len(str))
	for _, s := range str {
		if s != nil {
			out = append(out, fmt.Sprintf("%q", strval(s)))
		}
	}

	return strings.Join(out, " ")
}

func indent(spaces int, v string) string {
	pad := strings.Repeat(" ", spaces)

	return pad + strings.ReplaceAll(v, "\n", "\n"+pad)
}

func nindent(spaces int, v string) string {
	return "\n" + indent(spaces, v)
}

func regexFindString(regex string, s string, n int) (string, error) {
	r, err := regexp.Compile(regex)
	if err != nil {
		return "", err
	}

	matches := r.FindStringSubmatch(s)

	if len(matches) < n+1 {
		return "", nil
	}

	return matches[n], nil
}

func regexReplaceAll(regex string, s string, repl string) (string, error) {
	r, err := regexp.Compile(regex)
	if err != nil {
		return "", err
	}

	return r.ReplaceAllString(s, repl), nil
}

func regexFind(regex string, s string) (string, error) {
	r, err := regexp.Compile(regex)
	if err != nil {
		return "", err
	}

	return r.FindString(s), nil
}

// get returns the value for the given key in the given map, or an empty string if the key does not exist.
func get(m map[string]interface{}, key string) interface{} {
	if val, ok := m[key]; ok {
		return val
	}

	return ""
}

// getValue returns the value for the given key in a semicolon-separated key=value string.
func getValue(source string, key string) string {
	parts := strings.Split(source, ";")
	for _, part := range parts {
		kv := strings.Split(part, "=")
		if kv[0] == key {
			return kv[1]
		}
	}

	return ""
}

// base64encode returns the base64 encoding of the given string.
func base64encode(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

// base64decode returns the base64 decoding of the given string.
func base64decode(v string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// cidrhost returns the IP address of the given host number in the given CIDR.
func cidrhost(cidr string, hostnum ...int) (string, error) {
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

// slaac returns the SLAAC address for the given MAC address in the given IPv6 CIDR.
func slaac(mac string, cidr string) (string, error) {
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
