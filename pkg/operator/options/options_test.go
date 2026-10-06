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

package options

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

// TestCPUOvercommitRatioOptions verifies explicit policy selection and configuration precedence.
func TestCPUOvercommitRatioOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       string
		args      []string
		want      float64
		wantError bool
	}{
		{name: "default", want: 1},
		{name: "environment", env: "2", args: []string{"--node-policy=simple-with-cpu-overcommit"}, want: 2},
		{name: "fractional flag", args: []string{"--node-policy=simple-with-cpu-overcommit", "--cpu-overcommit-ratio=1.5"}, want: 1.5},
		{name: "flag overrides environment", env: "2", args: []string{"--node-policy=simple-with-cpu-overcommit", "--cpu-overcommit-ratio=3"}, want: 3},
		{name: "overcommit default", args: []string{"--node-policy=simple-with-cpu-overcommit"}, want: 1},
		{name: "simple overcommit rejected", env: "2", wantError: true},
		{name: "static default", args: []string{"--node-policy=static"}, want: 1},
		{name: "static overcommit", env: "2", args: []string{"--node-policy=static"}, wantError: true},
		{name: "invalid policy overcommit", env: "2", args: []string{"--node-policy=other"}, wantError: true},
		{name: "zero", env: "0", wantError: true},
		{name: "below one", env: "0.5", wantError: true},
		{name: "negative", env: "-1", wantError: true},
		{name: "nan", env: "NaN", wantError: true},
		{name: "infinity", env: "+Inf", wantError: true},
		{name: "malformed environment", env: "bad", wantError: true},
		{name: "malformed flag", args: []string{"--node-policy=simple-with-cpu-overcommit", "--cpu-overcommit-ratio=bad"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(cpuOvercommitRatioEnvVarName, tc.env)
			t.Setenv(nodePolicyEnvVarName, "simple")
			t.Setenv(cloudConfigEnvVarName, "config.yaml")
			if tc.env == "" {
				require.NoError(t, os.Unsetenv(cpuOvercommitRatioEnvVarName))
			}

			opts := &Options{}
			fs := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
			opts.AddFlags(fs)
			err := opts.Parse(fs, tc.args...)
			if tc.wantError {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, opts.CPUOvercommitRatio)
		})
	}
}
