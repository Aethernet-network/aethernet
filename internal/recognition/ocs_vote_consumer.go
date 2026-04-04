package recognition

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// OCSVoteAcceptor is the minimal interface for routing a verified vote into
// the OCS consensus round. *ocs.Engine satisfies this via AcceptPeerVote.
type OCSVoteAcceptor interface {
	AcceptPeerVote(eventID event.EventID, voterID crypto.AgentID, verdict bool) error
	IsPending(eventID event.EventID) bool
}

// VotePayload mirrors settlement.VerificationVotePayload without importing
// the settlement package to avoid potential import cycles. The JSON tags
// match exactly.
type VotePayload struct {
	Version       uint8  `json:"v"`
	TargetEventID string `json:"target_event_id"`
	VoterID       string `json:"voter_id"`
	Verdict       string `json:"verdict"`
	VerifiedValue uint64 `json:"verified_value"`
	Timestamp     int64  `json:"timestamp"`
}

// Deferred reason codes for vote recognition.
const (
	DeferTargetNotPending = "target_not_ocs_pending"
	DeferTargetMissing    = "target_missing_from_dag"
	DeferPayloadInvalid   = "vote_payload_invalid"
)

// PrerequisiteKeyOCSPending returns the prerequisite key for a vote waiting
// on its target event to become OCS pending.
func PrerequisiteKeyOCSPending(targetEventID string) string {
	return "ocs_pending:" + targetEventID
}

// OCSVoteConsumer is a CommitConsumer that recognizes VerificationVote DAG
// events and routes them into the OCS consensus round via AcceptPeerVote.
//
// Readiness: a vote is ready only when its target event is already OCS-pending
// (i.e., the ocs_submit consumer has recognized it). If the target is committed
// to the DAG but not yet OCS-pending, the vote is deferred with a targeted
// prerequisite key so it can be activated when the target lands.
//
// This consumer runs in parallel with the existing syncHandler and voteHandler
// paths. AcceptPeerVote is idempotent (duplicate votes are silently accepted
// by RegisterVote which skips already-voted pairs).
//
// The MsgVote wire path remains for low-latency direct propagation. This
// consumer handles the durable DAG event path.
type OCSVoteConsumer struct {
	acceptor OCSVoteAcceptor
}

// NewOCSVoteConsumer creates a consumer wired to the given OCS engine.
func NewOCSVoteConsumer(acceptor OCSVoteAcceptor) *OCSVoteConsumer {
	return &OCSVoteConsumer{acceptor: acceptor}
}

// Name returns the unique consumer identifier.
func (c *OCSVoteConsumer) Name() string { return "ocs_vote" }

// Interested returns true for VerificationVote events.
func (c *OCSVoteConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeVerificationVote
}

// Ready checks whether the vote's target event is OCS-pending. If not,
// returns deferred with a prerequisite key for targeted activation.
func (c *OCSVoteConsumer) Ready(_ context.Context, ev *event.Event, view ReadModel) (bool, string, error) {
	vp, err := c.parsePayload(ev)
	if err != nil {
		// Invalid payload — mark as ready (consumed as no-op) to prevent
		// infinite deferral of unparseable events.
		slog.Debug("recognition: ocs_vote invalid payload",
			"event_id", ev.ID, "err", err)
		return true, "", nil
	}

	targetID := event.EventID(vp.TargetEventID)

	// Check that the target event exists in the DAG.
	if view != nil && !view.HasEvent(targetID) {
		prereq := PrerequisiteKeyOCSPending(vp.TargetEventID)
		return false, prereq, nil
	}

	// Check that the target event is OCS-pending (has been recognized by
	// the ocs_submit consumer). This ensures the vote is counted in the
	// correct consensus round context.
	if !c.acceptor.IsPending(targetID) {
		prereq := PrerequisiteKeyOCSPending(vp.TargetEventID)
		return false, prereq, nil
	}

	return true, "", nil
}

// Consume routes the vote into the OCS consensus round. Idempotent:
// AcceptPeerVote tolerates duplicate votes via RegisterVote dedup.
func (c *OCSVoteConsumer) Consume(_ context.Context, ev *event.Event) error {
	vp, err := c.parsePayload(ev)
	if err != nil {
		// Invalid payload — consume as no-op. Already logged in Ready.
		return nil
	}

	targetID := event.EventID(vp.TargetEventID)
	voterID := crypto.AgentID(vp.VoterID)
	verdict := vp.Verdict == "accepted"

	if err := c.acceptor.AcceptPeerVote(targetID, voterID, verdict); err != nil {
		slog.Warn("recognition: ocs_vote consume failed",
			"event_id", ev.ID,
			"target", vp.TargetEventID,
			"voter", vp.VoterID,
			"err", err,
		)
		return err
	}

	slog.Info("recognition: ocs_vote consumed",
		"event_id", ev.ID,
		"target", vp.TargetEventID,
		"voter", vp.VoterID,
		"verdict", verdict,
	)
	return nil
}

// parsePayload extracts VotePayload from the event.
func (c *OCSVoteConsumer) parsePayload(ev *event.Event) (*VotePayload, error) {
	if ev.Payload == nil {
		return nil, fmt.Errorf("nil payload")
	}
	var vp VotePayload
	if err := json.Unmarshal(ev.Payload, &vp); err != nil {
		return nil, err
	}
	if vp.TargetEventID == "" || vp.VoterID == "" {
		return nil, fmt.Errorf("missing target_event_id or voter_id")
	}
	return &vp, nil
}

// Compile-time assertion.
var _ CommitConsumer = (*OCSVoteConsumer)(nil)
