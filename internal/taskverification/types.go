package taskverification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// DefaultAcceptanceThresholdBP is the minimum score (in basis points)
	// for a round to finalize as accepted. 6000 = 0.60.
	DefaultAcceptanceThresholdBP = 6000

	// DefaultRoundDeadlineSeconds is the initial deadline for a verification
	// round before extension.
	DefaultRoundDeadlineSeconds = 60

	// DefaultRoundExtensionSeconds is the additional time granted when a
	// round qualifies for deadline extension.
	DefaultRoundExtensionSeconds = 60

	// DefaultDiversityFloor is the minimum number of distinct analyzer
	// families that must contribute pass-weight for acceptance.
	DefaultDiversityFloor = 2
)

// ---------------------------------------------------------------------------
// RoundID
// ---------------------------------------------------------------------------

// RoundID uniquely identifies a TaskVerificationRound. It is derived
// deterministically from the TaskSubmitted event ID so that every node
// produces the same round ID for the same submission.
type RoundID string

// NewRoundID derives a deterministic round ID from a submission event ID.
// The derivation is SHA-256("tvr:" + eventID), hex-encoded. The "tvr:"
// prefix prevents collisions with raw event IDs.
func NewRoundID(submissionEventID event.EventID) RoundID {
	h := sha256.Sum256([]byte("tvr:" + string(submissionEventID)))
	return RoundID(hex.EncodeToString(h[:]))
}

// ---------------------------------------------------------------------------
// RoundState
// ---------------------------------------------------------------------------

// RoundState tracks a verification round through its lifecycle.
type RoundState int

const (
	// RoundStateOpen is the initial state. Votes are being collected.
	RoundStateOpen RoundState = 0

	// RoundStateFinalizedAccept means BFT supermajority voted pass with
	// sufficient analyzer-family diversity.
	RoundStateFinalizedAccept RoundState = 1

	// RoundStateFinalizedReject means BFT supermajority voted fail.
	RoundStateFinalizedReject RoundState = 2

	// RoundStateDisputed means the deadline expired without supermajority.
	// Settlement applies the disputed resolution (50/50 escrow split).
	RoundStateDisputed RoundState = 3

	// RoundStateExpired means the round expired without resolution and
	// without qualifying for disputed status.
	RoundStateExpired RoundState = 4
)

var roundStateNames = map[RoundState]string{
	RoundStateOpen:            "open",
	RoundStateFinalizedAccept: "finalized_accept",
	RoundStateFinalizedReject: "finalized_reject",
	RoundStateDisputed:        "disputed",
	RoundStateExpired:         "expired",
}

var roundStateFromName = map[string]RoundState{
	"open":              RoundStateOpen,
	"finalized_accept":  RoundStateFinalizedAccept,
	"finalized_reject":  RoundStateFinalizedReject,
	"disputed":          RoundStateDisputed,
	"expired":           RoundStateExpired,
}

// String returns the canonical string representation.
func (s RoundState) String() string {
	if name, ok := roundStateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("RoundState(%d)", int(s))
}

// Valid returns true if s is a defined RoundState value.
func (s RoundState) Valid() bool {
	_, ok := roundStateNames[s]
	return ok
}

// MarshalJSON serializes RoundState as a JSON string.
func (s RoundState) MarshalJSON() ([]byte, error) {
	name, ok := roundStateNames[s]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrInvalidRoundState, int(s))
	}
	return json.Marshal(name)
}

// UnmarshalJSON deserializes RoundState from a JSON string.
func (s *RoundState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	val, ok := roundStateFromName[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidRoundState, name)
	}
	*s = val
	return nil
}

// ---------------------------------------------------------------------------
// Verdict
// ---------------------------------------------------------------------------

// Verdict is a validator's vote on a task submission.
type Verdict int

const (
	// VerdictPass means the validator approves the submission.
	VerdictPass Verdict = 0

	// VerdictFail means the validator rejects the submission.
	VerdictFail Verdict = 1

	// VerdictAbstain means the validator declines to vote.
	VerdictAbstain Verdict = 2
)

var verdictNames = map[Verdict]string{
	VerdictPass:    "pass",
	VerdictFail:    "fail",
	VerdictAbstain: "abstain",
}

var verdictFromName = map[string]Verdict{
	"pass":    VerdictPass,
	"fail":    VerdictFail,
	"abstain": VerdictAbstain,
}

// String returns the canonical string representation.
func (v Verdict) String() string {
	if name, ok := verdictNames[v]; ok {
		return name
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// Valid returns true if v is a defined Verdict value.
func (v Verdict) Valid() bool {
	_, ok := verdictNames[v]
	return ok
}

// MarshalJSON serializes Verdict as a JSON string.
func (v Verdict) MarshalJSON() ([]byte, error) {
	name, ok := verdictNames[v]
	if !ok {
		return nil, fmt.Errorf("taskverification: invalid verdict: %d", int(v))
	}
	return json.Marshal(name)
}

// UnmarshalJSON deserializes Verdict from a JSON string.
func (v *Verdict) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	val, ok := verdictFromName[name]
	if !ok {
		return fmt.Errorf("taskverification: unknown verdict: %q", name)
	}
	*v = val
	return nil
}
