package taskverification

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/jcs"
)

// ---------------------------------------------------------------------------
// TaskVerificationRound
// ---------------------------------------------------------------------------

// TaskVerificationRound tracks the lifecycle of a multi-validator verification
// of a single task submission. Each round is opened when a TaskSubmitted event
// is recognized, collects votes from validators, and finalizes via BFT
// consensus rules including analyzer-family diversity requirements.
type TaskVerificationRound struct {
	RoundID               RoundID        `json:"round_id"`
	TaskID                string         `json:"task_id"`
	SubmissionEventID     event.EventID  `json:"submission_event_id"`
	WorkerID              crypto.AgentID `json:"worker_id"`
	PosterID              crypto.AgentID `json:"poster_id"`
	Category              string         `json:"category"`
	ValidatorSetVersion   uint64         `json:"validator_set_version"`
	Committee             []crypto.AgentID `json:"committee,omitempty"` // nil = all active (bootstrap mode)
	AnalyzerPolicyID      string         `json:"analyzer_policy_id"`
	DiversityFloor        int            `json:"diversity_floor"`
	AcceptanceThresholdBP uint64         `json:"acceptance_threshold_bp"`
	OpenedAtUnix          int64          `json:"opened_at_unix"`
	DeadlineUnix          int64          `json:"deadline_unix"`
	ExtendedUntilUnix     int64          `json:"extended_until_unix,omitempty"` // 0 if not extended
	State                 RoundState     `json:"state"`

	// Aggregation state
	PassWeight            uint64            `json:"pass_weight"`
	FailWeight            uint64            `json:"fail_weight"`
	AbstainWeight         uint64            `json:"abstain_weight"`
	ParticipatingFamilies    map[string]uint64 `json:"participating_families,omitempty"`     // family_id → accumulated pass-weight
	AllParticipatingFamilies map[string]bool   `json:"all_participating_families,omitempty"` // family_id → true if any vote (pass/fail/abstain) received

	// Vote records (for audit and finalization)
	Votes []TaskVerificationVoteRecord `json:"votes,omitempty"`

	// Final outcome (set on finalization)
	FinalVerdict      Verdict `json:"final_verdict"`
	FinalScoreBP      uint64  `json:"final_score_bp"`
	FinalizationTime  int64   `json:"finalization_time,omitempty"`

	// CalibrationApplied is set to true after the recognition-fabric
	// consensus consumer applies per-family calibration increments for
	// this round. Guards against double-apply on consensus-event replay.
	// Per step-2 plan §D2: name the semantic state (CalibrationApplied),
	// not the writer mechanism (Increment). Field is JSON-optional so
	// rounds persisted before step 2 deserialize with zero-value false,
	// which is correct — the first replay catches up and sets the flag.
	CalibrationApplied bool `json:"calibration_applied,omitempty"`
}

// TaskVerificationVoteRecord captures a single validator's vote within a round.
type TaskVerificationVoteRecord struct {
	ValidatorID          crypto.AgentID    `json:"validator_id"`
	Verdict              Verdict           `json:"verdict"`
	ScoreBP              uint64            `json:"score_bp"`
	ScoreBreakdown       map[string]uint64 `json:"score_breakdown,omitempty"`
	AnalyzerFamily       string            `json:"analyzer_family"`
	AnalyzerVersion      string            `json:"analyzer_version"`
	PolicyVersion        string            `json:"policy_version"`
	AnalysisArtifactHash string            `json:"analysis_artifact_hash,omitempty"`
	Stake                uint64            `json:"stake"`
	TimestampUnix        int64             `json:"timestamp_unix"`
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// OpenRoundParams contains the inputs for opening a new verification round.
type OpenRoundParams struct {
	TaskID                string
	SubmissionEventID     event.EventID
	WorkerID              crypto.AgentID
	PosterID              crypto.AgentID
	Category              string
	ValidatorSetVersion   uint64
	Committee             []crypto.AgentID // nil for bootstrap mode (all active validators)
	AnalyzerPolicyID      string
	DiversityFloor        int
	AcceptanceThresholdBP uint64
	DeadlineSeconds       int64
	Now                   int64 // injected for determinism in tests
}

// OpenRound creates a new TaskVerificationRound in Open state. The round ID
// is deterministically derived from the submission event ID.
func OpenRound(p OpenRoundParams) (*TaskVerificationRound, error) {
	if p.TaskID == "" {
		return nil, fmt.Errorf("%w: TaskID is required", ErrInvalidRoundID)
	}
	if p.SubmissionEventID == "" {
		return nil, fmt.Errorf("%w: SubmissionEventID is required", ErrInvalidRoundID)
	}
	if p.WorkerID == "" {
		return nil, fmt.Errorf("%w: WorkerID is required", ErrInvalidRoundID)
	}
	if p.PosterID == "" {
		return nil, fmt.Errorf("%w: PosterID is required", ErrInvalidRoundID)
	}
	if p.Category == "" {
		return nil, fmt.Errorf("%w: Category is required", ErrInvalidRoundID)
	}
	if p.DeadlineSeconds <= 0 {
		return nil, fmt.Errorf("%w: DeadlineSeconds must be > 0", ErrInvalidDeadline)
	}
	if p.DiversityFloor < 1 {
		return nil, fmt.Errorf("%w: DiversityFloor must be >= 1", ErrInvalidDeadline)
	}
	if p.AcceptanceThresholdBP > 10000 {
		return nil, fmt.Errorf("%w: AcceptanceThresholdBP must be in [0, 10000]", ErrInvalidDeadline)
	}

	return &TaskVerificationRound{
		RoundID:               NewRoundID(p.SubmissionEventID),
		TaskID:                p.TaskID,
		SubmissionEventID:     p.SubmissionEventID,
		WorkerID:              p.WorkerID,
		PosterID:              p.PosterID,
		Category:              p.Category,
		ValidatorSetVersion:   p.ValidatorSetVersion,
		Committee:             p.Committee,
		AnalyzerPolicyID:      p.AnalyzerPolicyID,
		DiversityFloor:        p.DiversityFloor,
		AcceptanceThresholdBP: p.AcceptanceThresholdBP,
		OpenedAtUnix:          p.Now,
		DeadlineUnix:          p.Now + p.DeadlineSeconds,
		ExtendedUntilUnix:     0,
		State:                 RoundStateOpen,
		ParticipatingFamilies:    make(map[string]uint64),
		AllParticipatingFamilies: make(map[string]bool),
		Votes:                   []TaskVerificationVoteRecord{},
	}, nil
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// validTransitions defines the allowed state transitions. All transitions
// originate from Open; all other states are terminal.
var validTransitions = map[RoundState][]RoundState{
	RoundStateOpen:            {RoundStateFinalizedAccept, RoundStateFinalizedReject, RoundStateDisputed, RoundStateExpired},
	RoundStateFinalizedAccept: {},
	RoundStateFinalizedReject: {},
	RoundStateDisputed:        {},
	RoundStateExpired:         {},
}

// CanTransitionTo returns true if the round can move from its current state
// to the given next state.
func (r *TaskVerificationRound) CanTransitionTo(next RoundState) bool {
	allowed, ok := validTransitions[r.State]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

// Transition moves the round to the given state. Returns
// ErrInvalidStateTransition if the transition is not permitted.
// now is the Unix timestamp to record as finalization time for terminal states.
func (r *TaskVerificationRound) Transition(next RoundState, now int64) error {
	if !r.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidStateTransition, r.State, next)
	}
	r.State = next
	if next != RoundStateOpen {
		r.FinalizationTime = now
	}
	return nil
}

// IsTerminal returns true if the round is in a terminal state (no further
// transitions possible).
func (r *TaskVerificationRound) IsTerminal() bool {
	return r.State != RoundStateOpen
}

// DeadlineForCurrentPhase returns the effective deadline. If the round has
// been extended, returns ExtendedUntilUnix; otherwise returns DeadlineUnix.
func (r *TaskVerificationRound) DeadlineForCurrentPhase() int64 {
	if r.ExtendedUntilUnix > 0 {
		return r.ExtendedUntilUnix
	}
	return r.DeadlineUnix
}

// DistinctPassFamilies returns the number of distinct analyzer families
// that have contributed pass-weight to this round.
func (r *TaskVerificationRound) DistinctPassFamilies() int {
	count := 0
	for _, w := range r.ParticipatingFamilies {
		if w > 0 {
			count++
		}
	}
	return count
}

// DistinctParticipatingFamilies returns the number of distinct analyzer
// families that have contributed any vote (pass, fail, or abstain) to this
// round. Used for the participation floor check.
//
// Computes from the Votes slice as the authoritative source, since the
// AllParticipatingFamilies map may lose entries under concurrent vote
// processing (read-then-write race on the round store).
func (r *TaskVerificationRound) DistinctParticipatingFamilies() int {
	families := make(map[string]bool)
	for _, v := range r.Votes {
		if v.AnalyzerFamily != "" {
			families[v.AnalyzerFamily] = true
		}
	}
	return len(families)
}

// ---------------------------------------------------------------------------
// Canonical serialization
// ---------------------------------------------------------------------------

// canonicalFamilyEntry is a sorted representation of a map[string]uint64
// entry, used for deterministic serialization.
type canonicalFamilyEntry struct {
	Key   string `json:"k"`
	Value uint64 `json:"v"`
}

// Canonical returns a deterministic byte representation of the round using
// JCS (RFC 8785) canonicalization. Map fields are sorted by key before
// serialization. Vote records are sorted by ValidatorID.
func (r *TaskVerificationRound) Canonical() ([]byte, error) {
	// Sort participating families by key for deterministic output.
	families := make([]canonicalFamilyEntry, 0, len(r.ParticipatingFamilies))
	for k, v := range r.ParticipatingFamilies {
		families = append(families, canonicalFamilyEntry{Key: k, Value: v})
	}
	sort.Slice(families, func(i, j int) bool { return families[i].Key < families[j].Key })

	// Sort votes by ValidatorID for deterministic output.
	sortedVotes := make([]TaskVerificationVoteRecord, len(r.Votes))
	copy(sortedVotes, r.Votes)
	sort.Slice(sortedVotes, func(i, j int) bool {
		return sortedVotes[i].ValidatorID < sortedVotes[j].ValidatorID
	})

	// Sort each vote's ScoreBreakdown by key.
	for i := range sortedVotes {
		if len(sortedVotes[i].ScoreBreakdown) > 0 {
			sorted := make([]canonicalFamilyEntry, 0, len(sortedVotes[i].ScoreBreakdown))
			for k, v := range sortedVotes[i].ScoreBreakdown {
				sorted = append(sorted, canonicalFamilyEntry{Key: k, Value: v})
			}
			sort.Slice(sorted, func(a, b int) bool { return sorted[a].Key < sorted[b].Key })
			rebuilt := make(map[string]uint64, len(sorted))
			for _, e := range sorted {
				rebuilt[e.Key] = e.Value
			}
			sortedVotes[i].ScoreBreakdown = rebuilt
		}
	}

	// Build canonical projection — uses the same struct but with sorted data.
	proj := TaskVerificationRound{
		RoundID:               r.RoundID,
		TaskID:                r.TaskID,
		SubmissionEventID:     r.SubmissionEventID,
		WorkerID:              r.WorkerID,
		PosterID:              r.PosterID,
		Category:              r.Category,
		ValidatorSetVersion:   r.ValidatorSetVersion,
		Committee:             r.Committee,
		AnalyzerPolicyID:      r.AnalyzerPolicyID,
		DiversityFloor:        r.DiversityFloor,
		AcceptanceThresholdBP: r.AcceptanceThresholdBP,
		OpenedAtUnix:          r.OpenedAtUnix,
		DeadlineUnix:          r.DeadlineUnix,
		ExtendedUntilUnix:     r.ExtendedUntilUnix,
		State:                 r.State,
		PassWeight:            r.PassWeight,
		FailWeight:            r.FailWeight,
		AbstainWeight:         r.AbstainWeight,
		ParticipatingFamilies: r.ParticipatingFamilies,
		Votes:                 sortedVotes,
		FinalVerdict:          r.FinalVerdict,
		FinalScoreBP:          r.FinalScoreBP,
		FinalizationTime:      r.FinalizationTime,
		CalibrationApplied:    r.CalibrationApplied,
	}

	data, err := json.Marshal(proj)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrSerializationFailed, err)
	}
	canonical, err := jcs.Canonicalize(data)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %v", ErrSerializationFailed, err)
	}
	return canonical, nil
}

// RoundFromCanonical deserializes a TaskVerificationRound from canonical
// (or any valid JSON) bytes.
func RoundFromCanonical(data []byte) (*TaskVerificationRound, error) {
	var r TaskVerificationRound
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %v", ErrSerializationFailed, err)
	}
	return &r, nil
}
