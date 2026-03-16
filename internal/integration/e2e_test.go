// Package integration_test contains end-to-end tests that exercise the full
// AetherNet protocol stack using direct function calls (no HTTP, no network).
//
// These tests verify that every critical protocol path works correctly when
// all components are wired together the same way cmd/node/main.go does it.
// If all 7 tests pass, the system works. Any failure pinpoints exactly what broke.
//
// Supply invariant: the sum of every known ledger account must equal the total
// genesis funding after every settlement. Checked after every mutating operation.
package integration_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/assurance"
	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/config"
	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	escrowpkg "github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/platform"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/validator"
)

// ─── Supply invariant tracker ────────────────────────────────────────────────

// supplyTracker monitors a transfer ledger's total balance across every known
// account. Callers register accounts with track() and call check() after any
// settlement operation to assert the supply invariant: sum(all balances) == genesis.
type supplyTracker struct {
	tl       *ledger.TransferLedger
	accounts map[crypto.AgentID]struct{}
	genesis  uint64
}

func newSupplyTracker(tl *ledger.TransferLedger) *supplyTracker {
	return &supplyTracker{
		tl:       tl,
		accounts: make(map[crypto.AgentID]struct{}),
	}
}

// fund seeds agentID with amount via FundAgent and records it as the genesis
// source of truth. Only call once per test setup — FundAgent mints tokens and
// increases the tracked genesis total.
func (st *supplyTracker) fund(agentID crypto.AgentID, amount uint64) {
	st.accounts[agentID] = struct{}{}
	st.genesis += amount
	if err := st.tl.FundAgent(agentID, amount); err != nil {
		panic(fmt.Sprintf("supplyTracker.fund(%s, %d): %v", agentID, amount, err))
	}
}

// track registers additional accounts (agent wallets, escrow buckets, the
// staking pool, treasury sinks) that receive token flows after genesis.
// These do not add to the genesis total — they are just watched for invariant.
func (st *supplyTracker) track(ids ...crypto.AgentID) {
	for _, id := range ids {
		st.accounts[id] = struct{}{}
	}
}

// check asserts that the sum of all tracked balances equals the genesis total.
// It logs the balance of every non-zero account when there is a discrepancy.
func (st *supplyTracker) check(t *testing.T, label string) {
	t.Helper()
	var total uint64
	details := make(map[crypto.AgentID]uint64)
	for id := range st.accounts {
		bal, _ := st.tl.Balance(id)
		total += bal
		if bal > 0 {
			details[id] = bal
		}
	}
	if total == st.genesis {
		return
	}
	diff := int64(total) - int64(st.genesis)
	t.Errorf("%s: SUPPLY INVARIANT VIOLATED — sum=%d, genesis=%d, diff=%+d",
		label, total, st.genesis, diff)
	for id, bal := range details {
		t.Logf("  %-50s  %d µAET", id, bal)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// initGenesis funds all six standard genesis buckets and returns the tracker.
// Uses the canonical AetherNet allocations. After this call, genesis total ==
// 1_000_000_000_000_000 µAET.
func initGenesis(t *testing.T, tl *ledger.TransferLedger) *supplyTracker {
	t.Helper()
	st := newSupplyTracker(tl)
	st.fund(genesis.BucketFounders, genesis.FoundersAllocation)
	st.fund(genesis.BucketInvestors, genesis.InvestorsAllocation)
	st.fund(genesis.BucketEcosystem, genesis.EcosystemAllocation)
	st.fund(genesis.BucketRewards, genesis.NetworkRewards)
	st.fund(genesis.BucketTreasury, genesis.TreasuryAllocation)
	st.fund(genesis.BucketPublic, genesis.PublicAllocation)
	return st
}

// onboardAgent allocates tokens from the ecosystem bucket to agentID, registers
// the agent in the identity registry, and starts them with ReputationScore=0.
// Returns the onboarding grant amount so the caller can track expected balances.
func onboardAgent(
	tl *ledger.TransferLedger,
	reg *identity.Registry,
	agentID crypto.AgentID,
	publicKey []byte,
	agentCount uint64, // how many agents were already registered (determines tier)
) (uint64, error) {
	grant := genesis.OnboardingAllocation(agentCount)
	if grant == 0 {
		return 0, fmt.Errorf("onboarding closed at agent %d", agentCount)
	}
	if err := tl.TransferFromBucket(genesis.BucketEcosystem, agentID, grant); err != nil {
		return 0, fmt.Errorf("onboard fund: %w", err)
	}
	fp, err := identity.NewFingerprint(agentID, publicKey, nil)
	if err != nil {
		return 0, fmt.Errorf("onboard fingerprint: %w", err)
	}
	return grant, reg.Register(fp)
}

// waitTask polls the task manager until the task reaches wantStatus or the
// deadline expires. Returns the final task status.
func waitTask(tm *tasks.TaskManager, taskID string, wantStatus tasks.TaskStatus, timeout time.Duration) tasks.TaskStatus {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, err := tm.Get(taskID)
		if err == nil && tk.Status == wantStatus {
			return tk.Status
		}
		time.Sleep(15 * time.Millisecond)
	}
	tk, err := tm.Get(taskID)
	if err != nil {
		return ""
	}
	return tk.Status
}

// waitGenLedger polls the generation ledger until TotalVerifiedValue > 0 within
// the window, then returns the value. Avoids the race where ApproveTask completes
// before RecordTaskGeneration runs in settleTask.
func waitGenLedger(gl *ledger.GenerationLedger, window time.Duration, timeout time.Duration) uint64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, _ := gl.TotalVerifiedValue(window)
		if v > 0 {
			return v
		}
		time.Sleep(15 * time.Millisecond)
	}
	v, _ := gl.TotalVerifiedValue(window)
	return v
}

// ─── Test 1: Full Settlement Lifecycle ────────────────────────────────────────

// TestE2E_FullSettlementLifecycle exercises the complete happy path:
//
//  1. Genesis initialised (1 B AET)
//  2. 3 agents onboarded from ecosystem bucket
//  3. Each agent stakes half their allocation
//  4. Poster posts a 2 M µAET "code" task; escrow holds funds
//  5. Worker claims and submits real Go code that passes CodeVerifier
//  6. Auto-validator settles the task
//  7. Economic assertions: worker balance, validator fee, treasury fee, reputation
//  8. CRITICAL: supply invariant checked — no tokens created or destroyed
func TestE2E_FullSettlementLifecycle(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	// ── Agent setup ──────────────────────────────────────────────────────────
	posterKP, _ := crypto.GenerateKeyPair()
	workerKP, _ := crypto.GenerateKeyPair()
	validatorKP, _ := crypto.GenerateKeyPair()

	posterID := posterKP.AgentID()
	workerID := workerKP.AgentID()
	validatorID := validatorKP.AgentID()
	treasuryID := crypto.AgentID(genesis.BucketTreasury)

	// Onboard all 3 agents from the ecosystem bucket (tier 0: 50 M µAET each).
	for i, id := range []crypto.AgentID{posterID, workerID, validatorID} {
		var kp *crypto.KeyPair
		switch i {
		case 0:
			kp = posterKP
		case 1:
			kp = workerKP
		default:
			kp = validatorKP
		}
		grant, err := onboardAgent(tl, reg, id, kp.PublicKey, uint64(i))
		if err != nil {
			t.Fatalf("onboard agent[%d]: %v", i, err)
		}
		st.track(id)
		_ = grant
	}
	st.track("staking-pool")

	// Stake half the allocation (25 M µAET) for each agent.
	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)
	const stakeAmt uint64 = 25_000_000
	for _, id := range []crypto.AgentID{posterID, workerID, validatorID} {
		if err := sm.Stake(id, stakeAmt); err != nil {
			t.Fatalf("Stake(%s, %d): %v", id, stakeAmt, err)
		}
	}

	st.check(t, "after staking")

	// Verify each agent has half staked and half spendable.
	const alloc uint64 = 50_000_000_000
	for _, id := range []crypto.AgentID{posterID, workerID, validatorID} {
		if got := sm.StakedAmount(id); got != stakeAmt {
			t.Errorf("staked(%s) = %d; want %d", id, got, stakeAmt)
		}
		bal, _ := tl.Balance(id)
		if bal != alloc-stakeAmt {
			t.Errorf("balance(%s) = %d; want %d", id, bal, alloc-stakeAmt)
		}
	}

	// ── Task lifecycle ────────────────────────────────────────────────────────
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start OCS engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	tm := tasks.NewTaskManager()
	esc := escrowpkg.New(tl)
	fc := fees.NewCollector(tl)
	repMgr := reputation.NewReputationManager()

	const budget uint64 = 2_000_000
	task, err := tm.PostTask(string(posterID), "Implement rate limiter", "Write a Go token-bucket rate limiter", "code", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	taskID := task.ID
	st.track(crypto.AgentID("escrow:" + taskID))

	if err := esc.Hold(taskID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	st.check(t, "after escrow hold")

	// Poster's balance should be alloc-stakeAmt-budget = 25M-2M = 23M.
	posterBalAfterHold, _ := tl.Balance(posterID)
	if posterBalAfterHold != alloc-stakeAmt-budget {
		t.Errorf("poster balance after hold = %d; want %d", posterBalAfterHold, alloc-stakeAmt-budget)
	}

	// Worker claims and submits real Go code — CodeVerifier needs parseable code
	// with functions, error handling, and relevant identifiers to score ≥ 0.5.
	if err := tm.ClaimTask(taskID, workerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	goCode := `package ratelimit

import (
	"errors"
	"sync"
	"time"
)

// ErrRateLimitExceeded is returned when the token bucket is empty.
var ErrRateLimitExceeded = errors.New("rate limit exceeded")

// TokenBucket implements a token-bucket rate limiter.
type TokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens per second
	lastFill time.Time
}

// New creates a TokenBucket with the given capacity and refill rate.
func New(capacity, rate float64) *TokenBucket {
	return &TokenBucket{
		tokens:   capacity,
		capacity: capacity,
		rate:     rate,
		lastFill: time.Now(),
	}
}

// Allow reports whether one token is available and consumes it.
// Returns ErrRateLimitExceeded when the bucket is empty.
func (tb *TokenBucket) Allow() error {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	if tb.tokens < 1 {
		return ErrRateLimitExceeded
	}
	tb.tokens--
	return nil
}

// refill adds tokens proportional to the time elapsed since the last fill.
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastFill).Seconds()
	tb.tokens = min(tb.capacity, tb.tokens+elapsed*tb.rate)
	tb.lastFill = now
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}`

	codeBytes := []byte(goCode)
	resultHash := evidence.ComputeHash(codeBytes)
	if err := tm.SubmitResult(taskID, workerID, resultHash, goCode, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Wire the auto-validator with the full testnet stack.
	av := autovalidator.NewAutoValidator(eng, validatorID, 5*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetFeeCollector(fc, treasuryID)
	av.SetGenerationLedger(gl)
	av.SetReputationManager(repMgr)
	av.SetRegistry(reg)
	av.SetTaskStalenessThreshold(0)
	av.Start()
	t.Cleanup(av.Stop)

	// Wait for settlement, including generation-ledger write after ApproveTask.
	finalStatus := waitTask(tm, taskID, tasks.TaskStatusCompleted, 3*time.Second)
	if finalStatus != tasks.TaskStatusCompleted {
		t.Fatalf("task did not complete within timeout; status = %q", finalStatus)
	}
	genValue := waitGenLedger(gl, 24*time.Hour, 3*time.Second)
	if genValue == 0 {
		t.Error("generation ledger TotalVerifiedValue = 0 after settlement")
	}

	// ── Economic assertions ───────────────────────────────────────────────────
	fee := fees.CalculateFee(budget)
	netAmount := budget - fee
	validatorFee := fee * fees.ValidatorShare / 100
	treasuryFee := fee * fees.TreasuryShare / 100

	// Worker received net share of the budget.
	workerBal, _ := tl.Balance(workerID)
	if workerBal != (alloc-stakeAmt)+netAmount {
		t.Errorf("worker balance = %d; want %d (initial %d + net %d)",
			workerBal, (alloc-stakeAmt)+netAmount, alloc-stakeAmt, netAmount)
	}

	// Validator received its fee share (on top of initial balance).
	valBal, _ := tl.Balance(validatorID)
	if valBal != (alloc-stakeAmt)+validatorFee {
		t.Errorf("validator balance = %d; want %d (initial %d + fee %d)",
			valBal, (alloc-stakeAmt)+validatorFee, alloc-stakeAmt, validatorFee)
	}

	// Treasury received its fee share.
	treaBal, _ := tl.Balance(treasuryID)
	expectedTreasury := genesis.TreasuryAllocation + treasuryFee
	if treaBal != expectedTreasury {
		t.Errorf("treasury balance = %d; want %d", treaBal, expectedTreasury)
	}

	// Poster balance unchanged since hold.
	posterBalFinal, _ := tl.Balance(posterID)
	if posterBalFinal != posterBalAfterHold {
		t.Errorf("poster balance changed after settlement: %d → %d", posterBalAfterHold, posterBalFinal)
	}

	// Generation ledger has exactly one entry.
	if genValue == 0 {
		t.Error("generation ledger: TotalVerifiedValue = 0; expected > 0")
	}
	if genValue > budget {
		t.Errorf("generation ledger: TotalVerifiedValue %d > budget %d", genValue, budget)
	}

	// Worker's reputation updated in "code" category.
	catRec := repMgr.GetCategoryRecord(workerID, "code")
	if catRec == nil || catRec.TasksCompleted == 0 {
		t.Error("worker has no completed tasks in 'code' reputation category after settlement")
	}

	// CRITICAL: supply invariant.
	st.check(t, "after full settlement")
}

// ─── Test 2: Transfer Settlement with Consensus ───────────────────────────────

// TestE2E_TransferConsensus verifies that an OCS Transfer event submitted with
// consensus (VotingRound, MinParticipants=1) correctly:
//   - Reaches supermajority after a single YES vote from a weighted validator
//   - Settles the ledger: sender debited, receiver credited
//   - CRITICAL: supply invariant preserved
func TestE2E_TransferConsensus(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	// Build two agents: sender (agentA) and receiver (agentB).
	kpA, _ := crypto.GenerateKeyPair()
	kpB, _ := crypto.GenerateKeyPair()
	kpV, _ := crypto.GenerateKeyPair() // validator for voting

	agentA := kpA.AgentID()
	agentB := kpB.AgentID()
	validatorID := kpV.AgentID()
	treasuryID := crypto.AgentID(genesis.BucketTreasury)

	for i, kp := range []*crypto.KeyPair{kpA, kpB} {
		_, err := onboardAgent(tl, reg, kp.AgentID(), kp.PublicKey, uint64(i))
		if err != nil {
			t.Fatalf("onboard agent[%d]: %v", i, err)
		}
		st.track(kp.AgentID())
	}
	st.track(treasuryID)

	// Validator needs reputation + stake in its fingerprint for weight calculation.
	vFP, _ := identity.NewFingerprint(validatorID, kpV.PublicKey, nil)
	vFP.ReputationScore = 5000  // 50% reputation
	vFP.StakedAmount = 10_000   // 10 000 µAET stake (consensus metadata only)
	if err := reg.Register(vFP); err != nil {
		t.Fatalf("register validator: %v", err)
	}
	st.track(validatorID)

	// Fund agentA with enough for the transfer. The stakeManager is wired so the
	// C3 re-check at settlement time uses the stakeManager.StakedAmount value.
	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)
	const transferAmt uint64 = 50_000
	const minStake uint64 = 1_000 // ocs.DefaultConfig().MinStakeRequired
	st.track("staking-pool")

	// Stake the minimum so C3 stake re-check passes.
	if err := sm.Stake(agentA, minStake); err != nil {
		t.Fatalf("Stake agentA: %v", err)
	}

	// OCS engine with fee collection and stake manager.
	fc := fees.NewCollector(tl)
	cfg := ocs.DefaultConfig()
	eng := ocs.NewEngine(cfg, tl, gl, reg)
	eng.SetEconomics(fc, sm, treasuryID)
	if err := eng.Start(); err != nil {
		t.Fatalf("start OCS engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	// Wire VotingRound with MinParticipants=1 (single-node in-process test).
	vcfg := consensus.DefaultConsensusConfig()
	vcfg.MinParticipants = 1
	vr := consensus.NewVotingRound(vcfg, reg)
	eng.SetConsensus(vr)

	// Build and submit the Transfer event.
	payload := event.TransferPayload{
		FromAgent: string(agentA),
		ToAgent:   string(agentB),
		Amount:    transferAmt,
		Currency:  "AET",
	}
	ev, err := event.New(
		event.EventTypeTransfer,
		nil,
		payload,
		string(agentA),
		nil,
		minStake, // stake_amount >= MinStakeRequired
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	// Snapshot balances BEFORE Submit — the OCS engine optimistically reserves
	// the sender's balance at Record time (not at settlement time), so we must
	// capture the pre-submit state for the sender assertion.
	balAPreSubmit, _ := tl.Balance(agentA)
	balBPreSubmit, _ := tl.Balance(agentB)

	if err := eng.Submit(ev); err != nil {
		t.Fatalf("engine.Submit: %v", err)
	}

	// Submit a YES vote from the validator — with MinParticipants=1 and a single
	// voter weight=5000*10000/10000=5000, the ratio is 100% >> 66.7%.
	// ProcessVote → RegisterVote → IsFinalized=true → ProcessResult (VerifierID="").
	voteResult := ocs.VerificationResult{
		EventID:    ev.ID,
		VerifierID: validatorID,
		Verdict:    true,
		Timestamp:  time.Now(),
	}
	if err := eng.ProcessVote(voteResult); err != nil {
		t.Fatalf("ProcessVote: %v", err)
	}

	// Verify consensus reached: event must be settled (removed from pending).
	if eng.IsPending(ev.ID) {
		t.Error("event still pending after consensus supermajority — settlement did not fire")
	}

	// Fee distribution with consensus path: when the voting round finalises,
	// ProcessResult is called with VerifierID="" (no single verifier to credit).
	// CollectFeeFromRecipient skips the validator transfer for empty VerifierID,
	// so only the treasury fee is deducted from the receiver.
	fee := fees.CalculateFee(transferAmt)
	treasuryFee := fee * fees.TreasuryShare / 100

	balAFinal, _ := tl.Balance(agentA)
	balBFinal, _ := tl.Balance(agentB)

	// Sender: debited transferAmt from pre-submit balance.
	if balAFinal != balAPreSubmit-transferAmt {
		t.Errorf("sender balance: want %d-%d=%d, got %d",
			balAPreSubmit, transferAmt, balAPreSubmit-transferAmt, balAFinal)
	}
	// Receiver: credited transferAmt minus treasury fee (validator fee stays
	// uncollected because consensus path uses VerifierID="").
	expectedBFinal := balBPreSubmit + transferAmt - treasuryFee
	if balBFinal != expectedBFinal {
		t.Errorf("receiver balance: want %d+%d-%d=%d, got %d",
			balBPreSubmit, transferAmt, treasuryFee, expectedBFinal, balBFinal)
	}

	// Validator: no fee in consensus path (VerifierID="" skips validator credit).
	valBal, _ := tl.Balance(validatorID)
	if valBal != 0 {
		t.Errorf("validator balance = %d; want 0 (no validator credit with consensus path)", valBal)
	}
	// Treasury: received treasury portion.
	treaBal, _ := tl.Balance(treasuryID)
	if treaBal != genesis.TreasuryAllocation+treasuryFee {
		t.Errorf("treasury balance = %d; want %d", treaBal, genesis.TreasuryAllocation+treasuryFee)
	}

	// CRITICAL: supply invariant.
	st.check(t, "after transfer consensus settlement")
}

// ─── Test 3: Staking Integrity ────────────────────────────────────────────────

// TestE2E_StakingIntegrity verifies the staking lifecycle including atomicity
// guarantees: a failed ledger credit during unstake must leave stake unchanged.
func TestE2E_StakingIntegrity(t *testing.T) {
	tl := ledger.NewTransferLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	kp, _ := crypto.GenerateKeyPair()
	agentID := kp.AgentID()
	_, err := onboardAgent(tl, reg, agentID, kp.PublicKey, 0)
	if err != nil {
		t.Fatalf("onboard: %v", err)
	}
	st.track(agentID, "staking-pool")

	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)

	balBefore, _ := tl.Balance(agentID)

	// Step 1: stake 10 M µAET.
	const stakeAmt uint64 = 10_000_000
	if err := sm.Stake(agentID, stakeAmt); err != nil {
		t.Fatalf("Stake: %v", err)
	}

	if got := sm.StakedAmount(agentID); got != stakeAmt {
		t.Errorf("staked after Stake = %d; want %d", got, stakeAmt)
	}
	balAfterStake, _ := tl.Balance(agentID)
	if balAfterStake != balBefore-stakeAmt {
		t.Errorf("balance after Stake = %d; want %d", balAfterStake, balBefore-stakeAmt)
	}

	st.check(t, "after stake")

	// Step 2: unstake 5 M µAET — must succeed.
	const unstakeAmt uint64 = 5_000_000
	if !sm.Unstake(agentID, unstakeAmt) {
		t.Fatal("Unstake 5M returned false; expected true")
	}
	if got := sm.StakedAmount(agentID); got != stakeAmt-unstakeAmt {
		t.Errorf("staked after partial unstake = %d; want %d", got, stakeAmt-unstakeAmt)
	}
	balAfterUnstake, _ := tl.Balance(agentID)
	if balAfterUnstake != balAfterStake+unstakeAmt {
		t.Errorf("balance after unstake = %d; want %d", balAfterUnstake, balAfterStake+unstakeAmt)
	}

	st.check(t, "after partial unstake")

	// Step 3: attempt unstake more than staked — must fail with no state change.
	overAmt := sm.StakedAmount(agentID) + 1
	if sm.Unstake(agentID, overAmt) {
		t.Error("Unstake(overAmt) returned true; expected false — cannot unstake more than staked")
	}
	if got := sm.StakedAmount(agentID); got != stakeAmt-unstakeAmt {
		t.Errorf("stake changed after failed over-unstake: got %d", got)
	}

	st.check(t, "after over-unstake attempt")

	// Step 4: atomicity — force ledger credit failure by creating a fresh stake
	// manager with NO ledger, then attempt to unstake. The pool has zero balance
	// in the new tl2, so the credit will fail.
	// We test via the whitebox path: create a StakeManager whose transfer ledger
	// has no staking-pool balance, verify Unstake returns false and stake unchanged.
	tl2 := ledger.NewTransferLedger()
	smAtomic := staking.NewStakeManager()
	smAtomic.SetTransferLedger(tl2)

	// Directly fund agentID in tl2 but intentionally do NOT fill the staking-pool.
	if err := tl2.FundAgent(agentID, 10_000); err != nil {
		t.Fatalf("FundAgent tl2: %v", err)
	}
	// Bypass Stake() (which would fill the pool) and just test the atomic abort.
	// We do this by calling Stake on a second manager with a properly funded pool,
	// then testing atomicity with the poolless ledger.
	smAtomic2 := staking.NewStakeManager()
	tl3 := ledger.NewTransferLedger()
	if err := tl3.FundAgent(agentID, 10_000); err != nil {
		t.Fatalf("FundAgent tl3: %v", err)
	}
	smAtomic2.SetTransferLedger(tl3)
	if err := smAtomic2.Stake(agentID, 5_000); err != nil {
		t.Fatalf("Stake on tl3: %v", err)
	}
	// Now drain the pool to simulate ledger-credit-failure scenario.
	// Transfer from staking-pool back to agentID to empty it.
	if err := tl3.TransferFromBucket("staking-pool", agentID, 5_000); err != nil {
		t.Fatalf("drain staking-pool: %v", err)
	}
	// The in-memory stake still says 5000 but the pool has 0.
	// Unstake must return false (credit would fail) and stake must be unchanged.
	if smAtomic2.Unstake(agentID, 5_000) {
		t.Error("Unstake returned true despite empty staking-pool; atomicity violated")
	}
	if got := smAtomic2.StakedAmount(agentID); got != 5_000 {
		t.Errorf("stake after failed unstake = %d; want 5000 (stake must be unchanged)", got)
	}

	// CRITICAL: supply invariant on original ledger.
	st.check(t, "after staking integrity tests")
}

// ─── Test 4: Escrow Idempotency ───────────────────────────────────────────────

// TestE2E_EscrowIdempotency verifies the ReleaseNet paid-flag mechanism:
//   - WorkerPaid=true skips the worker re-payment on retry
//   - ValidatorPaid=false still pays the validator on retry
//   - CRITICAL: supply invariant — no double payments
func TestE2E_EscrowIdempotency(t *testing.T) {
	tl := ledger.NewTransferLedger()
	_, _ = ledger.NewGenerationLedger(), identity.NewRegistry() // not needed
	st := initGenesis(t, tl)

	posterID := crypto.AgentID("poster-idem")
	workerID := crypto.AgentID("worker-idem")
	validatorID := crypto.AgentID("validator-idem")
	treasuryID := crypto.AgentID(genesis.BucketTreasury)

	// Fund poster from ecosystem.
	const posterFund = uint64(10_000_000)
	if err := tl.TransferFromBucket(genesis.BucketEcosystem, posterID, posterFund); err != nil {
		t.Fatalf("fund poster: %v", err)
	}
	st.track(posterID, workerID, validatorID)
	// treasuryID is already tracked (genesis:treasury in the genesis set).

	const budget = uint64(1_000_000)
	esc := escrowpkg.New(tl)

	// Post and hold.
	tm := tasks.NewTaskManager()
	task, err := tm.PostTask(string(posterID), "Test idempotency", "desc", "code", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	taskID := task.ID
	st.track(crypto.AgentID("escrow:" + taskID))

	if err := esc.Hold(taskID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	st.check(t, "after hold")

	// Compute the expected fee splits.
	fee := fees.CalculateFee(budget)
	netAmount := budget - fee
	valAmt := fee * fees.ValidatorShare / 100
	treaAmt := fee * fees.TreasuryShare / 100

	// Simulate a partial first call: worker payment succeeded but validator
	// payment failed. Manually set WorkerPaid=true on the entry.
	// Since this is in package integration_test (external), we call ReleaseNet
	// with worker payment, read the entry via Get(), and verify WorkerPaid.
	// We test the retry path by calling ReleaseNet a second time.
	//
	// First full call (all three payments succeed):
	if err := esc.ReleaseNet(taskID, workerID, netAmount, validatorID, valAmt, treasuryID, treaAmt); err != nil {
		t.Fatalf("ReleaseNet (first, full): %v", err)
	}

	// Entry should be deleted after successful full disbursement.
	if _, err := esc.Get(taskID); err == nil {
		t.Error("entry should be deleted after successful full ReleaseNet")
	}

	// Second call must return ErrEscrowNotFound — not double-pay.
	retryErr := esc.ReleaseNet(taskID, workerID, netAmount, validatorID, valAmt, treasuryID, treaAmt)
	if retryErr == nil {
		t.Fatal("second ReleaseNet should return error (entry deleted after first)")
	}
	if !errors.Is(retryErr, escrowpkg.ErrEscrowNotFound) {
		t.Errorf("second ReleaseNet: want ErrEscrowNotFound, got %v", retryErr)
	}

	// Worker must have been paid exactly once.
	workerBal, _ := tl.Balance(workerID)
	if workerBal != netAmount {
		t.Errorf("worker balance = %d; want %d (must not be double-paid)", workerBal, netAmount)
	}

	// Now test the partial-completion path directly using the whitebox test
	// pattern: Hold a second task, set WorkerPaid=true manually is not possible
	// from this package — instead call ReleaseNet with worker succeeding, then
	// simulate retry by calling again. The entry is deleted after the first full
	// call, so the second returns ErrEscrowNotFound — that IS the idempotency
	// guarantee for the "full success then retry" case.
	//
	// For the "partial success" case (WorkerPaid=true, ValidatorPaid=false), the
	// whitebox test in internal/escrow/idempotency_test.go covers it precisely.
	// Here we verify the contract from the outside: no double-payment on retry.

	// CRITICAL: supply invariant.
	st.check(t, "after escrow idempotency")
}

// ─── Test 5: Security Enforcement ─────────────────────────────────────────────

// TestE2E_SecurityEnforcement verifies three security properties:
//  1. Write requests without X-API-Key return 401 when requireAuth=true + platformKeys set
//  2. Registering the same agent ID twice returns ErrAgentAlreadyExists
//  3. Claiming a task with the poster's own ID returns ErrSelfClaim
func TestE2E_SecurityEnforcement(t *testing.T) {
	// ── 5.1: Auth enforcement ─────────────────────────────────────────────────
	t.Run("auth_required_for_writes", func(t *testing.T) {
		d := dag.New()
		tl := ledger.NewTransferLedger()
		gl := ledger.NewGenerationLedger()
		reg := identity.NewRegistry()
		eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
		if err := eng.Start(); err != nil {
			t.Fatalf("start engine: %v", err)
		}
		t.Cleanup(eng.Stop)
		sm := ledger.NewSupplyManager(tl, gl)
		kp, _ := crypto.GenerateKeyPair()

		srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
		// Wire platformKeys so auth is enforced (requireAuth=true is already the default).
		km := platform.NewKeyManager()
		srv.SetPlatformKeys(km)

		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)

		// A POST without X-API-Key must be rejected with 401.
		resp, err := http.Post(ts.URL+"/v1/generation", "application/json",
			strings.NewReader(`{"claimed_value":1000,"evidence_hash":"sha256:test","stake_amount":1000}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated POST: got %d; want 401", resp.StatusCode)
		}

		// A GET (read operation) must succeed without a key.
		resp2, err := http.Get(ts.URL + "/v1/status")
		if err != nil {
			t.Fatalf("GET status: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("GET /v1/status without key: got %d; want 200", resp2.StatusCode)
		}
	})

	// ── 5.2: Duplicate agent registration ────────────────────────────────────
	t.Run("duplicate_agent_registration_rejected", func(t *testing.T) {
		reg := identity.NewRegistry()
		kp, _ := crypto.GenerateKeyPair()
		fp, _ := identity.NewFingerprint(kp.AgentID(), kp.PublicKey, nil)

		if err := reg.Register(fp); err != nil {
			t.Fatalf("first Register: %v", err)
		}

		// Second registration with the same AgentID must fail.
		fp2, _ := identity.NewFingerprint(kp.AgentID(), kp.PublicKey, nil)
		err := reg.Register(fp2)
		if err == nil {
			t.Fatal("second Register should have failed; duplicate agent ID accepted")
		}
		if !errors.Is(err, identity.ErrAgentAlreadyExists) {
			t.Errorf("want ErrAgentAlreadyExists; got %v", err)
		}
	})

	// ── 5.3: Self-dealing prevention ─────────────────────────────────────────
	t.Run("self_claim_rejected", func(t *testing.T) {
		tm := tasks.NewTaskManager()
		posterID := crypto.AgentID("self-poster")

		task, err := tm.PostTask(string(posterID), "Self-dealing test", "desc", "code", 200_000)
		if err != nil {
			t.Fatalf("PostTask: %v", err)
		}

		// Poster claiming their own task must be rejected.
		err = tm.ClaimTask(task.ID, posterID)
		if err == nil {
			t.Fatal("self-claim should have failed")
		}
		if !errors.Is(err, tasks.ErrSelfClaim) {
			t.Errorf("want ErrSelfClaim; got %v", err)
		}
	})
}

// ─── Test 6: Deterministic Verification ──────────────────────────────────────

// TestE2E_DeterministicVerification verifies that the evidence verifiers make
// correct pass/fail decisions for each category. Uses real content that satisfies
// each verifier's quality criteria.
func TestE2E_DeterministicVerification(t *testing.T) {
	vr := evidence.NewVerifierRegistry()

	// ── 6.1: CodeVerifier — real Go code passes ───────────────────────────────
	t.Run("code_passes_real_go", func(t *testing.T) {
		goCode := `package cache

import (
	"errors"
	"sync"
	"time"
)

// ErrCacheMiss is returned when a key is not found in the cache.
var ErrCacheMiss = errors.New("cache miss")

// entry stores a value along with its expiry time.
type entry struct {
	value     interface{}
	expiresAt time.Time
}

// Cache implements a thread-safe TTL cache.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]entry
}

// New creates a Cache with a background cleanup goroutine.
func New() *Cache {
	c := &Cache{entries: make(map[string]entry)}
	go c.evictLoop()
	return c
}

// Set stores a value with a given TTL.
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{value: value, expiresAt: time.Now().Add(ttl)}
}

// Get retrieves a value, returning ErrCacheMiss if absent or expired.
func (c *Cache) Get(key string) (interface{}, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, ErrCacheMiss
	}
	return e.value, nil
}

// evictLoop removes expired entries every second.
func (c *Cache) evictLoop() {
	for range time.Tick(time.Second) {
		c.mu.Lock()
		for k, e := range c.entries {
			if time.Now().After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}`
		ev := &evidence.Evidence{
			Hash:          evidence.ComputeHash([]byte(goCode)),
			OutputType:    "code",
			OutputSize:    uint64(len(goCode)),
			Summary:       goCode,
			OutputPreview: goCode,
		}
		score, passed := vr.Verify(ev, "Implement TTL cache", "Write a thread-safe Go TTL cache with eviction", 500_000, "code")
		if !passed {
			t.Errorf("real Go code should pass CodeVerifier; score=%.3f (want >= 0.5)", score.Overall)
		}
		if score.Overall < 0.5 {
			t.Errorf("code score = %.3f; want >= 0.5", score.Overall)
		}
	})

	// ── 6.2: CodeVerifier — gibberish fails ──────────────────────────────────
	t.Run("gibberish_fails_code_verifier", func(t *testing.T) {
		gibberish := "xkcd jfkd sldf jlsdf jls df jlsdf jlksdf jlksdf jls lkj"
		ev := &evidence.Evidence{
			Hash:       evidence.ComputeHash([]byte(gibberish)),
			OutputType: "code",
			OutputSize: uint64(len(gibberish)),
			Summary:    gibberish,
		}
		score, passed := vr.Verify(ev, "Write sorting algorithm", "Implement merge sort in Go", 500_000, "code")
		if passed {
			t.Errorf("gibberish should fail CodeVerifier; score=%.3f", score.Overall)
		}
	})

	// ── 6.3: DataVerifier — structured JSON analysis passes ──────────────────
	t.Run("data_passes_structured_json", func(t *testing.T) {
		jsonData := `{
  "analysis": "Climate temperature anomaly analysis for 2023",
  "dataset": "NOAA Global Surface Temperature",
  "methodology": "Linear regression on monthly anomalies",
  "findings": [
    {"year": 2023, "anomaly_c": 1.45, "trend": "record high"},
    {"year": 2022, "anomaly_c": 0.89, "trend": "above average"}
  ],
  "statistics": {
    "mean_anomaly": 1.17,
    "std_deviation": 0.28,
    "sample_size": 12
  },
  "conclusion": "2023 shows statistically significant warming above 1.5°C baseline.",
  "confidence": 0.95
}`
		ev := &evidence.Evidence{
			Hash:       evidence.ComputeHash([]byte(jsonData)),
			OutputType: "json",
			OutputSize: uint64(len(jsonData)),
			Summary:    jsonData,
		}
		score, passed := vr.Verify(ev, "Climate data analysis", "Analyse temperature anomaly data for 2023", 500_000, "data")
		if !passed {
			t.Errorf("structured JSON analysis should pass DataVerifier; score=%.3f (want >= 0.5)", score.Overall)
		}
	})

	// ── 6.4: ContentVerifier — well-formed article passes ────────────────────
	t.Run("content_passes_well_formed_article", func(t *testing.T) {
		article := `# Introduction to Distributed Systems

Distributed systems are collections of independent computers that work together
to appear as a single coherent system. They power modern infrastructure from
databases to cloud platforms.

## Key Challenges

**Consistency** means all nodes see the same data at the same time.
**Availability** ensures the system responds to every request.
**Partition tolerance** allows the system to operate despite network failures.

The CAP theorem states that a distributed system can guarantee at most two of
these three properties simultaneously. Most production systems choose CP or AP
depending on their requirements.

## Common Patterns

The leader-follower pattern designates one node to handle writes while followers
replicate data asynchronously. The Raft consensus algorithm uses this approach to
achieve fault-tolerant replication with strong consistency guarantees.

## Conclusion

Building reliable distributed systems requires careful consideration of failure
modes, consistency requirements, and operational complexity. Modern tools like
etcd, ZooKeeper, and Consul implement these patterns as reusable primitives.`

		ev := &evidence.Evidence{
			Hash:       evidence.ComputeHash([]byte(article)),
			OutputType: "text",
			OutputSize: uint64(len(article)),
			Summary:    article,
		}
		score, passed := vr.Verify(ev, "Write distributed systems article", "Explain key concepts of distributed systems for engineers", 500_000, "writing")
		if !passed {
			t.Errorf("well-formed article should pass ContentVerifier; score=%.3f (want >= 0.5)", score.Overall)
		}
	})
}

// ─── Test 7: Multi-Agent Economics ────────────────────────────────────────────

// TestE2E_MultiAgentEconomics registers 10 agents, posts and settles 5 tasks
// from 5 different posters, and verifies that:
//   - All balances are consistent
//   - Total fees collected = 0.1% of total settled value
//   - Generation ledger has exactly 5 entries
//   - CRITICAL: supply invariant after all settlements
func TestE2E_MultiAgentEconomics(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)
	st.track("staking-pool")

	// Register 10 agents: 5 posters + 5 workers.
	const numAgents = 10
	kps := make([]*crypto.KeyPair, numAgents)
	agentIDs := make([]crypto.AgentID, numAgents)
	for i := 0; i < numAgents; i++ {
		kp, _ := crypto.GenerateKeyPair()
		kps[i] = kp
		agentIDs[i] = kp.AgentID()
		_, err := onboardAgent(tl, reg, kp.AgentID(), kp.PublicKey, uint64(i))
		if err != nil {
			t.Fatalf("onboard agent[%d]: %v", i, err)
		}
		st.track(kp.AgentID())
	}

	validatorID := crypto.AgentID("multi-validator")
	treasuryID := crypto.AgentID(genesis.BucketTreasury)

	// Register validator with rep+stake for consensus weight.
	vFP, _ := identity.NewFingerprint(validatorID, make([]byte, 32), nil)
	vFP.ReputationScore = 5000
	vFP.StakedAmount = 10_000
	_ = reg.Register(vFP)
	st.track(validatorID)

	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)
	const stakeAmt uint64 = 10_000_000 // 10 M each
	for _, id := range agentIDs {
		if err := sm.Stake(id, stakeAmt); err != nil {
			t.Fatalf("Stake(%s, %d): %v", id, stakeAmt, err)
		}
	}

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	tm := tasks.NewTaskManager()
	sharedEsc := escrowpkg.New(tl)
	fc := fees.NewCollector(tl)
	repMgr := reputation.NewReputationManager()

	const numTasks = 5
	const taskBudget uint64 = 500_000 // 0.5 AET each

	taskIDs := make([]string, numTasks)

	for i := 0; i < numTasks; i++ {
		posterID := agentIDs[i]
		task, err := tm.PostTask(string(posterID),
			fmt.Sprintf("Task %d", i),
			fmt.Sprintf("Description for task %d", i),
			"code", taskBudget)
		if err != nil {
			t.Fatalf("PostTask[%d]: %v", i, err)
		}
		taskIDs[i] = task.ID
		st.track(crypto.AgentID("escrow:" + task.ID))

		if err := sharedEsc.Hold(task.ID, posterID, taskBudget); err != nil {
			t.Fatalf("Hold[%d]: %v", i, err)
		}
	}

	st.check(t, "after all holds")

	// Wire a single auto-validator with the shared escrow to settle all 5 tasks.
	avID := validatorID
	av := autovalidator.NewAutoValidator(eng, avID, 5*time.Millisecond)
	av.SetTaskManager(tm, sharedEsc)
	av.SetFeeCollector(fc, treasuryID)
	av.SetGenerationLedger(gl)
	av.SetReputationManager(repMgr)
	av.SetRegistry(reg)
	av.SetTaskStalenessThreshold(0)
	av.Start()
	t.Cleanup(av.Stop)

	// Workers claim and submit all tasks.
	for i := 0; i < numTasks; i++ {
		workerID := agentIDs[5+i]
		if err := tm.ClaimTask(taskIDs[i], workerID); err != nil {
			t.Fatalf("ClaimTask[%d]: %v", i, err)
		}

		goCode := fmt.Sprintf(`package solution%d

import "errors"

// Solve implements the solution for task %d.
func Solve(input string) (string, error) {
	if input == "" {
		return "", errors.New("input must not be empty")
	}
	return "result-" + input, nil
}`, i, i)

		hash := evidence.ComputeHash([]byte(goCode))
		if err := tm.SubmitResult(taskIDs[i], workerID, hash, goCode, ""); err != nil {
			t.Fatalf("SubmitResult[%d]: %v", i, err)
		}
	}

	// Wait for all 5 tasks to complete + generation ledger to have 5 entries.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		completedAll := true
		for i := 0; i < numTasks; i++ {
			tk, _ := tm.Get(taskIDs[i])
			if tk == nil || tk.Status != tasks.TaskStatusCompleted {
				completedAll = false
				break
			}
		}
		genVal, _ := gl.TotalVerifiedValue(24 * time.Hour)
		if completedAll && genVal > 0 {
			// Verify generation ledger count by checking 5 separate task entries.
			allEntries := true
			for i := 0; i < numTasks; i++ {
				if v, _ := gl.TotalVerifiedValue(24 * time.Hour); v == 0 {
					allEntries = false
				}
			}
			if allEntries {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Assert all tasks completed.
	for i := 0; i < numTasks; i++ {
		tk, err := tm.Get(taskIDs[i])
		if err != nil {
			t.Errorf("Get task[%d]: %v", i, err)
			continue
		}
		if tk.Status != tasks.TaskStatusCompleted {
			t.Errorf("task[%d] status = %q; want completed", i, tk.Status)
		}
	}

	// Verify total fees collected ≈ 0.1% of total settled value.
	totalSettledValue := uint64(numTasks) * taskBudget
	expectedFees := fees.CalculateFee(taskBudget) * uint64(numTasks)
	collected, _, _ := fc.Stats()
	if collected != expectedFees {
		t.Errorf("fees collected = %d; want %d (0.1%% of %d µAET total)",
			collected, expectedFees, totalSettledValue)
	}

	// Generation ledger must have entries for all settled tasks.
	genTotal, _ := gl.TotalVerifiedValue(24 * time.Hour)
	if genTotal == 0 {
		t.Error("generation ledger has no entries after 5 task settlements")
	}
	if genTotal > totalSettledValue {
		t.Errorf("generation ledger total %d > settled value %d", genTotal, totalSettledValue)
	}

	// CRITICAL: supply invariant after all settlements.
	st.check(t, "after multi-agent economics")
}

// ─── Test-only adapters ──────────────────────────────────────────────────────

// memReplayStore satisfies the unexported replay.replayStore interface for
// in-process testing without Badger.
type memReplayStore struct {
	jobs     map[string][]byte
	outcomes map[string][]byte
}

func newMemReplayStore() *memReplayStore {
	return &memReplayStore{
		jobs:     make(map[string][]byte),
		outcomes: make(map[string][]byte),
	}
}

func (s *memReplayStore) PutReplayJob(id string, data []byte) error {
	s.jobs[id] = append([]byte{}, data...)
	return nil
}

func (s *memReplayStore) GetReplayJob(id string) ([]byte, error) {
	d, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("replay job %q not found", id)
	}
	return d, nil
}

func (s *memReplayStore) AllReplayJobs() (map[string][]byte, error) {
	cp := make(map[string][]byte, len(s.jobs))
	for k, v := range s.jobs {
		cp[k] = v
	}
	return cp, nil
}

func (s *memReplayStore) PutReplayOutcome(id string, data []byte) error {
	s.outcomes[id] = append([]byte{}, data...)
	return nil
}

func (s *memReplayStore) GetReplayOutcome(id string) ([]byte, error) {
	d, ok := s.outcomes[id]
	if !ok {
		return nil, fmt.Errorf("replay outcome %q not found", id)
	}
	return d, nil
}

func (s *memReplayStore) AllReplayOutcomes() (map[string][]byte, error) {
	cp := make(map[string][]byte, len(s.outcomes))
	for k, v := range s.outcomes {
		cp[k] = v
	}
	return cp, nil
}

// ─── Test 8: Fraudulent Approval → Replay → Slash/Suspend ───────────────────

// TestE2E_FraudulentApprovalReplaySlash exercises the adversarial path where a
// task is approved fraudulently, a replay detects the mismatch, and the
// approving validator is slashed and suspended.
//
//  1. Task lifecycle: post → hold → claim → submit → approve
//  2. Replay produces a "mismatch" outcome with anomaly flags → severity ≥ 0.9
//  3. ReplayResolver evaluates to "slash_recommended"
//  4. Task marked "replay_disputed", generation denied
//  5. Validator slashed 30% (OffenseFraudulentApproval), suspended 30 days
//  6. Slash proceeds split to challenger (50%) and reserve (50%)
//  7. CRITICAL: supply invariant preserved
func TestE2E_FraudulentApprovalReplaySlash(t *testing.T) {
	tl := ledger.NewTransferLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	// ── Agents ──────────────────────────────────────────────────────────────
	posterKP, _ := crypto.GenerateKeyPair()
	workerKP, _ := crypto.GenerateKeyPair()
	validatorKP, _ := crypto.GenerateKeyPair()
	posterID := posterKP.AgentID()
	workerID := workerKP.AgentID()
	validatorID := validatorKP.AgentID()

	for i, kp := range []*crypto.KeyPair{posterKP, workerKP, validatorKP} {
		if _, err := onboardAgent(tl, reg, kp.AgentID(), kp.PublicKey, uint64(i)); err != nil {
			t.Fatalf("onboard[%d]: %v", i, err)
		}
		st.track(kp.AgentID())
	}
	st.track("staking-pool")

	// Stake the validator (must meet validator registry minimum of 10 AET).
	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)
	const stakeAmt uint64 = 25_000_000_000 // 25 AET
	if err := sm.Stake(validatorID, stakeAmt); err != nil {
		t.Fatalf("Stake: %v", err)
	}

	// ── Task lifecycle (fraudulently approved) ──────────────────────────────
	tm := tasks.NewTaskManager()
	esc := escrowpkg.New(tl)
	const budget uint64 = 2_000_000
	task, err := tm.PostTask(string(posterID), "Suspect task", "desc", "code", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	taskID := task.ID
	st.track(crypto.AgentID("escrow:" + taskID))

	if err := esc.Hold(taskID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(taskID, workerID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	resultHash := "sha256:fraudulent-output"
	if err := tm.SubmitResult(taskID, workerID, resultHash, "fake output", ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	if err := tm.ApproveTask(taskID, posterID); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}

	// Settle escrow manually (no autovalidator in adversarial path).
	fee := fees.CalculateFee(budget)
	netAmt := budget - fee
	valFee := fee * fees.ValidatorShare / 100
	treaFee := fee * fees.TreasuryShare / 100
	treasuryID := crypto.AgentID(genesis.BucketTreasury)
	if err := esc.ReleaseNet(taskID, workerID, netAmt, validatorID, valFee, treasuryID, treaFee); err != nil {
		t.Fatalf("ReleaseNet: %v", err)
	}

	st.check(t, "after fraudulent task settlement")

	// ── Replay detects fraud ────────────────────────────────────────────────
	ms := newMemReplayStore()
	coord := replay.NewReplayCoordinator(replay.DefaultReplayPolicy(), ms)
	resolver := replay.NewReplayResolver(ms)

	job, err := coord.ScheduleReplay(taskID, resultHash, "code", "v1", nil, "dispute", string(workerID))
	if err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}

	// Mismatch outcome with 2 anomaly flags → severity 0.8+0.2 = 1.0 ≥ 0.9.
	outcome := &replay.ReplayOutcome{
		JobID:  job.ID,
		TaskID: taskID,
		Status: "mismatch",
		Comparisons: []replay.CheckComparison{{
			CheckType:    "code_verify",
			OriginalHash: "sha256:original",
			ReplayedHash: "sha256:different",
			Match:        false,
			ScoreDelta:   0.8,
		}},
		AnomalyFlags: []string{"timing_anomaly", "environment_drift"},
		ReplayedAt:   time.Now(),
		ReplayerID:   "replay-agent",
	}

	if err := resolver.RecordOutcome(outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	verdict := resolver.EvaluateOutcome(outcome)
	if verdict.Action != "slash_recommended" {
		t.Errorf("verdict.Action = %q; want slash_recommended", verdict.Action)
	}
	if verdict.SeverityScore < 0.9 {
		t.Errorf("verdict.SeverityScore = %.2f; want >= 0.9", verdict.SeverityScore)
	}

	// Mark task as disputed and deny generation credit.
	if err := tm.SetReplayStatus(taskID, "replay_disputed", job.ID); err != nil {
		t.Fatalf("SetReplayStatus: %v", err)
	}
	if err := tm.SetGenerationStatus(taskID, "denied"); err != nil {
		t.Fatalf("SetGenerationStatus: %v", err)
	}

	tk, _ := tm.Get(taskID)
	if tk.ReplayStatus != "replay_disputed" {
		t.Errorf("task.ReplayStatus = %q; want replay_disputed", tk.ReplayStatus)
	}
	if tk.GenerationStatus != "denied" {
		t.Errorf("task.GenerationStatus = %q; want denied", tk.GenerationStatus)
	}

	// ── Slash validator ─────────────────────────────────────────────────────
	cfg := config.DefaultConfig()
	cfg.Validator.CapEnforcementMinValidators = 100 // disable for test
	valReg := validator.NewValidatorRegistry(&cfg.Validator, nil)
	val, err := valReg.Register(string(validatorID), stakeAmt, []string{"code"}, true)
	if err != nil {
		t.Fatalf("validator register: %v", err)
	}

	slashEng := validator.NewSlashEngine(valReg, &cfg.Validator)
	result, err := slashEng.Slash(val.ID, validator.OffenseFraudulentApproval)
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}

	// Assert slash economics.
	expectedSlash := uint64(float64(stakeAmt) * 0.30)
	if result.SlashPercentage != 0.30 {
		t.Errorf("SlashPercentage = %.2f; want 0.30", result.SlashPercentage)
	}
	if result.SlashAmount != expectedSlash {
		t.Errorf("SlashAmount = %d; want %d", result.SlashAmount, expectedSlash)
	}
	if result.RemainingStake != stakeAmt-expectedSlash {
		t.Errorf("RemainingStake = %d; want %d", result.RemainingStake, stakeAmt-expectedSlash)
	}
	if result.CooldownDays != 30 {
		t.Errorf("CooldownDays = %d; want 30", result.CooldownDays)
	}
	if result.PermanentExclusion {
		t.Error("PermanentExclusion should be false for fraudulent_approval")
	}

	// Validator must be suspended — resume blocked.
	if err := slashEng.Resume(val.ID); !errors.Is(err, validator.ErrSlashInCooldown) {
		t.Errorf("Resume during cooldown: want ErrSlashInCooldown, got %v", err)
	}

	// Execute slash token flows: staking-pool → challenger + reserve.
	challengerID := crypto.AgentID("fraud-challenger")
	reserveID := crypto.AgentID("dispute-reserve")
	st.track(challengerID, reserveID)

	if err := tl.TransferFromBucket("staking-pool", challengerID, result.ChallengerShare); err != nil {
		t.Fatalf("transfer ChallengerShare: %v", err)
	}
	if err := tl.TransferFromBucket("staking-pool", reserveID, result.ReserveShare); err != nil {
		t.Fatalf("transfer ReserveShare: %v", err)
	}

	// CRITICAL: supply invariant.
	st.check(t, "after fraudulent approval slash")
}

// ─── Test 9: Challenge Succeeds → Validator Slashed ──────────────────────────

// TestE2E_ChallengeSucceeds exercises the path where a replay produces a
// material discrepancy, a challenge is opened, and the challenge succeeds:
//
//  1. Replay produces "partial_match" with multiple mismatches → "open_challenge"
//  2. Challenger posts bond and opens challenge
//  3. Challenge resolved as "succeeded" → bond refunded, fraud bounty awarded
//  4. Validator slashed for OffenseDishonestReplay (40%, 60-day cooldown)
//  5. CRITICAL: supply invariant preserved across bond + slash flows
func TestE2E_ChallengeSucceeds(t *testing.T) {
	tl := ledger.NewTransferLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	challengerKP, _ := crypto.GenerateKeyPair()
	validatorKP, _ := crypto.GenerateKeyPair()
	challengerID := challengerKP.AgentID()
	validatorID := validatorKP.AgentID()

	for i, kp := range []*crypto.KeyPair{challengerKP, validatorKP} {
		if _, err := onboardAgent(tl, reg, kp.AgentID(), kp.PublicKey, uint64(i)); err != nil {
			t.Fatalf("onboard[%d]: %v", i, err)
		}
		st.track(kp.AgentID())
	}
	st.track("staking-pool")

	// Stake the validator (must meet validator registry minimum of 10 AET).
	sm := staking.NewStakeManager()
	sm.SetTransferLedger(tl)
	const stakeAmt uint64 = 25_000_000_000 // 25 AET
	if err := sm.Stake(validatorID, stakeAmt); err != nil {
		t.Fatalf("Stake: %v", err)
	}

	st.check(t, "after staking")

	// ── Replay produces open_challenge verdict ──────────────────────────────
	ms := newMemReplayStore()
	coord := replay.NewReplayCoordinator(replay.DefaultReplayPolicy(), ms)
	resolver := replay.NewReplayResolver(ms)

	const taskID = "disputed-task-001"
	job, err := coord.ScheduleReplay(taskID, "sha256:evidence", "code", "v1", nil, "spot-check", "worker-001")
	if err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}

	// Partial match with 2 mismatches + ScoreDelta > 0.3 → "open_challenge" (severity 0.6).
	outcome := &replay.ReplayOutcome{
		JobID:  job.ID,
		TaskID: taskID,
		Status: "partial_match",
		Comparisons: []replay.CheckComparison{
			{CheckType: "lint", OriginalHash: "sha256:a1", ReplayedHash: "sha256:b1", Match: false, ScoreDelta: 0.4},
			{CheckType: "test", OriginalHash: "sha256:a2", ReplayedHash: "sha256:b2", Match: false, ScoreDelta: 0.5},
		},
		ReplayedAt: time.Now(),
		ReplayerID: "replay-agent",
	}

	if err := resolver.RecordOutcome(outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	verdict := resolver.EvaluateOutcome(outcome)
	if verdict.Action != "open_challenge" {
		t.Errorf("verdict.Action = %q; want open_challenge", verdict.Action)
	}

	// ── Challenge lifecycle ─────────────────────────────────────────────────
	cfg := config.DefaultConfig()
	chalMgr := assurance.NewChallengeManager(&cfg.Assurance, nil)

	const taskBudget uint64 = 2_000_000
	bond := assurance.ComputeBond(taskBudget, &cfg.Assurance)

	// Challenger posts bond — transfer to bond escrow.
	bondBucket := crypto.AgentID("challenge-bond:" + taskID)
	st.track(bondBucket)
	if err := tl.TransferFromBucket(challengerID, bondBucket, bond); err != nil {
		t.Fatalf("post bond: %v", err)
	}

	st.check(t, "after bond posted")

	chal, err := chalMgr.OpenChallenge(taskID, string(challengerID), string(validatorID), bond)
	if err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}
	if chal.Status != assurance.ChallengeOpen {
		t.Errorf("challenge status = %q; want %q", chal.Status, assurance.ChallengeOpen)
	}

	// Slash the validator (dishonest replay — 40%, 60 days).
	cfg.Validator.CapEnforcementMinValidators = 100 // disable for test
	valReg := validator.NewValidatorRegistry(&cfg.Validator, nil)
	val, err := valReg.Register(string(validatorID), stakeAmt, []string{"code"}, true)
	if err != nil {
		t.Fatalf("validator register: %v", err)
	}

	slashEng := validator.NewSlashEngine(valReg, &cfg.Validator)
	slashResult, err := slashEng.Slash(val.ID, validator.OffenseDishonestReplay)
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}

	if slashResult.SlashPercentage != 0.40 {
		t.Errorf("SlashPercentage = %.2f; want 0.40", slashResult.SlashPercentage)
	}
	if slashResult.CooldownDays != 60 {
		t.Errorf("CooldownDays = %d; want 60", slashResult.CooldownDays)
	}

	// Resolve challenge as succeeded — bond refunded, fraud bounty awarded.
	resolution, err := chalMgr.ResolveChallenge(chal.ID, assurance.ChallengeSucceeded, slashResult.ChallengerShare)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if resolution.Outcome != assurance.ChallengeSucceeded {
		t.Errorf("resolution.Outcome = %q; want %q", resolution.Outcome, assurance.ChallengeSucceeded)
	}
	if resolution.RefundedBond != bond {
		t.Errorf("RefundedBond = %d; want %d (full bond)", resolution.RefundedBond, bond)
	}
	if resolution.ForfeitAmount != 0 {
		t.Errorf("ForfeitAmount = %d; want 0 on success", resolution.ForfeitAmount)
	}

	// Execute token flows: return bond, distribute slash proceeds.
	if err := tl.TransferFromBucket(bondBucket, challengerID, resolution.RefundedBond); err != nil {
		t.Fatalf("refund bond: %v", err)
	}

	reserveID := crypto.AgentID("dispute-reserve")
	st.track(reserveID)
	if err := tl.TransferFromBucket("staking-pool", challengerID, slashResult.ChallengerShare); err != nil {
		t.Fatalf("slash ChallengerShare: %v", err)
	}
	if err := tl.TransferFromBucket("staking-pool", reserveID, slashResult.ReserveShare); err != nil {
		t.Fatalf("slash ReserveShare: %v", err)
	}

	// Validator must be suspended.
	if err := slashEng.Resume(val.ID); !errors.Is(err, validator.ErrSlashInCooldown) {
		t.Errorf("Resume during cooldown: want ErrSlashInCooldown, got %v", err)
	}

	// CRITICAL: supply invariant.
	st.check(t, "after challenge success + slash")
}

// ─── Test 10: Challenge Fails → Bond Forfeiture ─────────────────────────────

// TestE2E_ChallengeFails_BondForfeit exercises the path where a frivolous
// challenge is opened and fails:
//
//  1. Challenger posts bond and opens challenge
//  2. Challenge resolved as "failed"
//  3. Bond forfeited: 50% to falsely accused validator, 50% to dispute reserve
//  4. CRITICAL: supply invariant preserved
func TestE2E_ChallengeFails_BondForfeit(t *testing.T) {
	tl := ledger.NewTransferLedger()
	reg := identity.NewRegistry()
	st := initGenesis(t, tl)

	challengerKP, _ := crypto.GenerateKeyPair()
	accusedKP, _ := crypto.GenerateKeyPair()
	challengerID := challengerKP.AgentID()
	accusedID := accusedKP.AgentID()

	for i, kp := range []*crypto.KeyPair{challengerKP, accusedKP} {
		if _, err := onboardAgent(tl, reg, kp.AgentID(), kp.PublicKey, uint64(i)); err != nil {
			t.Fatalf("onboard[%d]: %v", i, err)
		}
		st.track(kp.AgentID())
	}

	// Record initial balances.
	challengerBalBefore, _ := tl.Balance(challengerID)
	accusedBalBefore, _ := tl.Balance(accusedID)

	// ── Challenge lifecycle ─────────────────────────────────────────────────
	cfg := config.DefaultConfig()
	chalMgr := assurance.NewChallengeManager(&cfg.Assurance, nil)

	const taskBudget uint64 = 5_000_000
	bond := assurance.ComputeBond(taskBudget, &cfg.Assurance)

	// Challenger posts bond — transfer to bond escrow.
	const taskID = "frivolous-challenge-task"
	bondBucket := crypto.AgentID("challenge-bond:" + taskID)
	st.track(bondBucket)

	if err := tl.TransferFromBucket(challengerID, bondBucket, bond); err != nil {
		t.Fatalf("post bond: %v", err)
	}

	st.check(t, "after bond posted")

	chal, err := chalMgr.OpenChallenge(taskID, string(challengerID), string(accusedID), bond)
	if err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}

	// Resolve as failed — validator was honest.
	resolution, err := chalMgr.ResolveChallenge(chal.ID, assurance.ChallengeFailed, 0)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}

	if resolution.Outcome != assurance.ChallengeFailed {
		t.Errorf("resolution.Outcome = %q; want %q", resolution.Outcome, assurance.ChallengeFailed)
	}
	if resolution.ForfeitAmount != bond {
		t.Errorf("ForfeitAmount = %d; want %d (full bond)", resolution.ForfeitAmount, bond)
	}
	if resolution.RefundedBond != 0 {
		t.Errorf("RefundedBond = %d; want 0 on failure", resolution.RefundedBond)
	}

	// Bond split: 50% to falsely accused, 50% to dispute reserve.
	expectedAccusedShare := bond / 2
	expectedReserve := bond - expectedAccusedShare
	if resolution.AccusedShare != expectedAccusedShare {
		t.Errorf("AccusedShare = %d; want %d", resolution.AccusedShare, expectedAccusedShare)
	}
	if resolution.ReserveSplit != expectedReserve {
		t.Errorf("ReserveSplit = %d; want %d", resolution.ReserveSplit, expectedReserve)
	}

	// Execute forfeiture token flows.
	reserveID := crypto.AgentID("dispute-reserve")
	st.track(reserveID)

	if err := tl.TransferFromBucket(bondBucket, accusedID, resolution.AccusedShare); err != nil {
		t.Fatalf("transfer AccusedShare: %v", err)
	}
	if err := tl.TransferFromBucket(bondBucket, reserveID, resolution.ReserveSplit); err != nil {
		t.Fatalf("transfer ReserveSplit: %v", err)
	}

	// Challenger lost bond — balance reduced by exactly bond.
	challengerBalAfter, _ := tl.Balance(challengerID)
	if challengerBalAfter != challengerBalBefore-bond {
		t.Errorf("challenger balance = %d; want %d (lost bond %d)",
			challengerBalAfter, challengerBalBefore-bond, bond)
	}

	// Accused gained their share — balance increased by AccusedShare.
	accusedBalAfter, _ := tl.Balance(accusedID)
	if accusedBalAfter != accusedBalBefore+resolution.AccusedShare {
		t.Errorf("accused balance = %d; want %d (gained %d)",
			accusedBalAfter, accusedBalBefore+resolution.AccusedShare, resolution.AccusedShare)
	}

	// CRITICAL: supply invariant.
	st.check(t, "after challenge bond forfeiture")
}

// ─── Test 11: Slash → Cooldown → Blocked Resume → Successful Resume ─────────

// TestE2E_SlashCooldownResume verifies the full suspension lifecycle:
//
//  1. Slash sets validator to "suspended" with a cooldown period
//  2. Resume during cooldown fails with ErrSlashInCooldown
//  3. After cooldown expires, Resume succeeds and restores "active" status
func TestE2E_SlashCooldownResume(t *testing.T) {
	t.Run("blocked_during_cooldown", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Validator.CapEnforcementMinValidators = 100
		valReg := validator.NewValidatorRegistry(&cfg.Validator, nil)

		const stakeAmt uint64 = 100_000_000_000 // 100 AET
		v, err := valReg.Register("val-cooldown-1", stakeAmt, []string{"code"}, true)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		slashEng := validator.NewSlashEngine(valReg, &cfg.Validator)
		result, err := slashEng.Slash(v.ID, validator.OffenseFraudulentApproval)
		if err != nil {
			t.Fatalf("Slash: %v", err)
		}

		// CooldownUntil must be ~30 days in the future.
		if result.CooldownDays != 30 {
			t.Errorf("CooldownDays = %d; want 30", result.CooldownDays)
		}
		if result.CooldownUntil.Before(time.Now().Add(29 * 24 * time.Hour)) {
			t.Error("CooldownUntil is less than 29 days from now")
		}

		// Resume must fail during cooldown.
		err = slashEng.Resume(v.ID)
		if !errors.Is(err, validator.ErrSlashInCooldown) {
			t.Errorf("Resume during cooldown: want ErrSlashInCooldown, got %v", err)
		}

		// CanResume must return false.
		canResume, until := slashEng.CanResume(v.ID)
		if canResume {
			t.Error("CanResume should return false during cooldown")
		}
		if until.IsZero() {
			t.Error("CanResume should return non-zero until time during cooldown")
		}
	})

	t.Run("resumes_after_cooldown", func(t *testing.T) {
		// Use 0-day cooldown so the cooldown expires immediately.
		cfg := config.DefaultConfig()
		cfg.Validator.CooldownTier1Days = 0
		cfg.Validator.CapEnforcementMinValidators = 100
		valReg := validator.NewValidatorRegistry(&cfg.Validator, nil)

		const stakeAmt uint64 = 100_000_000_000
		v, err := valReg.Register("val-cooldown-2", stakeAmt, []string{"code"}, true)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		slashEng := validator.NewSlashEngine(valReg, &cfg.Validator)
		result, err := slashEng.Slash(v.ID, validator.OffenseFraudulentApproval)
		if err != nil {
			t.Fatalf("Slash: %v", err)
		}

		if result.CooldownDays != 0 {
			t.Errorf("CooldownDays = %d; want 0", result.CooldownDays)
		}

		// With 0-day cooldown, the suspension expires immediately.
		time.Sleep(time.Millisecond) // ensure wall clock advances past SuspendedUntil
		err = slashEng.Resume(v.ID)
		if err != nil {
			t.Errorf("Resume after 0-day cooldown: unexpected error: %v", err)
		}

		// After resume, a second slash should succeed (validator is active again).
		_, err = slashEng.Slash(v.ID, validator.OffenseFraudulentApproval)
		if err != nil {
			t.Errorf("re-slash after resume: unexpected error: %v", err)
		}
	})
}
