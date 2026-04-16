package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// AdmissionStore is the persistence interface for dispatcher admission
// records. *store.Store satisfies this interface.
type AdmissionStore interface {
	GetAdmission(key string) (*AdmissionRecord, error)
	PutAdmission(key string, record *AdmissionRecord) error
	AllAdmissions() ([]*AdmissionRecord, error)
}

// EvidenceEmitter publishes a canonical event to the DAG. Injected to
// break the circular dependency dispatch → localpub → dag → dispatch.
// In production, wired to localpub.Publisher.Publish via cmd/node/main.go.
type EvidenceEmitter func(ev *event.Event) error

// Dispatcher is the CanonicalEventDispatcher primitive. It sits between
// the recognition fabric and all canonical-event consumers, guaranteeing
// exactly-once successful Apply per (event, consumer) pair (C-1).
//
// Consumers register before startup via Register. Events are delivered
// via Admit, which may be called from any goroutine, any number of times
// per event — the dispatcher absorbs duplication at a single
// architectural choke point.
type Dispatcher struct {
	mu              sync.RWMutex
	consumers       map[string]Consumer
	store           AdmissionStore
	dag             DAGAnchorReader
	epochFn         func() uint64
	evidenceEmitter EvidenceEmitter
	deferralIndex   map[event.EventID][]string // prereq EventID → admission keys waiting
}

// NewDispatcher constructs a dispatcher. All parameters are required.
// Register consumers before calling Recover or Admit.
func NewDispatcher(store AdmissionStore, dag DAGAnchorReader, epochFn func() uint64) *Dispatcher {
	return &Dispatcher{
		consumers:     make(map[string]Consumer),
		store:         store,
		dag:           dag,
		epochFn:       epochFn,
		deferralIndex: make(map[event.EventID][]string),
	}
}

// SetEvidenceEmitter wires the function used to emit PrerequisiteWithholding
// evidence events to the DAG. Must be called before Admit.
func (d *Dispatcher) SetEvidenceEmitter(fn EvidenceEmitter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.evidenceEmitter = fn
}

// Register adds a consumer. Must be called before Recover or Admit.
// Performs structural validation per C-8. Returns an error if the
// consumer fails validation or if a consumer with the same Name is
// already registered.
func (d *Dispatcher) Register(c Consumer) error {
	if err := validateConsumer(c); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.consumers[c.Name()]; exists {
		return fmt.Errorf("dispatch: consumer %q already registered", c.Name())
	}
	d.consumers[c.Name()] = c
	return nil
}

// ConsumerCount returns the number of registered consumers.
func (d *Dispatcher) ConsumerCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.consumers)
}

// Admit processes a canonical event delivery. Thread-safe; may be called
// from multiple goroutines concurrently. The dispatcher guarantees
// exactly-once invocation of each interested consumer's Apply method
// per canonical event (C-1).
//
// The admission flow:
//  1. Canonicalize the event and compute the BLAKE3 admission key (outside
//     any transaction, per C-3/C-5).
//  2. Read-modify-write the admission record (atomic, per C-2).
//  3. Verify the DAG anchor (per C-6).
//  4. Invoke consumers outside the transaction (per C-5).
func (d *Dispatcher) Admit(ctx context.Context, ev *event.Event) error {
	key, err := AdmissionKey(ev)
	if err != nil {
		return fmt.Errorf("dispatch: admission key: %w", err)
	}

	if err := verifyAnchor(d.dag, d.currentAnchor()); err != nil {
		return err
	}

	d.mu.RLock()
	consumers := d.snapshotInterestedConsumers(ev)
	d.mu.RUnlock()

	if len(consumers) == 0 {
		return nil
	}

	rec, err := d.reserveOrLoad(key, ev, consumers)
	if err != nil {
		return err
	}

	if rec.State == StateApplied {
		return nil
	}

	return d.invokeConsumers(ctx, ev, key, rec)
}

func (d *Dispatcher) currentAnchor() event.EventID {
	tips := d.dag.Tips()
	if len(tips) > 0 {
		return tips[0]
	}
	return ""
}

func (d *Dispatcher) snapshotInterestedConsumers(ev *event.Event) []Consumer {
	var interested []Consumer
	for _, c := range d.consumers {
		if c.Interested(ev) {
			interested = append(interested, c)
		}
	}
	return interested
}

// reserveOrLoad atomically reads or creates the admission record.
// Per C-2: the check-and-set is atomic. Per C-7: any storage error
// other than "not found" causes the dispatcher to refuse invocation.
func (d *Dispatcher) reserveOrLoad(key string, ev *event.Event, consumers []Consumer) (*AdmissionRecord, error) {
	existing, err := d.store.GetAdmission(key)
	if err != nil {
		// Not found: create a new reservation.
		if isNotFound(err) {
			return d.createReservation(key, ev, consumers)
		}
		return nil, fmt.Errorf("dispatch: get admission %s: %w", key, err)
	}

	// Already have a record. Handle based on state.
	switch existing.State {
	case StateApplied:
		return existing, nil

	case StateFailedRetryable:
		existing.State = StateProcessing
		if err := d.store.PutAdmission(key, existing); err != nil {
			return nil, fmt.Errorf("dispatch: retry transition for %s: %w", key, err)
		}
		return existing, nil

	case StateReservedPendingPrereqs:
		// Part D handles prerequisite re-check. In Part C, transition
		// to processing immediately.
		existing.State = StateProcessing
		if err := d.store.PutAdmission(key, existing); err != nil {
			return nil, fmt.Errorf("dispatch: prereq transition for %s: %w", key, err)
		}
		return existing, nil

	case StateProcessing:
		return existing, nil
	}

	return existing, nil
}

func (d *Dispatcher) createReservation(key string, ev *event.Event, consumers []Consumer) (*AdmissionRecord, error) {
	consumerMap := make(map[string]PerConsumerStatus, len(consumers))
	for _, c := range consumers {
		consumerMap[c.Name()] = ConsumerPending
	}

	rec := &AdmissionRecord{
		SchemaVersion:             1,
		Key:                       key,
		State:                     StateProcessing, // Part C skips reserved-pending-prereqs
		DAGAnchor:                 d.currentAnchor(),
		PrerequisiteSchemaVersion: 0,
		Consumers:                 consumerMap,
		EventID:                   ev.ID,
		EventType:                 string(ev.Type),
		CreatedAtEpoch:            d.epochFn(),
	}

	if err := d.store.PutAdmission(key, rec); err != nil {
		return nil, fmt.Errorf("dispatch: create reservation for %s: %w", key, err)
	}
	return rec, nil
}

// invokeConsumers calls Apply on each consumer that is still pending or
// failed-retryable. Consumers already in per-consumer applied are skipped
// (C-1). After all invocations, the top-level state is recomputed and
// persisted.
func (d *Dispatcher) invokeConsumers(ctx context.Context, ev *event.Event, key string, rec *AdmissionRecord) error {
	d.mu.RLock()
	consumers := d.consumers
	d.mu.RUnlock()

	changed := false
	for name, status := range rec.Consumers {
		if status == ConsumerApplied {
			continue
		}
		c, ok := consumers[name]
		if !ok {
			continue
		}

		err := safeApply(ctx, c, ev)
		if err != nil {
			slog.Warn("dispatch: consumer Apply failed",
				"consumer", name, "event", ev.ID, "err", err)
			rec.Consumers[name] = ConsumerFailedRetryable
			changed = true
		} else {
			rec.Consumers[name] = ConsumerApplied
			changed = true
		}
	}

	if changed {
		rec.State = computeTopLevelState(rec.Consumers)
		if err := d.store.PutAdmission(key, rec); err != nil {
			return fmt.Errorf("dispatch: persist after invoke for %s: %w", key, err)
		}
	}
	return nil
}

// safeApply calls consumer.Apply with panic recovery. A panic is treated
// as a failed-retryable error.
func safeApply(ctx context.Context, c Consumer, ev *event.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dispatch: consumer %s panicked on event %s: %v", c.Name(), ev.ID, r)
		}
	}()
	return c.Apply(ctx, ev)
}

// Recover scans all non-terminal admission records at startup and
// resolves them deterministically. Must be called after Register and
// before any Admit calls (load-before-listener ordering).
func (d *Dispatcher) Recover(ctx context.Context) error {
	records, err := d.store.AllAdmissions()
	if err != nil {
		return fmt.Errorf("dispatch: recovery scan failed: %w", err)
	}

	d.mu.RLock()
	consumers := d.consumers
	d.mu.RUnlock()

	for _, rec := range records {
		switch rec.State {
		case StateApplied:
			continue

		case StateFailedRetryable:
			continue

		case StateReservedPendingPrereqs:
			// Part C: prerequisites are always satisfied; transition to processing.
			rec.State = StateProcessing
			fallthrough

		case StateProcessing:
			ev, evErr := d.dag.Get(rec.EventID)
			if evErr != nil {
				return fmt.Errorf("dispatch: recovery: event %s not in DAG: %w",
					rec.EventID, evErr)
			}

			for name, status := range rec.Consumers {
				if status != ConsumerPending {
					continue
				}
				c, ok := consumers[name]
				if !ok {
					continue
				}
				probeResult, probeErr := c.RecoveryProbe(ctx, ev)
				if probeErr != nil {
					return fmt.Errorf("dispatch: recovery probe %s for event %s: %w",
						name, rec.EventID, probeErr)
				}
				switch probeResult {
				case RecoveryCompleted:
					rec.Consumers[name] = ConsumerApplied
				case RecoveryNotStarted:
					rec.Consumers[name] = ConsumerFailedRetryable
				}
			}
		}

		rec.State = computeTopLevelState(rec.Consumers)
		if err := d.store.PutAdmission(rec.Key, rec); err != nil {
			return fmt.Errorf("dispatch: recovery persist for %s: %w", rec.EventID, err)
		}
	}
	return nil
}

// isNotFound checks whether err is a "key not found" error from BadgerDB
// or the store layer. The store returns badger.ErrKeyNotFound directly.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "Key not found"
}
