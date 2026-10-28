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

package instance

import (
	"context"
	"slices"
	"time"
)

// rollbackTimeout bounds how long undoing a failed launch may take.
const rollbackTimeout = 5 * time.Minute

// launchRollback undoes a launch that failed partway. Each launch step that
// changes something outside the VM's own config (zone capacity, the VM
// itself, IP pool addresses, the cloud-init ISO) adds its undo as soon as
// the change is made, and run undoes them newest first.
type launchRollback struct {
	undo []func(ctx context.Context)
}

func (r *launchRollback) add(undo func(ctx context.Context)) {
	r.undo = append(r.undo, undo)
}

// run undoes every added step, newest first, then forgets them. It ignores
// ctx's cancellation, so a launch that failed because ctx was canceled is
// still undone.
func (r *launchRollback) run(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	for _, undo := range slices.Backward(r.undo) {
		undo(ctx)
	}

	r.undo = nil
}
