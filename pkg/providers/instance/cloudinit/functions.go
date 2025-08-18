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
	"reflect"
	"regexp"
	"strings"
	"text/template"

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
	"getValue": getValue,

	// Encoding functions:
	"b64enc": base64encode,
	"b64dec": base64decode,

	// Flow Control functions:
	"empty":     empty,
	"contains":  func(substr string, str string) bool { return strings.Contains(str, substr) },
	"hasPrefix": func(substr string, str string) bool { return strings.HasPrefix(str, substr) },
	"hasSuffix": func(substr string, str string) bool { return strings.HasSuffix(str, substr) },
}

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

func toJson(v any) string {
	output, _ := json.Marshal(v) //nolint: errchkjson

	return string(output)
}

func toPrettyJson(v any) string {
	output, _ := json.MarshalIndent(v, "", "  ") //nolint: errchkjson

	return string(output)
}

func toYaml(v any) string {
	var output bytes.Buffer

	encoder := goYaml.NewEncoder(&output)
	encoder.Encode(v)

	return strings.TrimSuffix(output.String(), "\n")
}

func toYamlPretty(v any) string {
	var output bytes.Buffer

	encoder := goYaml.NewEncoder(&output)
	encoder.SetIndent(2)
	encoder.Encode(v)

	return strings.TrimSuffix(output.String(), "\n")
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

func base64encode(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func base64decode(v string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

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
