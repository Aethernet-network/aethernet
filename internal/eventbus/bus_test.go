package eventbus_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/eventbus"
)

func TestPublishSubscribe(t *testing.T) {
	b := eventbus.New()
	sub := b.Subscribe(nil, 10)
	defer b.Unsubscribe(sub)

	evt := eventbus.Event{
		Type:      eventbus.EventTypeTransfer,
		Timestamp: time.Now(),
		Data:      map[string]any{"amount": 100},
	}
	b.Publish(evt)

	select {
	case got := <-sub.Ch:
		if got.Type != eventbus.EventTypeTransfer {
			t.Fatalf("expected %s, got %s", eventbus.EventTypeTransfer, got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestFilter(t *testing.T) {
	b := eventbus.New()
	sub := b.Subscribe([]eventbus.EventType{eventbus.EventTypeGeneration}, 10)
	defer b.Unsubscribe(sub)

	// Transfer should be filtered out; Generation should pass through.
	b.Publish(eventbus.Event{Type: eventbus.EventTypeTransfer, Timestamp: time.Now()})
	b.Publish(eventbus.Event{Type: eventbus.EventTypeGeneration, Timestamp: time.Now()})

	select {
	case got := <-sub.Ch:
		if got.Type != eventbus.EventTypeGeneration {
			t.Fatalf("expected generation, got %s", got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	// No more events should be pending.
	select {
	case got := <-sub.Ch:
		t.Fatalf("unexpected extra event: %s", got.Type)
	default:
		// correct
	}
}

func TestFilter_Empty(t *testing.T) {
	b := eventbus.New()
	// Empty (non-nil) filter should behave the same as nil — receive all types.
	sub := b.Subscribe([]eventbus.EventType{}, 10)
	defer b.Unsubscribe(sub)

	b.Publish(eventbus.Event{Type: eventbus.EventTypeTransfer, Timestamp: time.Now()})
	b.Publish(eventbus.Event{Type: eventbus.EventTypeGeneration, Timestamp: time.Now()})

	count := 0
	for i := 0; i < 2; i++ {
		select {
		case <-sub.Ch:
			count++
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 events, got %d", count)
	}
}

func TestUnsubscribe(t *testing.T) {
	b := eventbus.New()
	sub := b.Subscribe(nil, 10)
	b.Unsubscribe(sub)

	// Channel should be closed.
	_, open := <-sub.Ch
	if open {
		t.Fatal("expected channel to be closed after Unsubscribe")
	}

	if b.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers after unsubscribe, got %d", b.SubscriberCount())
	}

	// Publishing after all unsubscribes must not panic.
	b.Publish(eventbus.Event{Type: eventbus.EventTypeTransfer, Timestamp: time.Now()})
}

func TestPublish_FullBuffer(t *testing.T) {
	b := eventbus.New()
	sub := b.Subscribe(nil, 1)
	defer b.Unsubscribe(sub)

	// Fill the single-slot buffer.
	b.Publish(eventbus.Event{Type: eventbus.EventTypeTransfer, Timestamp: time.Now()})
	// This must not block or panic even though the buffer is full.
	b.Publish(eventbus.Event{Type: eventbus.EventTypeGeneration, Timestamp: time.Now()})

	// The first event should be in the buffer.
	select {
	case got := <-sub.Ch:
		if got.Type != eventbus.EventTypeTransfer {
			t.Fatalf("expected transfer (first event), got %s", got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	// The second event was dropped — channel must be empty now.
	select {
	case got := <-sub.Ch:
		t.Fatalf("expected empty buffer, got %s", got.Type)
	default:
		// correct
	}
}

func TestMultipleSubscribers(t *testing.T) {
	b := eventbus.New()
	sub1 := b.Subscribe(nil, 10)
	sub2 := b.Subscribe(nil, 10)
	defer b.Unsubscribe(sub1)
	defer b.Unsubscribe(sub2)

	b.Publish(eventbus.Event{Type: eventbus.EventTypeSlash, Timestamp: time.Now()})

	for i, sub := range []*eventbus.Subscriber{sub1, sub2} {
		select {
		case got := <-sub.Ch:
			if got.Type != eventbus.EventTypeSlash {
				t.Fatalf("sub%d: expected slash, got %s", i+1, got.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for event on subscriber %d", i+1)
		}
	}
}

func TestSubscriberCount(t *testing.T) {
	b := eventbus.New()
	if b.SubscriberCount() != 0 {
		t.Fatalf("expected 0 initially, got %d", b.SubscriberCount())
	}
	sub1 := b.Subscribe(nil, 10)
	sub2 := b.Subscribe(nil, 10)
	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
	b.Unsubscribe(sub1)
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("expected 1 after unsubscribe, got %d", got)
	}
	b.Unsubscribe(sub2)
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}
