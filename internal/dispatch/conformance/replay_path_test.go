package conformance_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// replayConsumer is a minimal Type A dispatch.Consumer used by
// TestSyntheticConsumer_ReplayConformance. Filters on EventTypeTransfer
// — the corpus is all Transfer events plus a Generation probe injected
// by the NonInterestedSkipped sub-test.
type replayConsumer struct {
	mu      sync.Mutex
	applied map[event.EventID]bool
}

func newReplayConsumer() *replayConsumer {
	return &replayConsumer{applied: make(map[event.EventID]bool)}
}

func (c *replayConsumer) Name() string                                { return "synthetic-replay-consumer" }
func (c *replayConsumer) Type() dispatch.ConsumerType                 { return dispatch.TypeA }
func (c *replayConsumer) Interested(ev *event.Event) bool             { return ev.Type == event.EventTypeTransfer }
func (c *replayConsumer) Prerequisites(_ *event.Event) []event.EventID { return nil }
func (c *replayConsumer) PrerequisiteSchemaVersion() uint32           { return 0 }

func (c *replayConsumer) Apply(_ context.Context, ev *event.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applied[ev.ID] = true
	return nil
}

func (c *replayConsumer) RecoveryProbe(_ context.Context, ev *event.Event) (dispatch.RecoveryStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applied[ev.ID] {
		return dispatch.RecoveryCompleted, nil
	}
	return dispatch.RecoveryNotStarted, nil
}

// makeReplayCorpus returns three signed, root-only Transfer events.
// Each event is created with a fresh keypair so they are byte-distinct
// (different AgentID → different canonical bytes → different EventID),
// avoiding dag.Add's ErrDuplicateEvent path.
//
// All three are root events (no causal refs) so the corpus order is
// trivially valid for sequential dag.Add insertion. The replay path
// under test (recognition.ReplayHistoricalToBusConsumers) is what
// exercises topological emission downstream.
func makeReplayCorpus(t *testing.T) []*event.Event {
	t.Helper()
	const n = 3
	events := make([]*event.Event, 0, n)
	for i := 0; i < n; i++ {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatalf("corpus: keypair[%d]: %v", i, err)
		}
		aid := string(kp.AgentID())
		ev, err := event.New(
			event.EventTypeTransfer,
			nil,
			event.TransferPayload{
				Version:   1,
				FromAgent: aid,
				ToAgent:   "sink",
				Amount:    uint64(100 + i),
				Currency:  "AET",
			},
			aid,
			nil,
			0,
		)
		if err != nil {
			t.Fatalf("corpus: event.New[%d]: %v", i, err)
		}
		if err := crypto.SignEvent(ev, kp); err != nil {
			t.Fatalf("corpus: sign[%d]: %v", i, err)
		}
		events = append(events, ev)
	}
	return events
}

// TestSyntheticConsumer_ReplayConformance is the F4A step 2 RED gate.
//
// The synthetic consumer + corpus exercise the replay-conformance
// template defined in replay_path.go. The template drives the full
// recognition bus → consumer chain:
//
//  1. populate a fresh DAG with the corpus,
//  2. register the consumer (wrapped as a recognition.CommitConsumer),
//  3. invoke recognition.ReplayHistoricalToBusConsumers,
//  4. assert Apply fired for every Interested() corpus event.
//
// The test FAILS today because recognition.ReplayHistoricalToBusConsumers
// is a stub (internal/recognition/replay.go) that returns nil without
// emitting anything. F4A step 3 implements the body and the test flips
// RED → GREEN.
//
// To keep CI green during the RED window, the test is skipped by
// default. Set F4_REPLAY_TEMPLATE_RED=1 to lift the skip and observe the
// captured failure (snapshot in
// testdata/replay_template_red_baseline.txt). When step 3 lands the
// skip is removed permanently.
func TestSyntheticConsumer_ReplayConformance(t *testing.T) {
	if os.Getenv("F4_REPLAY_TEMPLATE_RED") != "1" {
		t.Skip("RED — pending F4A step 3 (recognition.ReplayHistoricalToBusConsumers " +
			"implementation, F4 plan §8.1). " +
			"Captured failure: testdata/replay_template_red_baseline.txt. " +
			"Re-run with F4_REPLAY_TEMPLATE_RED=1 to confirm the RED state.")
	}

	conformance.RunReplayConformance(t,
		func() (dispatch.Consumer, func()) {
			c := newReplayConsumer()
			return c, func() {}
		},
		conformance.ReplayCorpusFunc(makeReplayCorpus),
	)
}
