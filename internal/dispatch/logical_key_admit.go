package dispatch

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// admitLogicalKey runs the F4B logical-key admission flow for ev.
//
// Per F4 plan v2 §4.5:
//   1. Snapshot the registered logical-key consumers in lex-sorted order
//      (E.P1 invariant — deterministic across nodes).
//   2. Filter to those whose Interested(ev) returns true.
//   3. For each interested consumer, run admitOneLogicalKey, which is
//      the per-consumer logical-key state machine.
//
// Cheap fast-path: when no logical-key consumers are registered the
// snapshot is empty and the function returns immediately. The
// performance non-regression gate requires that
// BenchmarkAdmit_FreshContentHash NOT regress >5% from the F4A baseline
// just from the addition of this branch — empty-map iteration plus the
// nil-slice early return is well below that ceiling.
//
// Per C-6: VerifyAnchor must be called before logical-key consumers
// observe an event. Skipped on the empty-snapshot fast path because no
// observation occurs there; called once when at least one consumer is
// interested (mirrors the content-hash flow's invariant that anchor
// verification gates every admission that mutates state).
func (d *Dispatcher) admitLogicalKey(ctx context.Context, ev *event.Event) error {
	d.mu.RLock()
	consumers := d.snapshotInterestedLogicalKeyConsumersLocked(ev)
	d.mu.RUnlock()

	if len(consumers) == 0 {
		return nil
	}

	if err := VerifyAnchor(d.dag, d.currentAnchor()); err != nil {
		return err
	}

	for _, c := range consumers {
		if err := d.admitOneLogicalKey(ctx, ev, c); err != nil {
			return err
		}
	}
	return nil
}

// snapshotInterestedLogicalKeyConsumersLocked returns the subset of
// registered logical-key consumers whose Interested(ev) returns true,
// in lex-sorted Name order. Caller must hold d.mu (read or write).
//
// E.P1 (F4 plan §6 / locked-invariant review §3.4): iterate consumer
// names in lexicographic order so the returned slice is deterministic
// across nodes. Map iteration order would otherwise leak into the
// sequence of admitOneLogicalKey invocations, which is observable
// cross-node when consumers count > 1 (e.g., when both
// TVConsensusLogicalKeyConsumer and a future SettlementLogicalKey
// consumer share an event type).
func (d *Dispatcher) snapshotInterestedLogicalKeyConsumersLocked(ev *event.Event) []LogicalKeyConsumer {
	if len(d.logicalKeyConsumers) == 0 {
		return nil
	}

	names := make([]string, 0, len(d.logicalKeyConsumers))
	// safe: collected then lex-sorted before iteration; no iteration-order leak
	for name := range d.logicalKeyConsumers {
		names = append(names, name)
	}
	sort.Strings(names)

	var interested []LogicalKeyConsumer
	for _, name := range names {
		c := d.logicalKeyConsumers[name]
		if c.Interested(ev) {
			interested = append(interested, c)
		}
	}
	return interested
}

// admitOneLogicalKey runs the per-(consumer, key) admission state
// machine for one logical-key consumer observing one event.
//
// Per F4 plan v2 §4.5 steps (a) through (g):
//   (a) Compute key = consumer.Key(ev).
//   (b) Reserve-or-load the per-(consumer, key) admission record.
//   (c) If already StateApplied: return without re-Apply (per-key
//       Apply guarantee). Future events for this key are observed but
//       do not trigger Apply.
//   (d) Query canonical RoundState via consumer.RoundState.
//   (e) consumer.IsComplete(rs). If false: persist as StateProcessing
//       (the observation is recorded; no Apply invocation).
//   (f) If complete and not yet applied: consumer.DeriveOutcome(rs).
//   (g) consumer.Apply(ctx, key, outcome). On success, mark
//       StateApplied. On failure, mark StateFailedRetryable; future
//       observations of an event for this key (or a re-Admit of any
//       event for this key) drive a retry.
//
// Critical: Apply receives the derived Outcome, NOT ev.Payload. This
// is the C-17 enforcement point — by passing the derived Outcome, the
// dispatcher prevents the consumer from accidentally deriving
// canonical state from the triggering event's (potentially advisory)
// payload fields.
//
// **Per-(consumer, key) lock** (F5 5B post-#133 LK race fix, Path A):
// the read-modify-write region (reserve → gate → IsComplete →
// DeriveOutcome → Apply → persist) is serialized by a per-storeKey
// mutex from d.keyLocks. Without it, two concurrent commit-bus
// workers processing byte-distinct events for the same logical key
// could both pass the line-127 StateApplied gate and both invoke
// consumer.Apply.
//
// Defense-in-depth framing per d.keyLocks doc: the lock is INTRA-NODE
// only; cross-node correctness rests on each LK consumer's Apply
// canonicality + ledger ErrDuplicateEntry idempotency + the F4B
// contract that LK Apply is idempotent or no-op for byte-distinct
// events with the same logical key. The lock prevents wasted intra-
// node Apply calls on race-loss; it is NOT the canonical-correctness
// mechanism. Same V-1-class layer separation as
// internal/escrow/applicator.go's recordLocks.
func (d *Dispatcher) admitOneLogicalKey(ctx context.Context, ev *event.Event, c LogicalKeyConsumer) error {
	key, err := c.Key(ev)
	if err != nil {
		return fmt.Errorf("dispatch: logical-key extraction for %s: %w", c.Name(), err)
	}

	storeKey := LogicalAdmissionKey(c.Name(), key)

	// Acquire per-(consumer, key) intra-node lock for the duration of
	// the read-modify-write region below. Defense-in-depth only;
	// canonical correctness is guaranteed by downstream idempotency
	// per d.keyLocks doc.
	lockI, _ := d.keyLocks.LoadOrStore(storeKey, &sync.Mutex{})
	lock := lockI.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	rec, err := d.reserveOrLoadLogical(storeKey, ev, c, key)
	if err != nil {
		return err
	}

	// Per-key Apply guarantee (C-1 generalized to (consumer, key)):
	// once StateApplied, no future event for this key invokes Apply
	// again. Returning here also handles the "later observation" case
	// where a second byte-distinct canonical event projects into the
	// same logical key after the first one already drove Apply.
	if rec.State == StateApplied {
		return nil
	}

	rs, err := c.RoundState(ctx, key)
	if err != nil {
		return fmt.Errorf("dispatch: RoundState for %s/%s: %w", c.Name(), key, err)
	}

	complete, err := c.IsComplete(rs)
	if err != nil {
		return fmt.Errorf("dispatch: IsComplete for %s/%s: %w", c.Name(), key, err)
	}

	if !complete {
		// Record the observation; defer Apply until completeness is reached.
		rec.State = StateProcessing
		rec.Consumers[c.Name()] = ConsumerPending
		if err := d.store.PutAdmission(storeKey, rec); err != nil {
			return fmt.Errorf("dispatch: persist incomplete logical-key %s/%s: %w", c.Name(), key, err)
		}
		return nil
	}

	outcome, err := c.DeriveOutcome(rs)
	if err != nil {
		return fmt.Errorf("dispatch: DeriveOutcome for %s/%s: %w", c.Name(), key, err)
	}

	// Per-key Apply (atomic per C-11). Apply once; failures are
	// recorded as StateFailedRetryable so a subsequent re-Admit of any
	// event for this key drives a retry (mirrors the F3-B
	// failed-retryable retry pattern at the (consumer, event) grain).
	//
	// triggerEventID is ev.ID — the canonical event whose admission
	// satisfied the IsComplete gate. Threaded through to Apply per the
	// LogicalKeyConsumer.Apply contract (F5 5B Shape 3).
	if applyErr := safeApplyLogicalKey(ctx, c, ev.ID, key, outcome); applyErr != nil {
		rec.State = StateFailedRetryable
		rec.Consumers[c.Name()] = ConsumerFailedRetryable
		if persistErr := d.store.PutAdmission(storeKey, rec); persistErr != nil {
			return fmt.Errorf("dispatch: persist after logical-key apply failure for %s/%s: %w", c.Name(), key, persistErr)
		}
		return fmt.Errorf("dispatch: logical-key Apply for %s/%s: %w", c.Name(), key, applyErr)
	}

	rec.State = StateApplied
	rec.Consumers[c.Name()] = ConsumerApplied
	if err := d.store.PutAdmission(storeKey, rec); err != nil {
		return fmt.Errorf("dispatch: persist after logical-key apply success for %s/%s: %w", c.Name(), key, err)
	}
	return nil
}

// reserveOrLoadLogical atomically reads or creates the per-
// (consumer, key) admission record for a logical-key consumer.
//
// Mirrors reserveOrLoad's read-or-create discipline. Validates that
// any pre-existing record at storeKey has Strategy ==
// AdmissionStrategyLogicalKey; a mismatch here would indicate a
// LogicalAdmissionKey collision with the content-hash key space (which
// the "lk:" sub-prefix is designed to prevent) and is fail-loud rather
// than fail-silent.
func (d *Dispatcher) reserveOrLoadLogical(storeKey string, ev *event.Event, c LogicalKeyConsumer, key LogicalKey) (*AdmissionRecord, error) {
	existing, err := d.store.GetAdmission(storeKey)
	if err != nil {
		if isNotFound(err) {
			return &AdmissionRecord{
				SchemaVersion:  AdmissionCurrentVersion,
				Key:            storeKey,
				Strategy:       AdmissionStrategyLogicalKey,
				LogicalKey:     key,
				State:          StateProcessing,
				DAGAnchor:      d.currentAnchor(),
				Consumers:      map[string]PerConsumerStatus{c.Name(): ConsumerPending},
				EventID:        ev.ID,
				EventType:      string(ev.Type),
				CreatedAtEpoch: d.epochFn(),
			}, nil
		}
		return nil, fmt.Errorf("dispatch: get logical admission %s: %w", storeKey, err)
	}
	if existing.Strategy != AdmissionStrategyLogicalKey {
		return nil, fmt.Errorf("dispatch: storeKey collision — %s exists as %s strategy, want logical-key", storeKey, existing.Strategy)
	}
	if existing.Consumers == nil {
		existing.Consumers = make(map[string]PerConsumerStatus, 1)
	}
	if _, ok := existing.Consumers[c.Name()]; !ok {
		existing.Consumers[c.Name()] = ConsumerPending
	}
	return existing, nil
}

// safeApplyLogicalKey calls consumer.Apply with panic recovery,
// mirroring safeApply for content-hash consumers. A panic is treated
// as a failed-retryable error.
func safeApplyLogicalKey(ctx context.Context, c LogicalKeyConsumer, triggerEventID event.EventID, key LogicalKey, outcome Outcome) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dispatch: logical-key consumer %s panicked on key %s: %v", c.Name(), key, r)
		}
	}()
	return c.Apply(ctx, triggerEventID, key, outcome)
}
