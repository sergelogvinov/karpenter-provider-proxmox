//go:build e2e

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

package framework

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

// SharedClient and SharedConfig are set once by TestMain before any test
// runs, and read (never mutated) by every Framework created afterwards.
var (
	SharedClient *Client
	SharedConfig Config
)

// Framework is the per-test handle: it owns the shared Client/Config set up
// once in TestMain and generates unique, prefixed names for the
// cluster-scoped objects (ProxmoxTemplate, ProxmoxNodeClass, ...) a test
// creates.
type Framework struct {
	T      *testing.T
	Client *Client
	Config Config
}

// New creates a Framework for t, backed by the shared cluster client.
func New(t *testing.T) *Framework {
	t.Helper()

	return &Framework{
		T:      t,
		Client: SharedClient,
		Config: SharedConfig,
	}
}

// Name returns a unique object name "<prefix>-<random>" for a test to give
// a cluster-scoped resource it creates.
func (f *Framework) Name() string {
	return fmt.Sprintf("%s-%s", f.Config.NamePrefix, randSuffix())
}

// Context returns a context bound to the Framework's configured timeout.
func (f *Framework) Context() (context.Context, context.CancelFunc) {
	return f.ContextTimeout(f.Config.Timeout)
}

// ContextTimeout returns a context bound to timeout, for waits that need a
// duration other than the Framework's default Config.Timeout - e.g.
// Config.NodeTimeout, for anything that needs a new Proxmox VM to boot and
// join the cluster.
func (f *Framework) ContextTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// Logf narrates what the test is doing right now: prefixed with a
// wall-clock timestamp so a `go test -v` stream shows live progress through
// a scenario, not just the pass/fail result at the end.
func (f *Framework) Logf(format string, args ...any) {
	f.T.Helper()
	f.T.Logf("[%s] %s", time.Now().Format(time.TimeOnly), fmt.Sprintf(format, args...))
}

// Step narrates the start of a named action and returns a function to call
// once it completes, which logs how long it took. Usage:
//
//	defer f.Step("waiting for proxmoxtemplate %s to be ready", name)()
func (f *Framework) Step(format string, args ...any) func() {
	f.T.Helper()

	msg := fmt.Sprintf(format, args...)
	f.Logf("-> %s", msg)

	start := time.Now()

	return func() {
		f.T.Helper()
		f.Logf("<- %s (%s)", msg, time.Since(start).Round(time.Millisecond))
	}
}

func randSuffix() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

	b := make([]byte, 6)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}

	return string(b)
}
