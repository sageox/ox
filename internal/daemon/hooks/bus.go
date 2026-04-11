package hooks

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// EventBus dispatches daemon events to hook scripts.
// All dispatch is asynchronous — Emit() never blocks the caller.
type EventBus struct {
	hookMu       sync.RWMutex
	hookDispatch func(ctx context.Context, event Event) // set via SetHookDispatch
	logger       *slog.Logger
}

// New creates an EventBus.
func New(logger *slog.Logger) *EventBus {
	return &EventBus{
		logger: logger,
	}
}

// SetHookDispatch sets the function called to dispatch events to hook scripts.
func (b *EventBus) SetHookDispatch(fn func(ctx context.Context, event Event)) {
	b.hookMu.Lock()
	defer b.hookMu.Unlock()
	b.hookDispatch = fn
}

// Emit dispatches an event asynchronously. It never blocks the caller.
func (b *EventBus) Emit(ctx context.Context, event Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	b.logger.Debug("event emitted", "event", event.Name)

	b.hookMu.RLock()
	dispatch := b.hookDispatch
	b.hookMu.RUnlock()
	if dispatch != nil {
		dispatch(ctx, event)
	}
}
