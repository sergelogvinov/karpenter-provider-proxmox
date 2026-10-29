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

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"
)

func generate(args ...string) ([]*instancetype.InstanceTypeStatic, error) {
	cmd := buildGenerateCmd()
	cmd.SetArgs(args)

	out := &bytes.Buffer{}
	cmd.SetOut(out)

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	instanceTypes := []*instancetype.InstanceTypeStatic{}
	if err := json.Unmarshal(out.Bytes(), &instanceTypes); err != nil {
		return nil, err
	}

	return instanceTypes, nil
}

func TestGenerateCPUShare(t *testing.T) {
	t.Parallel()

	instanceTypes, err := generate("--cpus", "2", "--memfactor", "2,4", "--cpushare", "2,4")
	require.NoError(t, err)

	got := map[string]int{}
	for _, it := range instanceTypes {
		got[it.Name] = it.CPUShare
	}

	require.Equal(t, map[string]int{
		"c1.2VCPU-4GB": 0,
		"s1.2VCPU-8GB": 0,
		"e2.2VCPU-4GB": 2,
		"e2.2VCPU-8GB": 2,
		"e4.2VCPU-4GB": 4,
		"e4.2VCPU-8GB": 4,
	}, got)
}

func TestGenerateRejectsInvalidCPUShare(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"1", "5", "0", "two", "2,8"} {
		_, err := generate("--cpushare", value)
		require.ErrorContains(t, err, zonecapacity.CPUShareSeats, "--cpushare %s", value)
	}
}
