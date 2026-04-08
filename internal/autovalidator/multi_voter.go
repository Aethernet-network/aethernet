package autovalidator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// eventPublisher is the minimal interface for publishing DAG events.
// Satisfied by *localpub.Publisher.
type eventPublisher interface {
	Publish(ev *event.Event) error
}

// MultiVoter scores submissions using all configured analyzers and emits
// one TaskVerificationVote event per analyzer family. Each family's vote
// is an independent signal that carries the analyzer's verdict, score, and
// artifact hash.
type MultiVoter struct {
	analyzers   []verification.Analyzer
	rounds      taskverification.Store
	publisher   eventPublisher
	kp          *crypto.KeyPair
	validatorID crypto.AgentID
	clock       func() int64
	// analyzerTimeout is the per-analyzer context timeout.
	analyzerTimeout time.Duration
}

// NewMultiVoter creates a MultiVoter with the given analyzer set.
func NewMultiVoter(
	analyzers []verification.Analyzer,
	rounds taskverification.Store,
	publisher eventPublisher,
	kp *crypto.KeyPair,
	validatorID crypto.AgentID,
	clock func() int64,
) *MultiVoter {
	return &MultiVoter{
		analyzers:       analyzers,
		rounds:          rounds,
		publisher:       publisher,
		kp:              kp,
		validatorID:     validatorID,
		clock:           clock,
		analyzerTimeout: 30 * time.Second,
	}
}

// ScoreAndVoteResult captures the outcome of running all analyzers.
type ScoreAndVoteResult struct {
	AnalyzersRun    int
	VotesEmitted    int
	VotesSkipped    int // already voted by this validator for this family
	AnalyzersFailed int
	PerFamily       map[verification.FamilyID]FamilyVoteResult
}

// FamilyVoteResult captures the outcome for a single family.
type FamilyVoteResult struct {
	Family               verification.FamilyID
	AnalyzerID           verification.AnalyzerID
	Verdict              string
	ScoreBP              uint64
	ScoreBreakdown       map[string]uint64
	AnalysisArtifactHash string
	Skipped              bool
	Error                error
}

// ScoreAndVote runs all configured analyzers and emits one vote per family.
// Idempotent: skips families where this validator has already voted.
func (m *MultiVoter) ScoreAndVote(
	ctx context.Context,
	round *taskverification.TaskVerificationRound,
	input verification.AnalysisInput,
) (ScoreAndVoteResult, error) {
	result := ScoreAndVoteResult{
		PerFamily: make(map[verification.FamilyID]FamilyVoteResult),
	}

	if round.State != taskverification.RoundStateOpen {
		slog.Debug("multi_voter: round not open, skipping",
			"round_id", round.RoundID, "state", round.State)
		return result, nil
	}

	for _, analyzer := range m.analyzers {
		family := analyzer.Family()

		// Check if this validator already voted for this family.
		alreadyVoted := false
		for _, v := range round.Votes {
			if v.ValidatorID == m.validatorID && string(v.AnalyzerFamily) == string(family) {
				alreadyVoted = true
				break
			}
		}
		if alreadyVoted {
			result.VotesSkipped++
			result.PerFamily[family] = FamilyVoteResult{
				Family:     family,
				AnalyzerID: analyzer.ID(),
				Skipped:    true,
			}
			continue
		}

		// Run the analyzer with timeout.
		result.AnalyzersRun++
		analyzerCtx, cancel := context.WithTimeout(ctx, m.analyzerTimeout)
		output, err := analyzer.Analyze(analyzerCtx, input)
		cancel()

		if err != nil {
			result.AnalyzersFailed++
			result.PerFamily[family] = FamilyVoteResult{
				Family:     family,
				AnalyzerID: analyzer.ID(),
				Error:      err,
			}
			slog.Warn("multi_voter: analyzer failed",
				"analyzer", analyzer.ID(), "family", family,
				"task_id", round.TaskID, "err", err)
			continue
		}

		// Build and emit the vote event.
		payload := event.TaskVerificationVotePayload{
			Version:              1,
			RoundID:              string(round.RoundID),
			TaskID:               round.TaskID,
			SubmissionEventID:    string(round.SubmissionEventID),
			ValidatorID:          string(m.validatorID),
			Verdict:              output.Verdict,
			ScoreBP:              output.ScoreBP,
			ScoreBreakdown:       output.ScoreBreakdown,
			AnalyzerFamily:       string(family),
			AnalyzerVersion:      output.Version,
			PolicyVersion:        "bootstrap_v1",
			AnalysisArtifactHash: output.ArtifactHash,
			TimestampUnix:        m.clock(),
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			result.AnalyzersFailed++
			result.PerFamily[family] = FamilyVoteResult{
				Family: family, AnalyzerID: analyzer.ID(),
				Error: fmt.Errorf("marshal payload: %w", err),
			}
			continue
		}

		// Semantic parent: the TaskSubmitted event being voted on.
		refs := []event.EventID{round.SubmissionEventID}
		priorTS := map[event.EventID]uint64{}
		voteEvent, err := event.New(
			event.EventTypeTaskVerificationVote,
			refs,
			json.RawMessage(payloadBytes),
			string(m.validatorID),
			priorTS,
			0,
		)
		if err != nil {
			result.AnalyzersFailed++
			result.PerFamily[family] = FamilyVoteResult{
				Family: family, AnalyzerID: analyzer.ID(),
				Error: fmt.Errorf("create event: %w", err),
			}
			continue
		}

		if err := crypto.SignEvent(voteEvent, m.kp); err != nil {
			result.AnalyzersFailed++
			result.PerFamily[family] = FamilyVoteResult{
				Family: family, AnalyzerID: analyzer.ID(),
				Error: fmt.Errorf("sign event: %w", err),
			}
			continue
		}

		if m.publisher != nil {
			if err := m.publisher.Publish(voteEvent); err != nil {
				slog.Warn("multi_voter: publish failed",
					"analyzer", analyzer.ID(), "family", family,
					"task_id", round.TaskID, "err", err)
				result.AnalyzersFailed++
				result.PerFamily[family] = FamilyVoteResult{
					Family: family, AnalyzerID: analyzer.ID(),
					Error: fmt.Errorf("publish: %w", err),
				}
				continue
			}
		}

		result.VotesEmitted++
		result.PerFamily[family] = FamilyVoteResult{
			Family:               family,
			AnalyzerID:           analyzer.ID(),
			Verdict:              output.Verdict,
			ScoreBP:              output.ScoreBP,
			ScoreBreakdown:       output.ScoreBreakdown,
			AnalysisArtifactHash: output.ArtifactHash,
		}

		slog.Info("multi_voter: vote emitted",
			"task_id", round.TaskID,
			"round_id", round.RoundID,
			"family", family,
			"analyzer", analyzer.ID(),
			"verdict", output.Verdict,
			"score_bp", output.ScoreBP,
			"artifact_hash", output.ArtifactHash,
		)
	}

	return result, nil
}
