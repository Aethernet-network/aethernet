// Package ocs implements the Optimistic Capability Settlement engine for AetherNet.
//
// # Mental model
//
// Think of a 1970s bank clearing house: cheques are accepted immediately on good-faith
// trust, cleared in overnight batches, and bounced cheques trigger corrections the
// next morning. No one waits at the counter while the bank phones the issuing branch.
//
// OCS applies the same principle to AI agent transactions. When an agent submits a
// Transfer or Generation event, the Engine records it optimistically and allows it to
// take effect immediately. A verification agent then inspects the work asynchronously
// and delivers a verdict. If the verdict is positive the event settles permanently;
// if negative the ledger entry is adjusted and the agent's reputation is penalised.
// Events that receive no verdict before their deadline are treated as failed.
//
// # Concurrency model
//
// A single background goroutine owns the verdict processing loop. External callers
// submit verdicts via SubmitVerification, which places them on a buffered channel.
// The background goroutine drains the channel and also fires a 5-second ticker to
// sweep for deadline-exceeded items. ProcessResult may also be called directly for
// synchronous settlement (useful in tests and trusted validator paths).
//
// The pending map is protected by a sync.RWMutex; all other state is either
// immutable after construction or owned by the background goroutine.
package ocs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/eventbus"
	"github.com/aethernet/core/internal/fees"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/staking"
	"github.com/aethernet/core/internal/validation"
)

// ocsPersistence is the subset of store.Store used by Engine.
// *store.Store from the store package satisfies this interface.
type ocsPersistence interface {
	PutPending(item *PendingItem) error
	DeletePending(id event.EventID) error
	AllPending() ([]*PendingItem, error)
}

// Sentinel errors for programmatic handling by callers.
var (
	// ErrAlreadyRunning is returned by Start when the engine is already active.
	ErrAlreadyRunning = errors.New("ocs: engine already running")

	// ErrNotRunning is returned by SubmitVerification when the engine has not
	// been started or has been stopped.
	ErrNotRunning = errors.New("ocs: engine not running")

	// ErrQueueFull is returned by Submit when the pending map has reached
	// MaxPendingItems and cannot accept further events.
	ErrQueueFull = errors.New("ocs: pending queue full")

	// ErrInsufficientStake is returned by Submit when the event's StakeAmount
	// is below the configured MinStakeRequired threshold.
	ErrInsufficientStake = errors.New("ocs: insufficient stake")

	// ErrUnsupportedEventType is returned by Submit for event types that the
	// engine does not know how to settle (non-Transfer, non-Generation).
	ErrUnsupportedEventType = errors.New("ocs: unsupported event type")

	// ErrAlreadyPending is returned by Submit when the event ID is already
	// tracked in the pending map.
	ErrAlreadyPending = errors.New("ocs: event already pending")

	// ErrNotPending is returned by ProcessResult when the event ID is not in
	// the pending map — either already settled, expired, or unknown.
	ErrNotPending = errors.New("ocs: event not in pending")

	// ErrSelfDealing is returned by ProcessResult when the verifier is a party
	// to the transaction being verified (sender or recipient).
	ErrSelfDealing = errors.New("ocs: validator cannot verify transactions they are party to")
)

// resultsBufferSize is the capacity of the internal verification results channel.
// Sized generously so that verification agents can submit verdicts without
// blocking while the background goroutine is busy processing a previous batch.
const resultsBufferSize = 256

// expiredCheckInterval is how often the background goroutine sweeps for
// pending items that have exceeded their verification deadline.
const expiredCheckInterval = 5 * time.Second

// VerificationResult carries the verdict delivered by a verification agent.
// It is the primary input to ProcessResult and flows through the results channel.
type VerificationResult struct {
	// EventID identifies the pending event being adjudicated.
	EventID event.EventID

	// Verdict is true if the event's claims are valid, false if fraudulent or
	// unverifiable. A false verdict triggers an Adjusted settlement.
	Verdict bool

	// VerifiedValue is the amount confirmed by the verifier, in micro-AET.
	// Meaningful only for Generation events; ignored for Transfer settlements.
	// May differ from the event's ClaimedValue.
	VerifiedValue uint64

	// VerifierID is the AgentID of the verification agent issuing this verdict.
	VerifierID crypto.AgentID

	// Reason is an optional human-readable explanation of the verdict,
	// useful for audit trails and debugging.
	Reason string

	// Timestamp is when the verification was completed.
	Timestamp time.Time
}

// PendingItem tracks an event that has been accepted optimistically but not
// yet settled by a verification verdict.
type PendingItem struct {
	// EventID is the content-addressed ID of the tracked event.
	EventID event.EventID

	// EventType determines which ledger and settlement path applies.
	EventType event.EventType

	// AgentID is the originating agent whose reputation is updated on settlement.
	AgentID crypto.AgentID

	// Amount is the economic value at stake: Transfer amount or Generation
	// ClaimedValue, in micro-AET. Used for analytics and future slashing logic.
	Amount uint64

	// RecipientID is the other party to the transaction (ToAgent for transfers,
	// BeneficiaryAgent for generation). Used for anti-self-dealing checks.
	// Empty for events with no distinct recipient.
	RecipientID crypto.AgentID

	// OptimisticAt is the wall-clock time the event was accepted as Optimistic.
	// Combined with Deadline it determines whether the item has expired.
	OptimisticAt time.Time

	// Deadline is the maximum duration from OptimisticAt before the item is
	// treated as a failed verification. Set from EngineConfig.VerificationTimeout.
	Deadline time.Duration
}

// EngineConfig holds tunable parameters for the OCS settlement engine.
// All zero values are invalid; use DefaultConfig to obtain safe defaults.
type EngineConfig struct {
	// VerificationTimeout is the maximum time an event may remain in Optimistic
	// state before being automatically treated as failed. Expired items are swept
	// by the background goroutine on every CheckInterval tick.
	VerificationTimeout time.Duration

	// MaxPendingItems caps the size of the pending map. Submit rejects new events
	// when this limit is reached. This bounds memory consumption under high load
	// and back-pressures upstream submitters.
	MaxPendingItems int

	// AdjustmentPenalty is the severity of the reputation penalty (in micro-AET)
	// applied when a verification verdict is negative. Stored for audit and future
	// stake-slashing extensions; the immediate effect is delivered through
	// identity.RecordTaskFailure which reduces OptimisticTrustLimit by 15%.
	AdjustmentPenalty uint64

	// MinStakeRequired is the minimum StakeAmount an event must carry for the
	// engine to accept it. Events with insufficient stake are rejected at Submit
	// time. This ensures every pending item has non-trivial skin-in-the-game.
	MinStakeRequired uint64

	// CheckInterval controls how often the background goroutine sweeps for
	// expired pending items. Defaults to 5 seconds in production; set lower
	// in tests that exercise the expiry path. Zero falls back to 5 seconds.
	CheckInterval time.Duration
}

// DefaultConfig returns a conservative EngineConfig suitable for production use.
func DefaultConfig() *EngineConfig {
	return &EngineConfig{
		VerificationTimeout: 30 * time.Second,
		MaxPendingItems:     10000,
		AdjustmentPenalty:   500,
		MinStakeRequired:    1000,
		CheckInterval:       5 * time.Second,
	}
}

// Engine is the OCS clearing-house process for a single AetherNet node.
// It accepts optimistic events, tracks their verification deadlines, and drives
// them to settled or adjusted state based on incoming verification verdicts.
//
// Engine is safe for concurrent use by multiple goroutines once started.
type Engine struct {
	config     *EngineConfig
	transfer   *ledger.TransferLedger
	generation *ledger.GenerationLedger
	identity   *identity.Registry

	// Economics — all optional. When nil the settlement path is unchanged and
	// all existing behaviour is preserved (backward compatible).
	feeCollector *fees.Collector
	stakeManager *staking.StakeManager
	treasuryID   crypto.AgentID

	// eventBus — optional. When non-nil, settlement events are published for
	// real-time streaming. Nil-safe throughout ProcessResult.
	eventBus *eventbus.Bus

	pending   map[event.EventID]*PendingItem
	processed map[event.EventID]struct{} // tracks already-settled events for idempotency
	results   chan VerificationResult

	store ocsPersistence // optional; nil means in-memory only

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the background goroutine exits
}

// SetEconomics attaches optional fee collection and staking mechanics to the
// engine. Call before Start. All three values may be nil individually; the engine
// skips whichever components are absent so existing tests remain unaffected.
func (e *Engine) SetEconomics(fc *fees.Collector, sm *staking.StakeManager, treasuryID crypto.AgentID) {
	e.feeCollector = fc
	e.stakeManager = sm
	e.treasuryID = treasuryID
}

// SetStore attaches a persistence backend to the Engine. After this call Submit
// writes new PendingItems through to the store and ProcessResult deletes them.
// s must satisfy ocsPersistence; *store.Store from the store package does so.
func (e *Engine) SetStore(s ocsPersistence) {
	e.store = s
}

// SetEventBus wires an event bus into the Engine. Call before Start.
// When non-nil, ProcessResult publishes settlement, verification, and slash
// events for real-time streaming. Nil-safe: all existing tests are unaffected.
func (e *Engine) SetEventBus(b *eventbus.Bus) {
	e.eventBus = b
}

// NewEngine constructs an Engine backed by the provided ledgers and identity registry.
// config may be nil, in which case DefaultConfig is used. The engine is idle until
// Start is called.
func NewEngine(
	config *EngineConfig,
	tl *ledger.TransferLedger,
	gl *ledger.GenerationLedger,
	reg *identity.Registry,
) *Engine {
	if config == nil {
		config = DefaultConfig()
	}
	return &Engine{
		config:     config,
		transfer:   tl,
		generation: gl,
		identity:   reg,
		pending:    make(map[event.EventID]*PendingItem),
		processed:  make(map[event.EventID]struct{}),
		results:    make(chan VerificationResult, resultsBufferSize),
	}
}

// Start launches the background goroutine that processes verification results
// and sweeps expired pending items. Returns ErrAlreadyRunning if the engine
// is already active.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cancel != nil {
		return ErrAlreadyRunning
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	e.cancel = cancel
	e.done = make(chan struct{})

	go e.run()
	return nil
}

// Stop signals the background goroutine to exit and blocks until it has done so.
// It is a no-op if the engine is not running. After Stop returns the engine may
// be restarted by calling Start again.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.cancel == nil {
		e.mu.Unlock()
		return
	}
	cancel := e.cancel
	done := e.done
	e.cancel = nil
	e.mu.Unlock()

	cancel()
	<-done
}

// run is the background goroutine body. It drains the results channel and fires
// periodic expiry sweeps until the context is cancelled.
func (e *Engine) run() {
	defer close(e.done)

	checkInterval := e.config.CheckInterval
	if checkInterval <= 0 {
		checkInterval = expiredCheckInterval
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case result := <-e.results:
			_ = e.ProcessResult(result)
		case <-ticker.C:
			e.checkExpired()
		}
	}
}

// Submit accepts a Transfer or Generation event in Optimistic state into the
// clearing pipeline.
//
// It records the event in the appropriate ledger and adds it to the pending map.
// Submission is rejected if:
//   - the event's StakeAmount is below MinStakeRequired
//   - the pending map has reached MaxPendingItems
//   - the event is already tracked in the pending map
//   - the event type is not Transfer or Generation
//   - the ledger Record call fails (e.g., duplicate event ID)
func (e *Engine) Submit(ev *event.Event) error {
	if ev.StakeAmount < e.config.MinStakeRequired {
		return fmt.Errorf("%w: event has %d, minimum is %d",
			ErrInsufficientStake, ev.StakeAmount, e.config.MinStakeRequired)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.pending) >= e.config.MaxPendingItems {
		return fmt.Errorf("%w: capacity %d reached", ErrQueueFull, e.config.MaxPendingItems)
	}

	if _, exists := e.pending[ev.ID]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyPending, ev.ID)
	}

	var amount uint64

	var recipientID crypto.AgentID

	switch ev.Type {
	case event.EventTypeTransfer:
		// Balance check before recording. Extract payload to get the amount.
		tp, err := event.GetPayload[event.TransferPayload](ev)
		if err != nil {
			return fmt.Errorf("ocs: decode transfer payload: %w", err)
		}
		if err := e.transfer.BalanceCheck(crypto.AgentID(tp.FromAgent), tp.Amount); err != nil {
			return fmt.Errorf("ocs: %w", err)
		}
		if err := e.transfer.Record(ev); err != nil {
			return fmt.Errorf("ocs: ledger record failed: %w", err)
		}
		amount = tp.Amount
		recipientID = crypto.AgentID(tp.ToAgent)

	case event.EventTypeGeneration:
		if err := e.generation.Record(ev); err != nil {
			return fmt.Errorf("ocs: ledger record failed: %w", err)
		}
		gp, err := event.GetPayload[event.GenerationPayload](ev)
		if err == nil {
			amount = gp.ClaimedValue
			recipientID = crypto.AgentID(gp.BeneficiaryAgent)
		}

	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedEventType, ev.Type)
	}

	e.pending[ev.ID] = &PendingItem{
		EventID:      ev.ID,
		EventType:    ev.Type,
		AgentID:      crypto.AgentID(ev.AgentID),
		Amount:       amount,
		RecipientID:  recipientID,
		OptimisticAt: time.Now(),
		Deadline:     e.config.VerificationTimeout,
	}
	if e.store != nil {
		_ = e.store.PutPending(e.pending[ev.ID])
	}
	return nil
}

// SubmitVerification enqueues a verification verdict for asynchronous processing
// by the background goroutine. Returns ErrNotRunning if the engine has not been
// started. Blocks if the results channel is full and the engine is still active.
func (e *Engine) SubmitVerification(result VerificationResult) error {
	e.mu.RLock()
	running := e.cancel != nil
	ctx := e.ctx
	e.mu.RUnlock()

	if !running {
		return ErrNotRunning
	}

	select {
	case e.results <- result:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ocs: engine shutting down, verdict for %s dropped", result.EventID)
	}
}

// ProcessResult applies a verification verdict to the appropriate ledger and
// updates the originating agent's identity fingerprint. It removes the event
// from the pending map regardless of verdict.
//
// If Verdict is true:
//   - Transfer events are advanced to SettlementSettled.
//   - Generation events are advanced to SettlementSettled with VerifiedValue set.
//   - identity.RecordTaskCompletion is called for the originating agent.
//
// If Verdict is false:
//   - Transfer events are advanced to SettlementAdjusted.
//   - Generation events are advanced to SettlementAdjusted via Reject.
//   - identity.RecordTaskFailure is called, reducing OptimisticTrustLimit by 15%.
//     The configured AdjustmentPenalty is available for future stake-slashing.
//
// Identity updates are best-effort: if the agent is not registered in the local
// Registry the error is silently ignored rather than failing the settlement.
// Ledger errors (e.g., invalid state transitions) are propagated to the caller.
//
// Returns ErrNotPending if the event is not in the pending map.
func (e *Engine) ProcessResult(result VerificationResult) error {
	e.mu.Lock()
	// Idempotency: if already processed, return silently. This prevents the
	// double-settlement race where checkExpired and a real verdict both try
	// to settle the same event.
	if _, done := e.processed[result.EventID]; done {
		e.mu.Unlock()
		return nil
	}
	item, exists := e.pending[result.EventID]
	if !exists {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotPending, result.EventID)
	}

	// Anti-self-dealing: reject verifiers who are party to the transaction.
	// Skip when VerifierID is empty (expiry sweep path).
	if result.VerifierID != "" && !validation.CanValidate(result.VerifierID, item.AgentID, item.RecipientID) {
		e.mu.Unlock()
		return fmt.Errorf("%w: verifier=%s sender=%s recipient=%s",
			ErrSelfDealing, result.VerifierID, item.AgentID, item.RecipientID)
	}

	delete(e.pending, result.EventID)
	e.processed[result.EventID] = struct{}{}
	if e.store != nil {
		_ = e.store.DeletePending(result.EventID)
	}
	e.mu.Unlock()

	// Use the event type as the capability domain for identity updates.
	// This is a coarse-grained domain; callers may supply finer-grained
	// domains through richer VerificationResult metadata in future iterations.
	domain := string(item.EventType)

	if result.Verdict {
		switch item.EventType {
		case event.EventTypeTransfer:
			if err := e.transfer.Settle(result.EventID, event.SettlementSettled); err != nil {
				return fmt.Errorf("ocs: settle transfer %s: %w", result.EventID, err)
			}
			if e.eventBus != nil {
				e.eventBus.Publish(eventbus.Event{
					Type:      eventbus.EventTypeTransfer,
					Timestamp: time.Now(),
					Data:      map[string]any{"event_id": string(result.EventID), "agent_id": string(item.AgentID), "amount": item.Amount},
				})
			}
		case event.EventTypeGeneration:
			if err := e.generation.Verify(result.EventID, result.VerifiedValue); err != nil {
				return fmt.Errorf("ocs: verify generation %s: %w", result.EventID, err)
			}
			if e.eventBus != nil {
				e.eventBus.Publish(eventbus.Event{
					Type:      eventbus.EventTypeGeneration,
					Timestamp: time.Now(),
					Data:      map[string]any{"event_id": string(result.EventID), "agent_id": string(item.AgentID), "amount": item.Amount, "verified_value": result.VerifiedValue},
				})
			}
		}
		// Collect settlement fee when economics are wired in.
		if e.feeCollector != nil && item.Amount > 0 {
			e.feeCollector.CollectFee(item.Amount, result.VerifierID, e.treasuryID)
		}
		// Record activity for decay tracking.
		if e.stakeManager != nil {
			e.stakeManager.RecordActivity(item.AgentID)
		}
		// Best-effort: agent may not be registered on this node.
		_ = e.identity.RecordTaskCompletion(item.AgentID, result.VerifiedValue, domain)
	} else {
		switch item.EventType {
		case event.EventTypeTransfer:
			if err := e.transfer.Settle(result.EventID, event.SettlementAdjusted); err != nil {
				return fmt.Errorf("ocs: adjust transfer %s: %w", result.EventID, err)
			}
		case event.EventTypeGeneration:
			if err := e.generation.Reject(result.EventID); err != nil {
				return fmt.Errorf("ocs: reject generation %s: %w", result.EventID, err)
			}
		}
		// Slash the offending agent's stake and credit the amount to treasury.
		// Transfer defaults (sender exploited trust) → full slash + reset timestamp.
		// Other failures (bad generation claim) → 10% slash.
		if e.stakeManager != nil {
			var slashed uint64
			if item.EventType == event.EventTypeTransfer {
				slashed = e.stakeManager.SlashDefault(item.AgentID)
			} else {
				slashed = e.stakeManager.Slash(item.AgentID, 10)
			}
			if slashed > 0 && e.treasuryID != "" {
				_ = e.transfer.FundAgent(e.treasuryID, slashed)
			}
			if slashed > 0 && e.eventBus != nil {
				e.eventBus.Publish(eventbus.Event{
					Type:      eventbus.EventTypeSlash,
					Timestamp: time.Now(),
					Data:      map[string]any{"agent_id": string(item.AgentID), "amount": slashed},
				})
			}
		}
		// RecordTaskFailure applies the 15% OptimisticTrustLimit reduction.
		_ = e.identity.RecordTaskFailure(item.AgentID, domain)
	}

	// Publish a verification event for every settled verdict (positive or negative).
	if e.eventBus != nil {
		e.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeVerification,
			Timestamp: time.Now(),
			Data: map[string]any{
				"event_id": string(result.EventID),
				"agent_id": string(item.AgentID),
				"amount":   item.Amount,
				"verdict":  result.Verdict,
			},
		})
	}

	return nil
}

// checkExpired sweeps the pending map for items whose verification deadline has
// passed and treats each as a failed verification. Called by the background
// goroutine on every expiredCheckInterval tick.
//
// Each expired item is re-verified under the write lock before processing to
// prevent races with concurrent real verdicts. The processed set ensures that
// if a real verdict was applied between collection and processing, the expiry
// is silently skipped (idempotent).
func (e *Engine) checkExpired() {
	now := time.Now()

	e.mu.Lock()
	var expired []event.EventID
	for id, item := range e.pending {
		if now.Sub(item.OptimisticAt) > item.Deadline {
			expired = append(expired, id)
		}
	}
	e.mu.Unlock()

	for _, id := range expired {
		_ = e.ProcessResult(VerificationResult{
			EventID:   id,
			Verdict:   false,
			Reason:    "verification deadline exceeded",
			Timestamp: now,
		})
	}
}

// PendingCount returns the number of events currently awaiting verification.
// The count is a point-in-time snapshot; it may change immediately after return.
func (e *Engine) PendingCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.pending)
}

// IsPending reports whether eventID is currently tracked in the pending map.
func (e *Engine) IsPending(eventID event.EventID) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.pending[eventID]
	return ok
}

// Pending returns a snapshot of all events currently awaiting verification.
// The caller receives a new slice; mutations do not affect the engine state.
func (e *Engine) Pending() []*PendingItem {
	e.mu.RLock()
	defer e.mu.RUnlock()
	items := make([]*PendingItem, 0, len(e.pending))
	for _, item := range e.pending {
		items = append(items, item)
	}
	return items
}

// LoadPendingFromStore restores in-flight PendingItems from the persistence store
// on node restart. It also sets the store on the engine so that subsequent
// Submit/ProcessResult calls continue to write through. Call before Start.
// s must satisfy ocsPersistence; *store.Store from the store package does so.
func (e *Engine) LoadPendingFromStore(s ocsPersistence) error {
	items, err := s.AllPending()
	if err != nil {
		return fmt.Errorf("ocs: load pending: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, item := range items {
		e.pending[item.EventID] = item
	}
	return nil
}
