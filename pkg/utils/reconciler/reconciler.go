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

package reconciler

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
)

// EventType represents the type of reconciliation event
type EventType string

const (
	FileEvent  EventType = "file"
	TimerEvent EventType = "timer"
)

// Event represents a reconciliation event
type Event struct {
	Type EventType
	Key  string
	Data any
}

// Equal checks if two events are equivalent and can be merged
func (e Event) Equal(other Event) bool {
	return e.Type == other.Type && e.Key == other.Key
}

// EventSender defines the interface for sending events
type EventSender interface {
	SendEvent(event Event)
}

// Handler defines the interface for reconciliation logic
type Handler interface {
	Reconcile(ctx context.Context, sender EventSender, event Event) error
}

// HandlerFunc is a function adapter for Handler
type HandlerFunc func(ctx context.Context, sender EventSender, event Event) error

// Reconcile calls the HandlerFunc with the given parameters
func (f HandlerFunc) Reconcile(ctx context.Context, sender EventSender, event Event) error {
	return f(ctx, sender, event)
}

// RetryableEvent wraps an event with retry information
type retryableEvent struct {
	event     Event
	attempts  int
	nextRetry time.Time
}

// ReconcilerConfig holds configuration for the reconciler
type ReconcilerConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	WatchPath  string
	SyncDelay  time.Duration

	Logger logr.Logger
}

// DefaultConfig returns a default reconciler configuration
func DefaultConfig(logger logr.Logger) ReconcilerConfig {
	return ReconcilerConfig{
		MaxRetries: 5,
		BaseDelay:  2 * time.Second,
		MaxDelay:   30 * time.Second,
		WatchPath:  "/run/qemu-server",
		Logger:     logger,
	}
}

// Reconciler manages the reconciliation process
//
//nolint:containedctx
type Reconciler struct {
	config  ReconcilerConfig
	handler Handler
	logger  logr.Logger

	eventQueue chan retryableEvent

	watcher *fsnotify.Watcher
	ticker  *time.Ticker

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopped atomic.Bool
}

// NewReconciler creates a new reconciliation scheduler
func NewReconciler(ctx context.Context, cancel context.CancelFunc, config ReconcilerConfig, handler Handler) (*Reconciler, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	rf := &Reconciler{
		config:     config,
		handler:    handler,
		logger:     config.Logger,
		eventQueue: make(chan retryableEvent, 100),
		watcher:    watcher,
		ctx:        ctx,
		cancel:     cancel,
	}

	return rf, nil
}

// Start begins the reconciliation scheduler
func (rf *Reconciler) Start() error {
	if rf.config.WatchPath != "" {
		if err := rf.watcher.Add(rf.config.WatchPath); err != nil {
			return fmt.Errorf("failed to watch path %s: %w", rf.config.WatchPath, err)
		}

		rf.wg.Add(1)
		go rf.watchFiles()
	}

	if rf.config.SyncDelay > 0 {
		rf.ticker = time.NewTicker(rf.config.SyncDelay)

		rf.wg.Add(1)
		go rf.watchTimer()
	}

	// Start retry processor
	rf.wg.Add(1)
	go rf.processRetries()

	if rf.config.SyncDelay > 0 {
		rf.SendEvent(Event{
			Type: TimerEvent,
			Key:  "sync",
			Data: time.Now(),
		})
	}

	return nil
}

// Stop gracefully stops the reconciliation framework
func (rf *Reconciler) Stop() {
	rf.cancel()
	rf.stopped.Store(true)

	rf.watcher.Close()
	if rf.ticker != nil {
		rf.ticker.Stop()
	}

	rf.wg.Wait()
	close(rf.eventQueue)
}

// SendEvent adds an event to the reconciliation queue
func (rf *Reconciler) SendEvent(event Event) {
	if rf.stopped.Load() {
		return
	}

	select {
	case rf.eventQueue <- retryableEvent{event: event, attempts: 0}:
	case <-rf.ctx.Done():
	}
}

// watchFiles monitors file system events
func (rf *Reconciler) watchFiles() {
	defer rf.wg.Done()

	rf.logger.V(1).Info("Starting file watcher")

	relevantOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove

	for {
		select {
		case event, ok := <-rf.watcher.Events:
			if !ok {
				return
			}

			if event.Op&relevantOps > 0 {
				rf.logger.V(3).Info("File system event received", "name", event.Name, "op", event.Op)

				rf.SendEvent(Event{
					Type: FileEvent,
					Key:  event.Name,
					Data: event,
				})
			}

		case err, ok := <-rf.watcher.Errors:
			if !ok {
				return
			}

			rf.logger.Error(err, "File watcher error")

		case <-rf.ctx.Done():
			rf.logger.V(1).Info("File watcher shutting down")

			return
		}
	}
}

// watchTimer monitors timer events
func (rf *Reconciler) watchTimer() {
	defer rf.wg.Done()

	rf.logger.V(1).Info("Starting timer watcher")

	for {
		select {
		case <-rf.ticker.C:
			rf.logger.V(3).Info("Timer event triggered")
			rf.SendEvent(Event{
				Type: TimerEvent,
				Key:  "sync",
				Data: time.Now(),
			})

		case <-rf.ctx.Done():
			rf.logger.V(1).Info("Timer watcher shutting down")

			return
		}
	}
}

// processRetries handles retry logic with exponential backoff
func (rf *Reconciler) processRetries() {
	rf.logger.V(1).Info("Starting retry processor")

	defer rf.wg.Done()

	retryTicker := time.NewTicker(time.Second)
	defer retryTicker.Stop()

	eventQueue := make([]retryableEvent, 0)

	for {
		select {
		case retryEvent := <-rf.eventQueue:
			found := false

			for i, existing := range eventQueue {
				if existing.event.Equal(retryEvent.event) {
					eventQueue[i] = retryEvent
					found = true

					break
				}
			}

			if !found {
				if retryEvent.attempts == 0 || time.Now().After(retryEvent.nextRetry) {
					if retry := rf.processRetryableEvent(retryEvent); retry != nil {
						eventQueue = append(eventQueue, *retry)
					}

					continue
				}

				eventQueue = append(eventQueue, retryEvent)
			}

		case <-retryTicker.C:
			now := time.Now()
			newQueue := eventQueue[:0]

			for _, retryEvent := range eventQueue {
				if now.After(retryEvent.nextRetry) {
					if retry := rf.processRetryableEvent(retryEvent); retry != nil {
						newQueue = append(newQueue, *retry)
					}
				} else {
					newQueue = append(newQueue, retryEvent)
				}
			}

			eventQueue = newQueue

		case <-rf.ctx.Done():
			return
		}
	}
}

// processRetryableEvent handles a single retryable event
func (rf *Reconciler) processRetryableEvent(retryEvent retryableEvent) *retryableEvent {
	rf.logger.V(1).Info("Processing retryable event", "type", retryEvent.event.Type, "key", retryEvent.event.Key, "attempts", retryEvent.attempts)

	err := rf.handler.Reconcile(rf.ctx, rf, retryEvent.event)

	switch {
	case err != nil && retryEvent.attempts < rf.config.MaxRetries:
		delay := time.Duration(float64(rf.config.BaseDelay) * math.Pow(2, float64(retryEvent.attempts)))
		delay = min(delay, rf.config.MaxDelay)

		rf.logger.Error(err, "Reconciliation failed, scheduling retry",
			"attempt", retryEvent.attempts+1,
			"maxRetries", rf.config.MaxRetries,
			"retryIn", delay)

		return &retryableEvent{
			event:     retryEvent.event,
			attempts:  retryEvent.attempts + 1,
			nextRetry: time.Now().Add(delay),
		}
	case err != nil:
		rf.logger.Error(err, "Reconciliation permanently failed", "attempts", retryEvent.attempts)
	default:
		rf.logger.V(1).Info("Reconciliation succeeded", "type", retryEvent.event.Type, "key", retryEvent.event.Key)
	}

	return nil
}
