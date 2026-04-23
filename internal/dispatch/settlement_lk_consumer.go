package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// SettlementLogicalKeyConsumer is the F4B-era Type E consumer for
// Settlement events (F4 plan v2 §5.2.2). It replaces the F3-B
// recognition-bus direct-Apply path for Settlement canonical effects.
//
// Admission is keyed by the Settlement payload's TargetEventID
// (AdmissionStrategyLogicalKey, locked-invariant review §3.5).
// Multiple validators may emit Settlement events independently for
// the same target (each node's consensus finalization callback fires
// locally); per C-17 (Serialization-2), this consumer ignores the
// triggering event's payload Attestations / Verdict fields and
// derives the canonical outcome from the local VotingRound's
// VoteRecord, which is cluster-uniform underlying state.
//
// Key properties (F4 plan v2 §4.5, §4.7):
//
//   - Apply fires exactly once per TargetEventID regardless of how
//     many byte-distinct Settlement events the cluster emits.
//   - IsComplete is deterministic on canonical state: the VoteRecord
//     is finalized. The BFT supermajority seal is enforced by the
//     consensus VotingRound at the time of finalization; once
//     Finalized=true the outcome cannot change.
//   - DeriveOutcome computes Verdict + ParticipatingIDs from the
//     VoteRecord. ScoreBP is 0 because Settlement payloads do not
//     carry a score (scoring is a Task-level concept — see the
//     TVConsensus consumer in tv_consensus_lk_consumer.go).
//   - RecoveryProbe consults settlementApp.IsApplied(TargetEventID)
//     as positive evidence of Apply completion on a prior instance.
//     IsApplied is the last state transition inside Applicator.Apply
//     (after all ledger / identity / fee / metrics side effects);
//     a true return is durable evidence the full settlement ran.
//
// RoundState-fetching convention: per locked-invariant review §3.6,
// the shared dispatch.RoundState struct is the union of logical-key
// consumer needs. The consensus.VoteRecord type does not fit the
// declared Votes []*event.Event slot (it's an aggregated in-memory
// record, not a slice of DAG vote events), so this consumer uses
// the "consumer-local typed helper" escape valve (voteRecordFor
// below) for IsComplete / DeriveOutcome / Apply. See §3.6 for the
// rationale and prior art (TVConsensusLogicalKeyConsumer.roundFor).
//
// Synthetic-payload construction at Apply time: the
// settlementApp.Apply entry point accepts a *SettlementPayload.
// Under C-17 the triggering event's payload is advisory (may carry
// a stale or conflicting Verdict / Attestations); this consumer
// constructs a fresh payload from the canonical VoteRecord +
// derived Outcome so the applicator sees cluster-uniform inputs.
// TargetEventID = LogicalKey (tautological, per admission strategy).
// ConsensusRound = record.FinalOrder (canonical assignment at
// finalization). Attestations are built from record.Votes map
// (every voter), sorted by VoterID for deterministic serialization
// (settlement.VoterAttestation → SortAttestations is not called on
// the synthetic because we emit sorted directly).
type SettlementLogicalKeyConsumer struct {
	settlementApp  *settlement.Applicator
	votingRound    *consensus.VotingRound
	activeWeightFn func() uint64
}

// NewSettlementLogicalKeyConsumer constructs the Type E Settlement
// consumer. settlementApp and votingRound are required; activeWeightFn
// is optional (future-facing — Settlement IsComplete does not
// currently consult active weight since VotingRound.Finalized is
// already guarded by the BFT supermajority rule at finalization time,
// but the dependency is accepted at construction so callers can
// supply it uniformly with the TVConsensus consumer's signature).
func NewSettlementLogicalKeyConsumer(
	settlementApp *settlement.Applicator,
	votingRound *consensus.VotingRound,
	activeWeightFn func() uint64,
) *SettlementLogicalKeyConsumer {
	return &SettlementLogicalKeyConsumer{
		settlementApp:  settlementApp,
		votingRound:    votingRound,
		activeWeightFn: activeWeightFn,
	}
}

// Name is the unique consumer identifier. Distinct from the legacy
// recognition-bus "settlement" consumer so admission-store records
// for the two paths never collide on name.
func (c *SettlementLogicalKeyConsumer) Name() string {
	return "settlement_lk"
}

// Interested reports whether the event is a Settlement event.
func (c *SettlementLogicalKeyConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeSettlement
}

// Key projects the event payload's TargetEventID as the logical
// admission key. An unparsable payload or empty TargetEventID is a
// programming / routing bug — return an error so the dispatcher's
// admitOneLogicalKey surfaces it loudly.
func (c *SettlementLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	payload, err := event.GetPayload[settlement.SettlementPayload](ev)
	if err != nil {
		return "", fmt.Errorf("settlement_lk: unmarshal payload: %w", err)
	}
	if payload.TargetEventID == "" {
		return "", errors.New("settlement_lk: empty TargetEventID")
	}
	return LogicalKey(payload.TargetEventID), nil
}

// RoundState populates only LogicalKey + Epoch on the shared struct.
// Per §3.6 convention: the consensus.VoteRecord needed by IsComplete
// / DeriveOutcome / Apply does not fit the declared Votes []*event.Event
// shape (aggregated in-memory record, not canonical DAG events), so
// this consumer uses the consumer-local voteRecordFor helper instead
// of populating rs.Votes.
//
// Absence of a VoteRecord for the target is not a fatal error — the
// local node may not have processed the target event yet. IsComplete
// then returns false; the dispatcher persists StateProcessing and
// defers to the next Admit after votes land.
func (c *SettlementLogicalKeyConsumer) RoundState(ctx context.Context, key LogicalKey) (RoundState, error) {
	epoch := uint64(0)
	// The dispatcher's epochFn is not reachable from here; pass
	// through what we have. The Epoch field is not consulted by
	// IsComplete/DeriveOutcome for Settlement — included for
	// observability and to satisfy the shared-struct convention for
	// consumers that may care (none today).
	return RoundState{
		LogicalKey: key,
		Epoch:      epoch,
	}, nil
}

// voteRecordFor is the typed side-channel per §3.6 convention.
// Returns the VoteRecord keyed by the logical key's underlying
// event.EventID. A not-found lookup returns (nil, nil) — absence of
// a record is handled by callers as "not yet complete," not as an
// error.
func (c *SettlementLogicalKeyConsumer) voteRecordFor(key LogicalKey) (*consensus.VoteRecord, error) {
	if c.votingRound == nil {
		return nil, nil
	}
	record, err := c.votingRound.GetRecord(event.EventID(key))
	if err != nil {
		if errors.Is(err, consensus.ErrEventNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("settlement_lk: get vote record for %s: %w", key, err)
	}
	return record, nil
}

// IsComplete returns true when the VoteRecord for the target is
// finalized. Per consensus/voting.go the Finalized flag is set
// atomically at the same mutex acquisition that assigns FinalOrder
// / FinalVerdict / FinalVerifiedValue, so a finalized record has
// cluster-uniform canonical state.
//
// A missing record means the node has not yet received or processed
// the target event; return false so the dispatcher defers.
func (c *SettlementLogicalKeyConsumer) IsComplete(rs RoundState) (bool, error) {
	record, err := c.voteRecordFor(rs.LogicalKey)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, nil
	}
	return record.Finalized, nil
}

// DeriveOutcome computes the canonical Outcome for a finalized
// VoteRecord. Preconditions: IsComplete returned true for rs.LogicalKey.
//
// Verdict selection: record.FinalVerdict (bool) maps to
// VerdictAccept / VerdictReject. Determined by the BFT supermajority
// rule at finalization time inside VotingRound.TallyVotes, NOT by
// vote-weight comparison at Derive time — the VoteRecord has already
// locked in the winning verdict.
//
// ParticipatingIDs: every validator whose vote was recorded, in
// lex order for determinism (E.P1 participant-ordering invariant).
//
// ScoreBP: 0. Settlement payloads do not carry a score; scoring is
// a Task-level concept handled by the TVConsensus consumer.
func (c *SettlementLogicalKeyConsumer) DeriveOutcome(rs RoundState) (Outcome, error) {
	record, err := c.voteRecordFor(rs.LogicalKey)
	if err != nil {
		return Outcome{}, err
	}
	if record == nil {
		return Outcome{}, fmt.Errorf("settlement_lk: DeriveOutcome called with no VoteRecord for key %s", rs.LogicalKey)
	}
	if !record.Finalized {
		return Outcome{}, fmt.Errorf("settlement_lk: DeriveOutcome called on non-finalized record for key %s; IsComplete contract violated", rs.LogicalKey)
	}

	var verdict Verdict
	if record.FinalVerdict {
		verdict = VerdictAccept
	} else {
		verdict = VerdictReject
	}

	ids := make([]crypto.AgentID, 0, len(record.Votes))
	// safe: collected then sorted before return; no iteration-order leak
	for id := range record.Votes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return Outcome{
		Verdict:          verdict,
		ScoreBP:          0,
		ParticipatingIDs: ids,
	}, nil
}

// Apply invokes settlementApp.Apply with a synthetic SettlementPayload
// constructed from the canonical VoteRecord + derived Outcome.
//
// Idempotency: settlementApp.Apply is already keyed by TargetEventID
// in its internal applied set; re-Apply after a crash-recovery window
// is safe and returns nil. Combined with the dispatcher's per-
// (consumer, key) admission-state machine, Apply fires at most once
// per (settlement_lk, TargetEventID) absent a crash, and at most
// twice across any crash-recovery window.
//
// Synthetic Verdict mapping: the applicator's Apply switches on
// settlement.VerdictAccepted / VerdictRejected (strings "accepted" /
// "rejected"). Outcome.Verdict is VerdictAccept / VerdictReject.
// The mapping below is explicit; unknown Verdict values return an
// error rather than falling through to a silent default.
func (c *SettlementLogicalKeyConsumer) Apply(ctx context.Context, key LogicalKey, outcome Outcome) error {
	record, err := c.voteRecordFor(key)
	if err != nil {
		return fmt.Errorf("settlement_lk: load vote record: %w", err)
	}
	if record == nil {
		return fmt.Errorf("settlement_lk: vote record %s missing at Apply time", key)
	}

	verdictString, err := verdictToSettlementString(outcome.Verdict)
	if err != nil {
		return fmt.Errorf("settlement_lk: %w for key %s", err, key)
	}

	// Build attestations from the canonical VoteRecord. Per E.P1 the
	// slice is built in sorted (VoterID lex) order to guarantee
	// deterministic serialization across nodes. Note: weights are
	// computed per-voter using the BoundSnapshot's weight function
	// when the snapshot is present; when absent (backward-compat /
	// pre-snapshot records), weight is 0. The applicator does not
	// consult weight for any canonical effect — it's recorded for
	// audit / observability (settlement.VoterAttestation.Weight).
	voterIDs := make([]crypto.AgentID, 0, len(record.Votes))
	// safe: collected then sorted before iteration; deterministic
	// output order enforced below
	for id := range record.Votes {
		voterIDs = append(voterIDs, id)
	}
	sort.Slice(voterIDs, func(i, j int) bool { return voterIDs[i] < voterIDs[j] })

	attestations := make([]settlement.VoterAttestation, 0, len(voterIDs))
	for _, id := range voterIDs {
		yes := record.Votes[id]
		perVoterVerdict := string(settlement.VerdictRejected)
		if yes {
			perVoterVerdict = string(settlement.VerdictAccepted)
		}
		weight := uint64(0)
		if record.BoundSnapshot != nil {
			// BoundSnapshot exposes per-validator weight as the
			// (reputation × stake) / 10000 product captured at round
			// creation. Backward compat: if the voter is not in the
			// snapshot (ineligible / not-participating), their weight
			// is 0. VoteWeightByKey is the canonical accessor per
			// ValidatorSetSource.
			if w, ok := record.BoundSnapshot.VoteWeightByKey(id); ok {
				weight = w
			}
		}
		attestations = append(attestations, settlement.VoterAttestation{
			VoterID: string(id),
			Verdict: perVoterVerdict,
			Weight:  weight,
		})
	}

	payload := settlement.SettlementPayload{
		Version:        1,
		TargetEventID:  string(key),
		Verdict:        verdictString,
		VerifiedValue:  record.FinalVerifiedValue,
		ConsensusRound: record.FinalOrder,
		Attestations:   attestations,
	}

	if err := c.settlementApp.Apply(&payload); err != nil {
		return fmt.Errorf("settlement_lk: apply %s: %w", key, err)
	}
	_ = ctx // settlementApp.Apply does not currently accept ctx; reserved for future
	return nil
}

// RecoveryProbe returns RecoveryCompleted iff the applicator reports
// the TargetEventID is in its applied set. Per C-14: evidence-based,
// monotonic, replay-safe — IsApplied is set as the last step inside
// Applicator.Apply (after ledger / identity / fee / metrics / eventbus
// side effects persist), so true indicates the full settlement ran.
//
// Absence of a positive IsApplied signal is RecoveryNotStarted — the
// next Admit for any Settlement event projecting to this key re-drives
// IsComplete / Apply.
func (c *SettlementLogicalKeyConsumer) RecoveryProbe(ctx context.Context, key LogicalKey) (RecoveryStatus, error) {
	if c.settlementApp == nil {
		return RecoveryNotStarted, nil
	}
	if c.settlementApp.IsApplied(event.EventID(key)) {
		return RecoveryCompleted, nil
	}
	_ = ctx
	return RecoveryNotStarted, nil
}

// verdictToSettlementString maps the dispatcher's Outcome.Verdict enum
// to the wire strings the settlement applicator switches on. The
// applicator uses "accepted" / "rejected" (settlement.VerdictAccepted /
// VerdictRejected) — NOT the "pass" / "fail" used by the TVConsensus
// payload. Unknown verdicts return an error so Apply fails loudly
// rather than invoking the applicator with an unroutable verdict.
func verdictToSettlementString(v Verdict) (string, error) {
	switch v {
	case VerdictAccept:
		return string(settlement.VerdictAccepted), nil
	case VerdictReject:
		return string(settlement.VerdictRejected), nil
	}
	return "", fmt.Errorf("unknown Outcome.Verdict %q", v)
}
