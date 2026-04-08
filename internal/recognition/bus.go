package recognition

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// DefaultQueueSize is the capacity of the dispatch queue.
const DefaultQueueSize = 4096

// DefaultWorkers is the number of concurrent dispatch workers.
const DefaultWorkers = 4

// BusConfig holds tunable parameters for the Commit Bus.
type BusConfig struct {
	QueueSize int
	Workers   int
}

// DefaultBusConfig returns production-ready defaults.
func DefaultBusConfig() BusConfig {
	return BusConfig{
		QueueSize: DefaultQueueSize,
		Workers:   DefaultWorkers,
	}
}

// commitItem is the internal dispatch unit queued by Emit.
type commitItem struct {
	Record CommitRecord
	Event  *event.Event
}

// Bus is the Causal Commit Bus (CCB). It dispatches committed events to
// registered consumers asynchronously, outside the DAG write lock.
//
// Properties:
//   - Bounded: fixed-size dispatch queue with backpressure on overflow.
//   - Async: worker pool drains the queue; consumers never block dag.Add.
//   - Idempotent: the Recognition Index prevents duplicate processing.
//   - Source-aware: CommitRecord.Source distinguishes local/remote/replay.
type Bus struct {
	config    BusConfig
	index     *Index
	readModel ReadModel

	consumers map[string]CommitConsumer
	mu        sync.RWMutex // protects consumers map

	queue  chan commitItem
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBus creates a Commit Bus with the given configuration, recognition
// index, and read model. Call Start() to launch workers.
func NewBus(cfg BusConfig, index *Index, readModel ReadModel) *Bus {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Bus{
		config:    cfg,
		index:     index,
		readModel: readModel,
		consumers: make(map[string]CommitConsumer),
		queue:     make(chan commitItem, cfg.QueueSize),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Register adds a consumer to the bus. Must be called before Start.
// Returns ErrConsumerAlreadyRegistered if a consumer with the same Name
// is already registered.
func (b *Bus) Register(c CommitConsumer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	name := c.Name()
	if _, exists := b.consumers[name]; exists {
		return ErrConsumerAlreadyRegistered
	}
	b.consumers[name] = c
	slog.Info("recognition: consumer registered", "consumer", name)
	return nil
}

// Start launches the worker pool. Call after all consumers are registered.
func (b *Bus) Start() {
	for i := 0; i < b.config.Workers; i++ {
		b.wg.Add(1)
		go b.worker(i)
	}
	slog.Info("recognition: commit bus started",
		"workers", b.config.Workers,
		"queue_size", b.config.QueueSize,
		"consumers", len(b.consumers),
	)
}

// Stop signals workers to exit and waits for them to drain.
func (b *Bus) Stop() {
	b.cancel()
	b.wg.Wait()
}

// Emit enqueues a committed event for dispatch to consumers. Non-blocking:
// returns ErrQueueFull if the dispatch queue is at capacity, signaling
// backpressure. The caller (dag.Add hook) must NOT block on this.
func (b *Bus) Emit(record CommitRecord, ev *event.Event) error {
	select {
	case b.queue <- commitItem{Record: record, Event: ev}:
		return nil
	default:
		slog.Warn("recognition: dispatch queue full — backpressure",
			"event_id", record.EventID,
			"source", record.Source,
			"queue_cap", b.config.QueueSize,
		)
		return ErrQueueFull
	}
}

// QueueDepth returns the current number of items in the dispatch queue.
func (b *Bus) QueueDepth() int {
	return len(b.queue)
}

// ConsumerCount returns the number of registered consumers.
func (b *Bus) ConsumerCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.consumers)
}

// worker drains the dispatch queue and routes events to consumers.
func (b *Bus) worker(id int) {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case item, ok := <-b.queue:
			if !ok {
				return
			}
			b.dispatch(item)
		}
	}
}

// dispatch routes a single committed event to all interested consumers.
func (b *Bus) dispatch(item commitItem) {
	b.mu.RLock()
	consumers := make([]CommitConsumer, 0, len(b.consumers))
	for _, c := range b.consumers {
		consumers = append(consumers, c)
	}
	b.mu.RUnlock()

	for _, c := range consumers {
		if !c.Interested(item.Event) {
			continue
		}
		b.processConsumer(c, item)
	}
}

// processConsumer handles recognition, readiness, and consumption for a
// single consumer and event.
func (b *Bus) processConsumer(c CommitConsumer, item commitItem) {
	name := c.Name()
	eventID := item.Record.EventID

	// Atomic idempotency: MarkRecognizedOnce returns false if the item was
	// already recognized by another worker. This prevents concurrent workers
	// from both proceeding past the recognition gate.
	firstTime, err := b.index.MarkRecognizedOnce(name, eventID)
	if err != nil {
		slog.Warn("recognition: mark recognized failed",
			"consumer", name, "event_id", eventID, "err", err)
		return
	}
	if !firstTime {
		return // another worker already handled this (consumer, event) pair
	}

	// Track attempt.
	b.index.IncrementAttempt(name, eventID)

	// Check readiness.
	ready, prereqKey, readyErr := c.Ready(b.ctx, item.Event, b.readModel)
	if readyErr != nil {
		slog.Warn("recognition: ready check failed",
			"consumer", name, "event_id", eventID, "err", readyErr)
		return
	}

	if !ready {
		_ = b.index.SetDeferred(name, eventID, prereqKey, prereqKey)
		slog.Debug("recognition: consumer deferred",
			"consumer", name,
			"event_id", eventID,
			"prerequisite_key", prereqKey,
			"source", item.Record.Source,
		)
		return
	}

	// Consume.
	if consumeErr := c.Consume(b.ctx, item.Event); consumeErr != nil {
		slog.Warn("recognition: consume failed",
			"consumer", name, "event_id", eventID, "err", consumeErr)
		return
	}

	_ = b.index.MarkReady(name, eventID)

	slog.Debug("recognition: consumer recognized",
		"consumer", name,
		"event_id", eventID,
		"source", item.Record.Source,
		"replay", item.Record.Replay,
	)
}

// getConsumer returns the consumer by name, or nil if not registered.
// Used by Activator for deferred retries.
func (b *Bus) getConsumer(name string) CommitConsumer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.consumers[name]
}

// getEvent retrieves an event from the read model. Used by Activator.
func (b *Bus) getEvent(id event.EventID) *event.Event {
	if b.readModel == nil {
		return nil
	}
	return b.readModel.GetEvent(id)
}
