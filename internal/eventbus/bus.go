// Package eventbus provides a lightweight thread-safe pub/sub event bus for
// broadcasting real-time node events to WebSocket clients and other consumers.
//
// Usage:
//
//	bus := eventbus.New()
//	sub := bus.Subscribe(nil, 64) // nil filter = all event types
//	defer bus.Unsubscribe(sub)
//
//	go func() {
//	    for evt := range sub.Ch {
//	        fmt.Printf("event: %s %v\n", evt.Type, evt.Data)
//	    }
//	}()
//
//	bus.Publish(eventbus.Event{Type: eventbus.EventTypeTransfer, ...})
package eventbus

import (
	"sync"
	"time"
)

// EventType identifies the category of a published event.
type EventType string

const (
	// EventTypeTransfer is published when a Transfer event settles positively.
	EventTypeTransfer EventType = "transfer"

	// EventTypeGeneration is published when a Generation event is verified positively.
	EventTypeGeneration EventType = "generation"

	// EventTypeVerification is published when any OCS verdict is processed.
	EventTypeVerification EventType = "verification"

	// EventTypeSlash is published when an agent's stake is slashed.
	EventTypeSlash EventType = "slash"

	// EventTypeRegistration is published when a service listing is created or updated.
	EventTypeRegistration EventType = "registration"

	// EventTypeStake is published when tokens are staked for an agent.
	EventTypeStake EventType = "stake"

	// EventTypeUnstake is published when tokens are unstaked for an agent.
	EventTypeUnstake EventType = "unstake"

	// EventTypeNewAgent is published when a new agent identity is registered.
	EventTypeNewAgent EventType = "new_agent"
)

// Event is the envelope published to all matching subscribers.
type Event struct {
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

// Subscriber is a registered consumer of bus events.
// Read from Ch to receive matching events; Ch is closed when Unsubscribe is called.
type Subscriber struct {
	// Ch delivers incoming events; closed when Unsubscribe is called.
	Ch chan Event

	// Filter is the set of event types this subscriber accepts.
	// An empty filter matches all event types.
	Filter []EventType

	// id uniquely identifies this subscriber within the bus.
	id uint64
}

// Bus is a thread-safe pub/sub event bus.
// It is safe for concurrent use by any number of publishers and subscribers.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[uint64]*Subscriber
	nextID      uint64
}

// New returns an empty, ready-to-use Bus.
func New() *Bus {
	return &Bus{
		subscribers: make(map[uint64]*Subscriber),
	}
}

// Subscribe registers a new subscriber and returns it.
// filter restricts which event types are delivered; nil or empty means all types.
// bufferSize is the Ch channel capacity; Publish is non-blocking and drops events
// when the buffer is full.
func (b *Bus) Subscribe(filter []EventType, bufferSize int) *Subscriber {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	sub := &Subscriber{
		Ch:     make(chan Event, bufferSize),
		Filter: filter,
		id:     id,
	}
	b.subscribers[id] = sub
	return sub
}

// Unsubscribe removes the subscriber from the bus and closes its channel.
// It is a no-op if sub was already unsubscribed.
func (b *Bus) Unsubscribe(sub *Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.subscribers[sub.id]; ok {
		delete(b.subscribers, sub.id)
		close(sub.Ch)
	}
}

// Publish delivers evt to all subscribers whose filter matches the event type.
// The delivery is non-blocking: if a subscriber's buffer is full the event is
// silently dropped for that subscriber without blocking the caller.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subscribers {
		if !matchesFilter(sub.Filter, evt.Type) {
			continue
		}
		select {
		case sub.Ch <- evt:
		default:
			// Buffer full — drop silently.
		}
	}
}

// SubscriberCount returns the current number of registered subscribers.
// The value is a point-in-time snapshot.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// matchesFilter reports whether typ should be delivered to a subscriber with
// the given filter list. An empty filter matches all types.
func matchesFilter(filter []EventType, typ EventType) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == typ {
			return true
		}
	}
	return false
}
