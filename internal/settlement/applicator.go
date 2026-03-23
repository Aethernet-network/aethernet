package settlement

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/metrics"
	"github.com/Aethernet-network/aethernet/internal/staking"
)

// appliedStore persists the set of applied target event IDs so the applicator
// can recover from crashes without re-applying settlements.
type appliedStore interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

// eventLookup retrieves a canonical event from the DAG by its ID.
type eventLookup func(event.EventID) (*event.Event, error)

// escrowHolder is the minimal escrow interface needed by the applicator to
// register escrow locks on settlement. Injected to avoid a Core→Application
// import.
type escrowHolder interface {
	Hold(taskID string, posterID crypto.AgentID, amount uint64) error
	IsLocked(taskID string) bool
}

// taskSettler executes task-specific settlement side effects. Injected by
// the Application layer to avoid a Core→Application import.
type taskSettler func(payload TaskSettlementPayload) error

// Applicator is the ONLY component authorized to mutate ledger state in
// response to consensus-finalized Settlement events. It is deterministic:
// given the same SettlementRecord and the same pre-state, every node
// produces the same post-state.
//
// Applied set is keyed solely by TargetEventID. Once any valid Settlement
// for a target is applied, all future records for the same target are
// ignored.
type Applicator struct {
	transfer   *ledger.TransferLedger
	generation *ledger.GenerationLedger
	identity   *identity.Registry
	eventBus   *eventbus.Bus
	lookup     eventLookup
	store      appliedStore

	// taskSettlerFn is injected by the Application layer to handle task-
	// specific settlement (escrow release, reputation, generation ledger).
	taskSettlerFn taskSettler

	// Escrow — optional, nil-safe.
	escrow escrowHolder

	// Economics — all optional, nil-safe.
	feeCollector *fees.Collector
	treasuryID   crypto.AgentID
	stakeManager *staking.StakeManager
	nodeMetrics  *metrics.AetherNetMetrics

	// applied tracks TargetEventIDs that have been settled.
	applied map[event.EventID]struct{}
	// deferred tracks TargetEventIDs whose target event is not yet in the
	// local DAG. The reconciliation loop retries these periodically.
	deferred map[event.EventID]*SettlementPayload
	mu       sync.Mutex

	// Metrics counters.
	appliedTotal    uint64
	duplicatedTotal uint64
	deferredTotal   uint64
	failedTotal     uint64
	stopCh          chan struct{}
	stopOnce        sync.Once
}

// NewApplicator creates a SettlementApplicator backed by the given ledgers.
func NewApplicator(
	tl *ledger.TransferLedger,
	gl *ledger.GenerationLedger,
	reg *identity.Registry,
	lookup eventLookup,
) *Applicator {
	return &Applicator{
		transfer:   tl,
		generation: gl,
		identity:   reg,
		lookup:     lookup,
		applied:    make(map[event.EventID]struct{}),
		deferred:   make(map[event.EventID]*SettlementPayload),
		stopCh:     make(chan struct{}),
	}
}

// SetStore wires persistence for the applied set. On crash recovery,
// LoadApplied restores the set so settlements are not re-applied.
func (a *Applicator) SetStore(s appliedStore) { a.store = s }

// SetEventBus wires the event bus for settlement notifications.
func (a *Applicator) SetEventBus(bus *eventbus.Bus) { a.eventBus = bus }

// SetTaskSettler wires the Application-layer task settlement handler.
func (a *Applicator) SetTaskSettler(fn taskSettler) { a.taskSettlerFn = fn }

// SetFeeCollector wires fee collection into the settlement pipeline.
func (a *Applicator) SetFeeCollector(fc *fees.Collector, treasuryID crypto.AgentID) {
	a.feeCollector = fc
	a.treasuryID = treasuryID
}

// SetStakeManager wires staking activity recording.
func (a *Applicator) SetStakeManager(sm *staking.StakeManager) { a.stakeManager = sm }

// SetMetrics wires metrics counters.
func (a *Applicator) SetMetrics(m *metrics.AetherNetMetrics) { a.nodeMetrics = m }

// SetEscrowManager wires the escrow manager so settled escrow-lock transfers
// register entries in the local escrow manager for subsequent release/refund.
func (a *Applicator) SetEscrowManager(e escrowHolder) { a.escrow = e }

// Apply processes a consensus-finalized SettlementPayload. It is the ONLY
// entry point for ledger mutation in response to consensus.
//
// Idempotent: returns nil if the TargetEventID is already in the applied set.
// If the target event is not yet in the local DAG, the settlement is deferred
// and retried by the reconciliation loop.
func (a *Applicator) Apply(sp *SettlementPayload) error {
	targetID := event.EventID(sp.TargetEventID)

	a.mu.Lock()
	if _, done := a.applied[targetID]; done {
		a.duplicatedTotal++
		a.mu.Unlock()
		slog.Debug("settlement: already applied", "target", targetID)
		return nil
	}
	a.mu.Unlock()

	// Look up the original event to determine type and extract payload.
	target, err := a.lookup(targetID)
	if err != nil {
		// Target not yet in local DAG — defer for reconciliation.
		a.mu.Lock()
		if _, exists := a.deferred[targetID]; !exists {
			a.deferred[targetID] = sp
			a.deferredTotal++
		}
		a.mu.Unlock()
		slog.Info("settlement: target not in DAG, deferred",
			"target", targetID, "err", err)
		return nil
	}

	return a.applyToTarget(sp, target)
}

func (a *Applicator) applyToTarget(sp *SettlementPayload, target *event.Event) error {
	targetID := event.EventID(sp.TargetEventID)
	verdict := SettlementVerdict(sp.Verdict)

	switch target.Type {
	case event.EventTypeTransfer:
		if err := a.applyTransfer(targetID, verdict, target); err != nil {
			a.mu.Lock()
			a.failedTotal++
			a.mu.Unlock()
			return err
		}

	case event.EventTypeGeneration:
		if err := a.applyGeneration(targetID, verdict, sp.VerifiedValue, target); err != nil {
			a.mu.Lock()
			a.failedTotal++
			a.mu.Unlock()
			return err
		}

	case event.EventTypeTaskSettlement:
		if err := a.applyTaskSettlement(target, verdict); err != nil {
			a.mu.Lock()
			a.failedTotal++
			a.mu.Unlock()
			return err
		}

	default:
		// Unknown target type — log and skip.
		slog.Warn("settlement: unknown target event type",
			"target", targetID, "type", target.Type)
	}

	// Identity updates (best-effort).
	agentID := crypto.AgentID(target.AgentID)
	domain := string(target.Type)
	if verdict == VerdictAccepted {
		_ = a.identity.RecordTaskCompletion(agentID, sp.VerifiedValue, domain)
	} else {
		_ = a.identity.RecordTaskFailure(agentID, domain)
	}

	// Economic side effects — single authority for fee collection, staking,
	// and metrics across all nodes.
	if verdict == VerdictAccepted {
		// Fee collection.
		if a.feeCollector != nil && sp.VerifiedValue > 0 {
			switch target.Type {
			case event.EventTypeTransfer:
				if tp, err := event.GetPayload[event.TransferPayload](target); err == nil {
					fee, burned := a.feeCollector.CollectFeeFromRecipient(
						crypto.AgentID(tp.ToAgent), tp.Amount, "", a.treasuryID,
					)
					if a.nodeMetrics != nil && fee > 0 {
						a.nodeMetrics.FeesCollected.Add(fee)
						a.nodeMetrics.FeesBurned.Add(burned)
						a.nodeMetrics.FeesToTreasury.Add(fee * fees.TreasuryShare / 100)
					}
				}
			case event.EventTypeGeneration:
				fee := fees.CalculateFee(sp.VerifiedValue)
				burned := fee - fee*fees.ValidatorShare/100 - fee*fees.TreasuryShare/100
				a.feeCollector.TrackFee(fee, burned, fee*fees.TreasuryShare/100)
				if a.nodeMetrics != nil && fee > 0 {
					a.nodeMetrics.FeesCollected.Add(fee)
					a.nodeMetrics.FeesBurned.Add(burned)
					a.nodeMetrics.FeesToTreasury.Add(fee * fees.TreasuryShare / 100)
				}
			}
		}
		// Staking activity.
		if a.stakeManager != nil {
			a.stakeManager.RecordActivity(agentID)
		}
		// Metrics.
		if a.nodeMetrics != nil {
			a.nodeMetrics.TransactionsSettled.Inc()
			a.nodeMetrics.TransactionVolume.Add(sp.VerifiedValue)
		}
	} else {
		if a.nodeMetrics != nil {
			a.nodeMetrics.TransactionsReversed.Inc()
		}
	}

	// EventBus notification.
	if a.eventBus != nil {
		a.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeVerification,
			Timestamp: time.Now(),
			Data: map[string]any{
				"event_id": sp.TargetEventID,
				"agent_id": target.AgentID,
				"verdict":  sp.Verdict,
			},
		})
	}

	// Mark as applied AFTER all mutations succeed.
	a.mu.Lock()
	a.applied[targetID] = struct{}{}
	delete(a.deferred, targetID) // clear if it was deferred
	a.appliedTotal++
	a.mu.Unlock()

	// Persist applied state for crash recovery.
	if a.store != nil {
		key := "settlement:applied:" + string(targetID)
		_ = a.store.PutMeta(key, []byte(sp.Verdict))
	}

	slog.Info("settlement: applied",
		"target", targetID, "type", target.Type, "verdict", sp.Verdict)
	return nil
}

func (a *Applicator) applyTransfer(targetID event.EventID, verdict SettlementVerdict, target *event.Event) error {
	// Record in the ledger first (idempotent — no-op if already recorded).
	// Submit() no longer records; the applicator is the ONLY code that
	// creates ledger entries for canonical settlement.
	if err := a.transfer.RecordFromSync(target); err != nil {
		return fmt.Errorf("settlement: record transfer %s: %w", targetID, err)
	}
	settleState := event.SettlementSettled
	if verdict != VerdictAccepted {
		settleState = event.SettlementAdjusted
	}
	if err := a.transfer.Settle(targetID, settleState); err != nil {
		return err
	}

	// Post-settlement bookkeeping for accepted transfers.
	if verdict == VerdictAccepted {
		if tp, err := event.GetPayload[event.TransferPayload](target); err == nil {
			switch tp.Reason {
			case "escrow-lock":
				// Register escrow entry so release/refund can find it.
				if a.escrow != nil && tp.TaskID != "" {
					if !a.escrow.IsLocked(tp.TaskID) {
						if err := a.escrow.Hold(tp.TaskID, crypto.AgentID(tp.FromAgent), tp.Amount); err != nil {
							slog.Warn("settlement: escrow hold failed",
								"task_id", tp.TaskID, "err", err)
						}
					}
				}
			case "stake-lock":
				// agent → staking-pool: record canonical stake.
				if a.stakeManager != nil {
					a.stakeManager.RecordCanonicalStake(crypto.AgentID(tp.FromAgent), tp.Amount)
				}
			case "stake-unlock":
				// staking-pool → agent: record canonical unstake.
				if a.stakeManager != nil {
					a.stakeManager.RecordCanonicalUnstake(crypto.AgentID(tp.ToAgent), tp.Amount)
				}
			}
		}
	}

	return nil
}

func (a *Applicator) applyGeneration(targetID event.EventID, verdict SettlementVerdict, verifiedValue uint64, target *event.Event) error {
	// Record in the ledger first (idempotent).
	if err := a.generation.RecordFromSync(target); err != nil {
		return fmt.Errorf("settlement: record generation %s: %w", targetID, err)
	}
	if verdict == VerdictAccepted {
		return a.generation.Verify(targetID, verifiedValue)
	}
	return a.generation.Reject(targetID)
}

func (a *Applicator) applyTaskSettlement(target *event.Event, verdict SettlementVerdict) error {
	if verdict != VerdictAccepted {
		return nil // rejected task settlements are no-ops (escrow stays held)
	}
	if a.taskSettlerFn == nil {
		return nil // task settlement not wired (e.g. passive node)
	}
	var payload TaskSettlementPayload
	if err := json.Unmarshal(target.Payload, &payload); err != nil {
		return fmt.Errorf("settlement: decode TaskSettlementPayload: %w", err)
	}
	return a.taskSettlerFn(payload)
}

// IsApplied reports whether a settlement for the given TargetEventID has
// already been applied.
func (a *Applicator) IsApplied(targetID event.EventID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, done := a.applied[targetID]
	return done
}

// Start launches the periodic reconciliation goroutine that retries deferred
// settlements whose targets have arrived in the DAG since the deferral.
func (a *Applicator) Start() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-a.stopCh:
				return
			case <-ticker.C:
				a.reconcile()
			}
		}
	}()
}

// Stop signals the reconciliation goroutine to exit.
func (a *Applicator) Stop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
}

func (a *Applicator) reconcile() {
	a.mu.Lock()
	// Snapshot deferred set under lock.
	snapshot := make(map[event.EventID]*SettlementPayload, len(a.deferred))
	for id, sp := range a.deferred {
		snapshot[id] = sp
	}
	a.mu.Unlock()

	for targetID, sp := range snapshot {
		target, err := a.lookup(targetID)
		if err != nil {
			continue // still not in DAG
		}
		if err := a.applyToTarget(sp, target); err != nil {
			slog.Warn("settlement: reconcile failed",
				"target", targetID, "err", err)
		}
	}
}

// LoadApplied restores the applied set from the store on node restart.
func (a *Applicator) LoadApplied(allMeta func(prefix string) (map[string][]byte, error)) {
	entries, err := allMeta("settlement:applied:")
	if err != nil {
		slog.Warn("settlement: failed to load applied set", "err", err)
		return
	}
	a.mu.Lock()
	for key := range entries {
		// key is "settlement:applied:<targetEventID>"
		targetID := event.EventID(key[len("settlement:applied:"):])
		a.applied[targetID] = struct{}{}
	}
	a.mu.Unlock()
	slog.Info("settlement: loaded applied set", "count", len(entries))
}

// Metrics returns the current settlement counters.
func (a *Applicator) Metrics() (applied, duplicated, deferred, failed uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.appliedTotal, a.duplicatedTotal, a.deferredTotal, a.failedTotal
}
