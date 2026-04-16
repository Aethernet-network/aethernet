package dispatch

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// addToDeferralIndex records that the admission at admissionKey is waiting
// for each EventID in missing. Must be called with d.mu held.
func (d *Dispatcher) addToDeferralIndex(missing []event.EventID, admissionKey string) {
	for _, pid := range missing {
		d.deferralIndex[pid] = append(d.deferralIndex[pid], admissionKey)
	}
}

// removeFromDeferralIndex removes admissionKey from all entries. Must be
// called with d.mu held.
func (d *Dispatcher) removeFromDeferralIndex(admissionKey string) {
	for pid, keys := range d.deferralIndex {
		filtered := keys[:0]
		for _, k := range keys {
			if k != admissionKey {
				filtered = append(filtered, k)
			}
		}
		if len(filtered) == 0 {
			delete(d.deferralIndex, pid)
		} else {
			d.deferralIndex[pid] = filtered
		}
	}
}

// rebuildDeferralIndex populates the in-memory deferral index from
// persisted admission records. Called during Recover.
// Must be called with d.mu held.
func (d *Dispatcher) rebuildDeferralIndex(records []*AdmissionRecord) {
	d.deferralIndex = make(map[event.EventID][]string)
	for _, rec := range records {
		if rec.State == StateReservedPendingPrereqs && len(rec.MissingPrerequisites) > 0 {
			d.addToDeferralIndex(rec.MissingPrerequisites, rec.Key)
		}
	}
}

// NotifyProjection is called when a new event is committed to the DAG.
// It re-checks deferred admissions waiting on the projected event and
// transitions them to processing if all prerequisites are now satisfied.
//
// Thread-safe; intended to be called from the DAG's onCommit hook.
func (d *Dispatcher) NotifyProjection(ctx context.Context, projectedEventID event.EventID) {
	d.mu.Lock()
	waitingKeys := d.deferralIndex[projectedEventID]
	if len(waitingKeys) == 0 {
		d.mu.Unlock()
		return
	}
	// Copy the slice to avoid holding the lock during re-admission.
	keys := make([]string, len(waitingKeys))
	copy(keys, waitingKeys)
	d.mu.Unlock()

	for _, key := range keys {
		rec, err := d.store.GetAdmission(key)
		if err != nil {
			slog.Warn("dispatch: notify projection: get admission failed",
				"key", key, "err", err)
			continue
		}
		if rec.State != StateReservedPendingPrereqs {
			continue
		}

		// Re-check which prerequisites are still missing.
		var stillMissing []event.EventID
		for _, pid := range rec.MissingPrerequisites {
			if _, dagErr := d.dag.Get(pid); dagErr != nil {
				stillMissing = append(stillMissing, pid)
			}
		}

		d.mu.Lock()
		if len(stillMissing) == 0 {
			d.removeFromDeferralIndex(key)
			rec.MissingPrerequisites = nil
			rec.State = StateProcessing
		} else {
			rec.MissingPrerequisites = stillMissing
		}
		d.mu.Unlock()

		if err := d.store.PutAdmission(key, rec); err != nil {
			slog.Error("dispatch: notify projection: persist failed",
				"key", key, "err", err)
			continue
		}

		// If all prerequisites are now satisfied, invoke consumers.
		if rec.State == StateProcessing {
			ev, evErr := d.dag.Get(rec.EventID)
			if evErr != nil {
				slog.Error("dispatch: notify projection: event not in DAG",
					"event_id", rec.EventID, "err", evErr)
				continue
			}
			if err := d.invokeConsumers(ctx, ev, key, rec); err != nil {
				slog.Warn("dispatch: notify projection: invoke consumers failed",
					"key", key, "err", err)
			}
		}

		// Check deferral thresholds for records still deferred.
		if rec.State == StateReservedPendingPrereqs {
			d.checkDeferralThresholds(ctx, rec)
		}
	}
}

// checkDeferralThresholds checks complaint and failover thresholds for a
// deferred admission record. Emits evidence at complaint threshold.
// The failover threshold (fail-startup) is checked only during Recover.
func (d *Dispatcher) checkDeferralThresholds(ctx context.Context, rec *AdmissionRecord) {
	currentEpoch := d.epochFn()
	if rec.CreatedAtEpoch > currentEpoch {
		return
	}
	age := currentEpoch - rec.CreatedAtEpoch

	if age >= DeferralComplaintThreshold && !rec.EvidenceEmitted {
		d.emitWithholdingEvidence(ctx, rec, currentEpoch)
	}
}

// emitWithholdingEvidence creates and publishes a PrerequisiteWithholding
// canonical event via the injected EvidenceEmitter. Marks the record's
// EvidenceEmitted flag to prevent duplicates.
func (d *Dispatcher) emitWithholdingEvidence(ctx context.Context, rec *AdmissionRecord, currentEpoch uint64) {
	d.mu.RLock()
	emitter := d.evidenceEmitter
	d.mu.RUnlock()

	if emitter == nil {
		slog.Warn("dispatch: evidence emitter not configured; cannot emit PrerequisiteWithholding",
			"event_id", rec.EventID)
		return
	}

	payload := event.PrerequisiteWithholdingPayload{
		Version:              1,
		StuckEventID:         rec.EventID,
		StuckEventType:       rec.EventType,
		MissingPrerequisites: rec.MissingPrerequisites,
		DeferredSinceEpoch:   rec.CreatedAtEpoch,
		CurrentEpoch:         currentEpoch,
	}

	ev, err := event.New(
		event.EventTypePrerequisiteWithholding,
		nil,
		payload,
		"dispatcher",
		nil,
		0,
	)
	if err != nil {
		slog.Error("dispatch: failed to construct PrerequisiteWithholding event",
			"event_id", rec.EventID, "err", err)
		return
	}

	if err := emitter(ev); err != nil {
		slog.Error("dispatch: failed to emit PrerequisiteWithholding evidence",
			"event_id", rec.EventID, "err", err)
		return
	}

	rec.EvidenceEmitted = true
	if err := d.store.PutAdmission(rec.Key, rec); err != nil {
		slog.Error("dispatch: failed to persist evidence-emitted flag",
			"key", rec.Key, "err", err)
	}

	slog.Info("dispatch: emitted PrerequisiteWithholding evidence",
		"stuck_event", rec.EventID, "deferred_since_epoch", rec.CreatedAtEpoch,
		"current_epoch", currentEpoch)
}
