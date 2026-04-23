package conformance_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// makeLKReplayCorpus returns 5 signed Transfer events designed to
// exercise the per-key Apply guarantee:
//
//   - 3 events from agent "alice" (same FromAgent → same LogicalKey)
//   - 2 events from agent "bob"   (same FromAgent → same LogicalKey)
//
// The synthetic Type E consumer projects FromAgent as the LogicalKey.
// After replay, Apply should fire exactly once per distinct key —
// ie. once for "alice" and once for "bob", NOT once per event.
//
// All 5 events are signed with distinct keypairs against their own
// FromAgent values — so canonical bytes differ even for events
// sharing a FromAgent. This guarantees distinct EventIDs and
// distinct content-hash admission keys. The dedup we're testing is
// the LOGICAL-key one: distinct events must collapse to one Apply
// when their projected key matches.
func makeLKReplayCorpus(t *testing.T) []*event.Event {
	t.Helper()
	// Shared per-agent keypairs so all events from the same agent use
	// the same signing key. This yields distinct canonical bytes (and
	// therefore distinct EventIDs) because the Transfer Amount field
	// differs, but the FromAgent projection lands the same LogicalKey.
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("keypair alice: %v", err)
	}
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("keypair bob: %v", err)
	}

	makeTransfer := func(kp *crypto.KeyPair, agentID, toAgent string, amount uint64, seq uint64) *event.Event {
		ev, newErr := event.New(
			event.EventTypeTransfer,
			nil,
			event.TransferPayload{
				Version:   1,
				FromAgent: agentID,
				ToAgent:   toAgent,
				Amount:    amount,
				Currency:  "AET",
			},
			agentID,
			nil,
			seq,
		)
		if newErr != nil {
			t.Fatalf("event.New: %v", newErr)
		}
		if signErr := crypto.SignEvent(ev, kp); signErr != nil {
			t.Fatalf("sign: %v", signErr)
		}
		return ev
	}

	aliceID := string(aliceKP.AgentID())
	bobID := string(bobKP.AgentID())

	return []*event.Event{
		makeTransfer(aliceKP, aliceID, "sink", 10, 1),
		makeTransfer(aliceKP, aliceID, "sink", 20, 2),
		makeTransfer(aliceKP, aliceID, "sink", 30, 3),
		makeTransfer(bobKP, bobID, "sink", 40, 4),
		makeTransfer(bobKP, bobID, "sink", 50, 5),
	}
}

// TestTypeE_SyntheticReplayConformance exercises
// RunLogicalKeyReplayConformance against the synthetic Type E consumer
// from synthetic_test.go. Validates:
//
//  1. PopulatedDAGReplay_PerKey: Apply fires exactly once for "alice"
//     (across 3 events) and exactly once for "bob" (across 2 events) —
//     the per-key Apply guarantee.
//  2. ReplayIdempotent_PerKey: second replay causes no additional
//     Apply calls.
//  3. NonInterestedSkipped: the synthetic is Interested in every
//     event, so this sub-test t.Skip's (degenerate case).
func TestTypeE_SyntheticReplayConformance(t *testing.T) {
	conformance.RunLogicalKeyReplayConformance(t,
		func() (dispatch.LogicalKeyConsumer, func()) {
			c := &syntheticTypeE{applied: make(map[dispatch.LogicalKey]int)}
			return c, func() {}
		},
		conformance.ReplayCorpusFunc(makeLKReplayCorpus),
	)
}
