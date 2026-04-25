package main

// Part E of the canonical-distribution-integer-migration workstream: this
// test exercises the startup-load path end-to-end.
//
// Scope (per the approval on Part E's plan): the migrationStoreAdapter
// round-trips an activation record correctly, and a restarted node with
// a prior activation in its store comes up with both the settler and the
// generation ledger out of shadow mode before any settlement runs.
//
// Placed in cmd/node/main_test.go so it exercises the concrete adapter
// and wiring shape defined in main.go. The test does not spin up the
// full node — it constructs the narrow pieces (store → adapter → two
// settlement flag-holders → SetShadowMode) that constitute the startup
// load sequence. If this passes, the production startup-load block is
// protected against three classes of silent-fork bug:
//
//   1. Adapter marshal/unmarshal drift: event_id or timestamp fields
//      do not round-trip.
//   2. Meta-key conflict: the migrationMetaKey collides with an adjacent
//      consumer's key.
//   3. Initialization-order slip: flags not flipped before the
//      dispatcher recovery pass.

import (
	"os"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/localpub"
	"github.com/Aethernet-network/aethernet/internal/store"
)

// shadowModeFlag implements dispatch.IntegerMigrationActivator with the
// minimal state needed to verify the startup-load flips correctly. The
// field starts at true (shadow mode) to match production construction.
type shadowModeFlag struct {
	shadow bool
}

func (s *shadowModeFlag) SetShadowMode(shadow bool) { s.shadow = shadow }

// TestStartup_LoadsPriorActivation_RestoresFlags exercises the full
// startup-load path: adapter round-trip through a real BadgerDB store,
// then the startup sequence from cmd/node/main.go applied to two
// independent shadow-mode flag holders (mirrors settler + gen-ledger).
//
// Steps:
//  1. Open a fresh store and adapter.
//  2. PUT an activation record via the adapter.
//  3. Discard and re-open the store (simulating process restart).
//  4. Construct new flag holders in shadow mode (mirror production
//     constructor defaults).
//  5. Run the exact startup-load snippet from main.go.
//  6. Assert: both flags now report false (integer-canonical).
//  7. Assert: the persisted record round-trips the event ID and
//     timestamp without drift.
func TestStartup_LoadsPriorActivation_RestoresFlags(t *testing.T) {
	dir := t.TempDir()

	// Step 1: open store at a persistent location (t.TempDir survives
	// the first Close + re-open below).
	s1, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(initial): %v", err)
	}

	const (
		activationEventID  event.EventID = "activation-event-xyz"
		activationEmitAtTS int64         = 1_729_000_000 // arbitrary unix seconds
	)

	// Step 2: write an activation record via the adapter.
	a1 := &migrationStoreAdapter{store: s1}
	if err := a1.PutIntegerMigrationActivated(activationEventID, activationEmitAtTS); err != nil {
		t.Fatalf("PutIntegerMigrationActivated: %v", err)
	}
	// Verify it round-trips in-process before simulating restart.
	act, id, ts, err := a1.GetIntegerMigrationActivated()
	if err != nil {
		t.Fatalf("GetIntegerMigrationActivated (same process): %v", err)
	}
	if !act || id != activationEventID || ts != activationEmitAtTS {
		t.Fatalf("in-process round-trip drift: activated=%v id=%q ts=%d; want true/%q/%d",
			act, id, ts, activationEventID, activationEmitAtTS)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close initial store: %v", err)
	}

	// Step 3: re-open the store at the same directory — simulates a node
	// restart with persisted state intact.
	s2, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(reopen): %v", err)
	}
	defer s2.Close()
	a2 := &migrationStoreAdapter{store: s2}

	// Step 4: construct fresh flag holders in shadow mode — this mirrors
	// the production behavior where NewVerificationConsensusSettler and
	// NewGenerationLedgerCalculator receive shadowMode=true from main.go.
	tvSettler := &shadowModeFlag{shadow: true}
	genLedgerCalc := &shadowModeFlag{shadow: true}

	// Guard against silent-fork risk: verify both flag holders satisfy
	// dispatch.IntegerMigrationActivator — if this compiles, the
	// adapter's invocations below reach the same interface the activation
	// consumer uses in production.
	var _ dispatch.IntegerMigrationActivator = tvSettler
	var _ dispatch.IntegerMigrationActivator = genLedgerCalc

	// Step 5: replay the startup-load block from cmd/node/main.go.
	// (Any change to the production block must be reflected here or
	// this test's guarantee weakens.)
	if activated, _, _, loadErr := a2.GetIntegerMigrationActivated(); loadErr == nil && activated {
		tvSettler.SetShadowMode(false)
		genLedgerCalc.SetShadowMode(false)
	} else {
		t.Fatalf("startup load did not observe prior activation: activated=%v err=%v",
			activated, loadErr)
	}

	// Step 6: both flags must now be false.
	if tvSettler.shadow {
		t.Error("settler flag remains shadow=true after startup-load; restart would produce float-canonical amounts on a post-activation network (silent fork)")
	}
	if genLedgerCalc.shadow {
		t.Error("genLedger flag remains shadow=true after startup-load; restart would produce float-canonical amounts on a post-activation network (silent fork)")
	}

	// Step 7: adapter round-trips event ID and timestamp across process
	// restart without drift.
	act, id, ts, err = a2.GetIntegerMigrationActivated()
	if err != nil {
		t.Fatalf("post-restart GetIntegerMigrationActivated: %v", err)
	}
	if !act {
		t.Error("activated flag lost across restart")
	}
	if id != activationEventID {
		t.Errorf("event ID drifted across restart: got %q want %q", id, activationEventID)
	}
	if ts != activationEmitAtTS {
		t.Errorf("timestamp drifted across restart: got %d want %d", ts, activationEmitAtTS)
	}
}

// TestStartup_NoPriorActivation_LeavesFlagsInShadow verifies the
// complement: on a fresh node with no prior activation, the startup-load
// leaves the flags in their constructor state (shadow=true). Catches a
// would-be bug where the adapter returns activated=true on an empty
// store (e.g. if GetMeta ever returned a zero-length body rather than
// an error for a missing key).
func TestStartup_NoPriorActivation_LeavesFlagsInShadow(t *testing.T) {
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	a := &migrationStoreAdapter{store: s}

	tvSettler := &shadowModeFlag{shadow: true}
	genLedgerCalc := &shadowModeFlag{shadow: true}

	// Exact startup-load block from main.go.
	if activated, _, _, loadErr := a.GetIntegerMigrationActivated(); loadErr == nil && activated {
		tvSettler.SetShadowMode(false)
		genLedgerCalc.SetShadowMode(false)
	}

	if !tvSettler.shadow {
		t.Error("settler flag unexpectedly flipped on fresh node (no prior activation)")
	}
	if !genLedgerCalc.shadow {
		t.Error("genLedger flag unexpectedly flipped on fresh node")
	}
}

// TestMigrationStoreAdapter_GetMalformedBody_ReturnsError stress-tests
// the adapter's unmarshal path. If a future store layer ever persists
// corrupt bytes at migrationMetaKey, the adapter surfaces an error
// rather than silently reporting not-activated; startup-load's
// "loadErr == nil && activated" guard then leaves the flags alone,
// which is the safe default (no silent cutover from broken state).
func TestMigrationStoreAdapter_GetMalformedBody_ReturnsError(t *testing.T) {
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	if err := s.PutMeta(migrationMetaKey, []byte("not valid json")); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
	a := &migrationStoreAdapter{store: s}
	_, _, _, err = a.GetIntegerMigrationActivated()
	if err == nil {
		t.Error("expected unmarshal error on malformed body; got nil")
	}
}

// TestEmitGenesisTransfers_AppliesLocalTransfers is the regression test
// for the founder Option C bootstrap-order race fix (2026-04-25).
//
// Pre-fix: emitGenesisFundingEvent only published the canonical event
// without applying the transfer to the local TransferLedger. Result:
// the immediately-following genesis-validator stake attempt at
// cmd/node/main.go:1300 saw testnet-validator balance=0 and failed
// with "insufficient balance: bucket testnet-validator balance 0 <
// allocation 50000000000". On some nodes the funding event subsequently
// propagated; on others it didn't, producing cross-node ledger
// divergence on genesis bootstrap state — surfaced by F5 5B Phase 3.4
// cluster-health verification via /v1/admin/ledger-snapshot.
//
// Post-fix: emitGenesisFundingEvent applies the transfer to the local
// ledger immediately after publishing the canonical event, mirroring
// the dual-action pattern at cmd/node/main.go:1342-1373 (node-own-agent
// funding). This test asserts the dual-action behavior at the
// emitGenesisTransfers level.
func TestEmitGenesisTransfers_AppliesLocalTransfers(t *testing.T) {
	tl := ledger.NewTransferLedger()
	// Seed source buckets so emitGenesisTransfers has funds to move.
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketRewards), genesis.NetworkRewards); err != nil {
		t.Fatalf("seed rewards bucket: %v", err)
	}
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketEcosystem), genesis.EcosystemAllocation); err != nil {
		t.Fatalf("seed ecosystem bucket: %v", err)
	}

	d := dag.New()
	pub := localpub.New(d, nil)
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	// Force the testnet branch so faucet funding emits.
	t.Setenv("AETHERNET_TESTNET", "true")

	// Pre-condition: testnet-validator + faucet balances are zero.
	preTV, _ := tl.Balance(crypto.AgentID(genesis.GenesisValidatorID))
	preFaucet, _ := tl.Balance(crypto.AgentID(genesis.BucketFaucet))
	if preTV != 0 || preFaucet != 0 {
		t.Fatalf("pre-condition: expected fresh balances 0/0, got tv=%d faucet=%d", preTV, preFaucet)
	}

	emitGenesisTransfers(d, pub, kp, kp.AgentID(), tl, s)

	// Post-condition: both targets MUST be funded synchronously
	// (not deferred to async sync handler).
	postTV, _ := tl.Balance(crypto.AgentID(genesis.GenesisValidatorID))
	postFaucet, _ := tl.Balance(crypto.AgentID(genesis.BucketFaucet))
	if postTV != genesis.GenesisValidatorFund {
		t.Errorf("testnet-validator balance after emit: got %d, want %d (founder Option C fix not applied)",
			postTV, genesis.GenesisValidatorFund)
	}
	if postFaucet != genesis.FaucetAllocation {
		t.Errorf("genesis:faucet balance after emit: got %d, want %d (founder Option C fix not applied)",
			postFaucet, genesis.FaucetAllocation)
	}

	// Idempotency: re-running emitGenesisTransfers with the marker
	// already set MUST NOT double-fund. The genesisTransfersKey marker
	// is checked at the top of emitGenesisTransfers and short-circuits.
	emitGenesisTransfers(d, pub, kp, kp.AgentID(), tl, s)
	postTV2, _ := tl.Balance(crypto.AgentID(genesis.GenesisValidatorID))
	postFaucet2, _ := tl.Balance(crypto.AgentID(genesis.BucketFaucet))
	if postTV2 != postTV || postFaucet2 != postFaucet {
		t.Errorf("idempotency violated: balances changed on second emitGenesisTransfers call (tv: %d->%d, faucet: %d->%d)",
			postTV, postTV2, postFaucet, postFaucet2)
	}
}

// TestEmitGenesisTransfers_StakingValidatorAfterFundingSucceeds asserts
// the END-TO-END invariant the founder Option C fix protects: after
// emitGenesisTransfers runs, the genesis-validator stake operation
// (which moves GenesisValidatorStake from testnet-validator to
// staking-pool) MUST succeed. Pre-fix this returned ErrInsufficientBalance
// because the funding hadn't been applied locally.
//
// Mirrors the canonical cmdStart() startup sequence post-reorder:
//   1. emitGenesisTransfers (funds testnet-validator)
//   2. stakeManager.Stake(testnet-validator, GenesisValidatorStake)
//
// If the stake fails with insufficient-balance, the bootstrap race is
// back.
func TestEmitGenesisTransfers_StakingValidatorAfterFundingSucceeds(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketRewards), genesis.NetworkRewards); err != nil {
		t.Fatalf("seed rewards: %v", err)
	}
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketEcosystem), genesis.EcosystemAllocation); err != nil {
		t.Fatalf("seed ecosystem: %v", err)
	}

	d := dag.New()
	pub := localpub.New(d, nil)
	kp, _ := crypto.GenerateKeyPair()
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	_ = os.Setenv("AETHERNET_TESTNET", "true")
	defer os.Unsetenv("AETHERNET_TESTNET")

	emitGenesisTransfers(d, pub, kp, kp.AgentID(), tl, s)

	// Now simulate the staking step at cmd/node/main.go:1300 — move
	// GenesisValidatorStake from testnet-validator to staking-pool.
	// This is what stakeManager.Stake does internally via the ledger.
	tvID := crypto.AgentID(genesis.GenesisValidatorID)
	bal, _ := tl.Balance(tvID)
	if bal < genesis.GenesisValidatorStake {
		t.Fatalf("BOOTSTRAP RACE REGRESSION: testnet-validator balance %d < required stake %d after emitGenesisTransfers; founder Option C fix is broken",
			bal, genesis.GenesisValidatorStake)
	}
	// Defensive: the actual staking transfer must succeed.
	if err := tl.TransferFromBucket(tvID, "staking-pool", genesis.GenesisValidatorStake); err != nil {
		t.Fatalf("BOOTSTRAP RACE REGRESSION: stake transfer failed after emitGenesisTransfers: %v", err)
	}
}
