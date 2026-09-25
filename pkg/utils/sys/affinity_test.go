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

package sys

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/utils/cpuset"
)

// TestGetThreadAffinitySelf reads the test binary's own affinity mask
// off /proc, which must always succeed and be non-empty on any Linux
// host.
func TestGetThreadAffinitySelf(t *testing.T) {
	pid := os.Getpid()

	cpus, err := GetThreadAffinity(pid, pid)
	require.NoError(t, err)
	assert.False(t, cpus.IsEmpty())
}

func TestGetThreadAffinityMissingProcess(t *testing.T) {
	_, err := GetThreadAffinity(1<<30, 1<<30)
	assert.Error(t, err)
}

func TestSetThreadsAffinityNoopGuards(t *testing.T) {
	ctx := t.Context()

	assert.NoError(t, SetThreadsAffinity(ctx, 100, os.Getpid(), nil, cpuset.New(0)))
	assert.NoError(t, SetThreadsAffinity(ctx, 100, os.Getpid(), []int{os.Getpid()}, cpuset.New()))
	assert.NoError(t, SetThreadsAffinity(ctx, 100, 0, []int{1}, cpuset.New(0)))
}

func TestSetProcessAffinityNoopGuards(t *testing.T) {
	ctx := t.Context()

	assert.NoError(t, SetProcessAffinity(ctx, 100, 0, cpuset.New(0)))
	assert.NoError(t, SetProcessAffinity(ctx, 100, os.Getpid(), cpuset.New()))
}

// TestSetProcessAffinityIdempotent exercises the real taskset-invoking
// path, but only ever "sets" the process to the mask it already has —
// GetThreadAffinity must report that as already satisfied, so no
// taskset invocation (and therefore no risk of narrowing the test
// binary's own affinity) ever happens.
func TestSetProcessAffinityIdempotent(t *testing.T) {
	pid := os.Getpid()

	current, err := GetThreadAffinity(pid, pid)
	require.NoError(t, err)

	assert.NoError(t, SetProcessAffinity(t.Context(), 100, pid, current))

	after, err := GetThreadAffinity(pid, pid)
	require.NoError(t, err)
	assert.True(t, current.Equals(after))
}
