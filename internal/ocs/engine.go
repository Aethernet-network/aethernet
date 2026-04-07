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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/metrics"
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

	// eventBus — optional. When non-nil, settlement events are published for
	// real-time streaming. Nil-safe throughout ProcessResult.
	eventBus *eventbus.Bus

	// metrics — optional. When non-nil, settlement outcomes are counted.
	// Nil-safe throughout Submit and ProcessResult.
	nodeMetrics *metrics.AetherNetMetrics

	// voting — optional. When non-nil, verdicts route through reputation-weighted
	// BFT consensus before settlement. Nil means single-node direct settlement.
	voting *consensus.VotingRound

	// broadcastVote — optional. When non-nil, called after a local vote is
	// registered so the network layer can propagate it to peer nodes.
	// The function receives the event ID, verdict, and voter ID.
	broadcastVote func(eventID event.EventID, verdict bool, voterID crypto.AgentID)

	// onFinalized — optional. When non-nil, called exactly once when
	// processVoteInternal detects that a consensus round has finalized.
	// The callback receives the target event ID, the computed consensus
	// verdict, the verified value, and the finalization order. The callback
	// is responsible for creating the Settlement DAG event and applying it.
	// This makes settlement creation inevitable on finalization, rather
	// than depending on a later observer noticing finalization.
	onFinalized func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64)

	pending      map[event.EventID]*PendingItem
	processed    map[event.EventID]struct{}    // tracks already-settled events for idempotency
	processedAt  map[event.EventID]time.Time   // wall-clock time each event was settled (for GC)
	results      chan VerificationResult

	store ocsPersistence // optional; nil means in-memory only

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the background goroutine exits
}

// MinEventStake returns the minimum StakeAmount every submitted event must
// carry. Callers (e.g. the HTTP API) use this to auto-fill stake_amount when
// the client omits it, so that Submit never rejects a well-formed request with
// ErrInsufficientStake due to a missing field.
func (e *Engine) MinEventStake() uint64 {
	return e.config.MinStakeRequired
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

// SetMetrics wires a metrics struct into the Engine. Call before Start.
// When non-nil, Submit and ProcessResult update transaction counters.
// Nil-safe: all existing tests are unaffected.
func (e *Engine) SetMetrics(m *metrics.AetherNetMetrics) {
	e.nodeMetrics = m
}

// SetConsensus wires a VotingRound into the Engine for multi-node agreement.
// When non-nil, verdicts submitted via ProcessVote are routed through
// reputation-weighted BFT consensus before settlement. When nil (the default),
// ProcessVote delegates directly to ProcessResult for backward compatibility.
// Call before Start.
func (e *Engine) SetConsensus(vr *consensus.VotingRound) {
	e.voting = vr
}

// SetVoteBroadcaster wires a broadcast callback called after a local vote is
// registered in the consensus round. The callback should propagate the vote to
// peer nodes over the P2P network. Nil-safe.
func (e *Engine) SetVoteBroadcaster(fn func(eventID event.EventID, verdict bool, voterID crypto.AgentID)) {
	e.broadcastVote = fn
}

// SetFinalizationHandler registers a callback that fires when consensus
// finalizes a target event. The callback is the authoritative path for
// settlement creation — it creates the Settlement DAG event and calls
// the settlement applicator. This makes settlement inevitable on
// finalization, regardless of which vote path (MsgVote or DAG sync)
// triggered the supermajority.
//
// The callback receives:
//   - targetID: the event that was finalized
//   - verdict: true if accepted (supermajority yes), false if rejected
//   - verifiedValue: the verified value from the triggering vote
//   - finalOrder: the monotonic finalization sequence number
//
// The callback must be idempotent — it may be called from multiple
// vote paths for the same target. Use settlementApp.IsApplied for
// deduplication.
//
// Call before Start.
func (e *Engine) SetFinalizationHandler(fn func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64)) {
	e.onFinalized = fn
}

// ProcessVote routes a verification verdict through the consensus engine when
// one is configured, settling the event only when a supermajority is reached.
// When no consensus engine is set (nil), it falls back to direct settlement
// via ProcessResult for full single-node backward compatibility.
//
// When consensus is active and the vote does not yet trigger finalization,
// ProcessVote returns nil — the event remains pending until more votes arrive
// or the deadline sweep fires.
//
// After registering a new local vote, ProcessVote calls the broadcastVote
// callback (if set) so the P2P layer can propagate it to peer nodes.
func (e *Engine) ProcessVote(result VerificationResult) error {
	return e.processVoteInternal(result, true)
}

// AcceptPeerVote registers a vote received from a peer node without re-broadcasting
// it. If the received vote triggers a supermajority, settlement proceeds immediately.
// This is the entry point for the network layer's vote handler.
func (e *Engine) AcceptPeerVote(eventID event.EventID, voterID crypto.AgentID, verdict bool) error {
	return e.processVoteInternal(VerificationResult{
		EventID:    eventID,
		VerifierID: voterID,
		Verdict:    verdict,
		Timestamp:  time.Now(),
	}, false)
}

// processVoteInternal is the shared implementation for ProcessVote and
// AcceptPeerVote. When broadcast is true and a new vote is successfully
// registered, the broadcastVote callback is invoked.
func (e *Engine) processVoteInternal(result VerificationResult, broadcast bool) error {
	if e.voting == nil {
		// Single-node mode: direct settlement identical to previous behaviour.
		if broadcast {
			return e.ProcessResult(result)
		}
		return nil
	}

	// Register the vote in the consensus round.
	err := e.voting.RegisterVote(result.EventID, result.VerifierID, result.Verdict, result.VerifiedValue)
	if err != nil {
		slog.Warn("ocs: RegisterVote rejected",
			"event_id", result.EventID,
			"voter", result.VerifierID,
			"broadcast", broadcast,
			"err", err,
		)
		return nil
	}

	// Propagate locally-originated votes to peer nodes.
	if broadcast && e.broadcastVote != nil {
		e.broadcastVote(result.EventID, result.Verdict, result.VerifierID)
	}

	// Exactly-once finalization guard: MarkCallbackFired returns true only
	// for the first caller that detects finalization for this event. This
	// prevents duplicate onFinalized calls when concurrent vote paths both
	// see the record as finalized.
	if !e.voting.MarkCallbackFired(result.EventID) {
		return nil // not finalized, or another path already fired the callback
	}

	// BFT supermajority reached — read the deterministic consensus result.
	rec, ferr := e.voting.GetRecord(result.EventID)
	if ferr != nil {
		return nil
	}

	// Clear from OCS pending (metrics only — no ledger mutation).
	_ = e.ProcessResult(VerificationResult{
		EventID:       result.EventID,
		Verdict:       rec.FinalVerdict,
		VerifiedValue: rec.FinalVerifiedValue,
		VerifierID:    "",
		Reason:        "consensus: BFT finalized",
		Timestamp:     result.Timestamp,
	})

	// Fire the finalization handler to create the Settlement event.
	if e.onFinalized != nil {
		e.onFinalized(result.EventID, rec.FinalVerdict, rec.FinalVerifiedValue, rec.FinalOrder)
	}

	return nil
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
		config:      config,
		transfer:    tl,
		generation:  gl,
		identity:    reg,
		pending:     make(map[event.EventID]*PendingItem),
		processed:   make(map[event.EventID]struct{}),
		processedAt: make(map[event.EventID]time.Time),
		results:     make(chan VerificationResult, resultsBufferSize),
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
			// Route through consensus when active; falls back to direct
			// settlement when voting is nil (single-node backward compat).
			_ = e.ProcessVote(result)
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

	// senderID is the economic actor responsible for this event — used for the
	// C3 stake re-check at settlement time.  For Transfer events this is the
	// payload's FromAgent (the entity whose balance is debited), NOT ev.AgentID
	// (the node that signed and submitted the event).  Using ev.AgentID here
	// caused the C3 check to evaluate the signing node's stake (typically 0)
	// instead of the actual sender's stake, reversing every transfer.
	senderID := crypto.AgentID(ev.AgentID)

	switch ev.Type {
	case event.EventTypeTransfer:
		// Validate balance without recording. No ledger mutation at submit
		// time — the SettlementApplicator records and settles atomically
		// after consensus finalization.
		tp, err := event.GetPayload[event.TransferPayload](ev)
		if err != nil {
			return fmt.Errorf("ocs: decode transfer payload: %w", err)
		}
		if err := e.transfer.BalanceCheck(crypto.AgentID(tp.FromAgent), tp.Amount); err != nil {
			return fmt.Errorf("ocs: %w", err)
		}
		amount = tp.Amount
		recipientID = crypto.AgentID(tp.ToAgent)
		senderID = crypto.AgentID(tp.FromAgent)

	case event.EventTypeGeneration:
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
		AgentID:      senderID,
		Amount:       amount,
		RecipientID:  recipientID,
		OptimisticAt: time.Now(),
		Deadline:     e.config.VerificationTimeout,
	}
	if e.store != nil {
		if err := e.store.PutPending(e.pending[ev.ID]); err != nil {
			slog.Error("ocs: failed to persist pending item", "event_id", ev.ID, "err", err)
		}
	}
	if e.nodeMetrics != nil {
		e.nodeMetrics.TransactionsTotal.Inc()
	}
	return nil
}

// SubmitFromSync adds a DAG-synced event to the local pending queue so the
// auto-validator can vote on it. No ledger mutation — the SettlementApplicator
// records and settles atomically after consensus finalization.
// Idempotent: returns nil if the event is already pending or processed.
func (e *Engine) SubmitFromSync(ev *event.Event) error {
	if ev.Type != event.EventTypeTransfer && ev.Type != event.EventTypeGeneration && ev.Type != event.EventTypeTaskSettlement {
		return nil
	}
	if ev.SettlementState != event.SettlementOptimistic {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.pending[ev.ID]; exists {
		return nil
	}
	if _, done := e.processed[ev.ID]; done {
		return nil
	}
	if len(e.pending) >= e.config.MaxPendingItems {
		return fmt.Errorf("%w: capacity %d reached", ErrQueueFull, e.config.MaxPendingItems)
	}

	var amount uint64
	var recipientID crypto.AgentID
	senderID := crypto.AgentID(ev.AgentID)

	switch ev.Type {
	case event.EventTypeTransfer:
		tp, err := event.GetPayload[event.TransferPayload](ev)
		if err == nil {
			amount = tp.Amount
			recipientID = crypto.AgentID(tp.ToAgent)
			senderID = crypto.AgentID(tp.FromAgent)
		}
	case event.EventTypeGeneration:
		gp, err := event.GetPayload[event.GenerationPayload](ev)
		if err == nil {
			amount = gp.ClaimedValue
			recipientID = crypto.AgentID(gp.BeneficiaryAgent)
		}
	case event.EventTypeTaskSettlement:
		// Task settlement events carry budget as the economic amount.
		var raw struct {
			Budget uint64 `json:"budget"`
		}
		if err := json.Unmarshal(ev.Payload, &raw); err == nil {
			amount = raw.Budget
		}
	}

	e.pending[ev.ID] = &PendingItem{
		EventID:      ev.ID,
		EventType:    ev.Type,
		AgentID:      senderID,
		Amount:       amount,
		RecipientID:  recipientID,
		OptimisticAt: time.Now(),
		Deadline:     e.config.VerificationTimeout,
	}
	if e.store != nil {
		if err := e.store.PutPending(e.pending[ev.ID]); err != nil {
			slog.Error("ocs: failed to persist synced pending item", "event_id", ev.ID, "err", err)
		}
	}
	slog.Info("ocs: synced event added to pending", "event_id", ev.ID, "type", ev.Type)
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

// ProcessResult removes the event from the pending map and publishes metrics
// and eventBus notifications. It does NOT mutate ledger state — all canonical
// ledger mutations are handled exclusively by the SettlementApplicator.
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

	delete(e.pending, result.EventID)
	e.processed[result.EventID] = struct{}{}
	e.processedAt[result.EventID] = time.Now()
	if e.store != nil {
		if err := e.store.DeletePending(result.EventID); err != nil {
			slog.Error("ocs: failed to delete pending item", "event_id", result.EventID, "err", err)
		}
	}
	e.mu.Unlock()

	// Metrics (no ledger mutation).
	if result.Verdict {
		if e.nodeMetrics != nil {
			e.nodeMetrics.TransactionsSettled.Inc()
			e.nodeMetrics.TransactionVolume.Add(item.Amount)
		}
	} else {
		if e.nodeMetrics != nil {
			e.nodeMetrics.TransactionsReversed.Inc()
		}
	}

	// EventBus notification for real-time UI streaming.
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
//
// Also sweeps processedAt for entries older than 1 hour to prevent the
// idempotency map from growing without bound over time.
func (e *Engine) checkExpired() {
	now := time.Now()
	processedCutoff := now.Add(-1 * time.Hour)

	e.mu.Lock()
	var expired []event.EventID
	for id, item := range e.pending {
		if now.Sub(item.OptimisticAt) > item.Deadline {
			expired = append(expired, id)
		}
	}
	// GC: remove processed entries older than 1 hour from the idempotency maps.
	for id, settledAt := range e.processedAt {
		if settledAt.Before(processedCutoff) {
			delete(e.processed, id)
			delete(e.processedAt, id)
		}
	}
	e.mu.Unlock()

	if len(expired) > 0 {
		slog.Warn("ocs: checkExpired found expired events",
			"expired_count", len(expired),
			"pending_count", len(e.pending),
		)
	}
	for _, id := range expired {
		// Expiry is cleanup, NOT consensus. The BFT threshold is the ONLY
		// path to finalization. Expired events are removed from pending with
		// a false verdict (no settlement). Escrow returns are handled by the
		// settlement applicator when it sees a rejected/missing settlement.
		if e.voting != nil {
			rec, recErr := e.voting.GetRecord(id)
			if recErr == nil && rec.Finalized {
				// Already finalized by BFT — skip.
				continue
			}
		}
		slog.Warn("ocs: event expired without BFT consensus — removing from pending",
			"event_id", id,
		)
		_ = e.ProcessResult(VerificationResult{
			EventID:   id,
			Verdict:   false,
			Reason:    "consensus: expired without BFT supermajority",
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
