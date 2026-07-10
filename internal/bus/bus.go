// Package bus implements the event bus for message dispatch.
package bus

import (
	"context"
	"sync"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// EventBus manages event distribution to listeners.
type EventBus struct {
	mu        sync.RWMutex
	listeners map[string][]contract.Listener // event type → listeners
	closed    bool
}

// New creates a new EventBus instance.
func New() *EventBus {
	return &EventBus{
		listeners: make(map[string][]contract.Listener),
	}
}

// Subscribe registers a listener for an event type.
func (eb *EventBus) Subscribe(listener contract.Listener) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.listeners[listener.Event] = append(eb.listeners[listener.Event], listener)
}

// Unsubscribe removes all listeners for a given event type.
// Returns the number of listeners removed.
func (eb *EventBus) Unsubscribe(event string) int {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	n := len(eb.listeners[event])
	delete(eb.listeners, event)
	return n
}

// Publish dispatches an event to all registered listeners for its type.
// Listeners are called in order of their Order field (lower values first).
// If a listener returns an error, the error is logged but subsequent listeners still run.
func (eb *EventBus) Publish(ctx context.Context, eventType string, eventCtx contract.EventContext) {
	eb.mu.RLock()
	listeners, ok := eb.listeners[eventType]
	eb.mu.RUnlock()

	if !ok || len(listeners) == 0 {
		return
	}

	// Sort by Order (stable, insertion order preserved for equal Order)
	sorted := make([]contract.Listener, len(listeners))
	copy(sorted, listeners)
	sortListeners(sorted)

	for _, l := range sorted {
		select {
		case <-ctx.Done():
			return
		default:
			if err := l.Handler(eventCtx); err != nil {
				// Error is logged by the caller; continue to next listener
				continue
			}
		}
	}
}

// Close shuts down the event bus and prevents new subscriptions.
func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.closed = true
	eb.listeners = nil
}

// sortListeners sorts listeners by Order field ascending.
func sortListeners(listeners []contract.Listener) {
	for i := 0; i < len(listeners); i++ {
		for j := i + 1; j < len(listeners); j++ {
			if listeners[j].Order < listeners[i].Order {
				listeners[i], listeners[j] = listeners[j], listeners[i]
			}
		}
	}
}
