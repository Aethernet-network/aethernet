package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ErrBindingMismatch is returned when a ReplaySubmission's binding fields
// (TaskID, Category, PolicyVersion, AcceptanceContractHash) do not match
// the corresponding fields in the referenced ReplayJob.
var ErrBindingMismatch = errors.New("replay: submission binding does not match job")

// ErrMissingRequiredChecks is returned when the ReplaySubmission omits one
// or more checks required by ReplayJob.ChecksToReplay.
var ErrMissingRequiredChecks = errors.New("replay: submission missing required checks")

// SubmissionProcessor handles the full lifecycle of an external replay
// submission: loading the job, validating binding, running the comparison,
// and routing the result through the enforcer.
//
// It is the dedicated path for POST /v1/replay/submit. ReplayRunner remains
// the separate polling/fallback path for InspectionExecutor outcomes.
type SubmissionProcessor struct {
	store       replayStore
	enforcer    *ReplayEnforcer
	taskDetails TaskDetailsProvider // optional; zero values used when nil
	cmpExec     ComparisonExecutor
}

// NewSubmissionProcessor returns a SubmissionProcessor. taskDetails may be nil;
// when nil the enforcer is called with zero agentID/resultHash/title,
// verifiedValue=0, and generationEligible=false.
func NewSubmissionProcessor(
	store replayStore,
	enforcer *ReplayEnforcer,
	taskDetails TaskDetailsProvider,
) *SubmissionProcessor {
	return &SubmissionProcessor{
		store:       store,
		enforcer:    enforcer,
		taskDetails: taskDetails,
	}
}

// Process validates sub, runs the protocol-side comparison against the
// stored ReplayJob, and routes the resulting outcome through the enforcer.
//
// Returns:
//   - (*ReplayOutcome, *ReplayVerdict, nil)     on success (match/mismatch)
//   - (nil, nil, ErrJobNotFound)                when sub.JobID is unknown
//   - (nil, nil, ErrOutcomeAlreadyTerminal)      when the job is already terminal
//   - (nil, nil, ErrBindingMismatch)            when binding fields do not match
//   - (nil, nil, ErrMissingRequiredChecks)      when required checks are absent
//   - (nil, nil, err)                           for persistence / enforcer errors
func (p *SubmissionProcessor) Process(sub *ReplaySubmission) (*ReplayOutcome, *verification.ReplayVerdict, error) {
	// Load the job from the store.
	jobData, err := p.store.GetReplayJob(sub.JobID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: job_id=%s: %v", ErrJobNotFound, sub.JobID, err)
	}
	var job ReplayJob
	if err := json.Unmarshal(jobData, &job); err != nil {
		return nil, nil, fmt.Errorf("replay: unmarshal job: %w", err)
	}

	// Reject submissions for jobs that have already reached a terminal state.
	if job.Status == "completed" || job.Status == "failed" {
		return nil, nil, fmt.Errorf("%w: job_id=%s status=%s", ErrOutcomeAlreadyTerminal, job.ID, job.Status)
	}

	// Validate that the submission's binding fields match the job.
	if err := validateBinding(&job, sub); err != nil {
		return nil, nil, err
	}

	// Run the protocol-side comparison.
	outcome := p.cmpExec.Compare(&job, sub)

	// Reject incomplete submissions (missing required checks) rather than
	// resolving them as benefit-of-the-doubt no_action outcomes. This forces
	// submitters to provide complete results.
	if outcome.Status == "error" {
		return nil, nil, fmt.Errorf("%w: %s", ErrMissingRequiredChecks, outcome.Notes)
	}

	// Look up task details for the enforcer (best-effort; absence is non-fatal).
	var (
		agentID            string
		resultHash         string
		title              string
		verifiedValue      uint64
		generationEligible bool
	)
	if p.taskDetails != nil {
		agentID, resultHash, title, verifiedValue, generationEligible, err =
			p.taskDetails.GetReplayDetails(sub.TaskID)
		if err != nil {
			slog.Warn("submission-processor: task details unavailable",
				"job_id", sub.JobID, "task_id", sub.TaskID, "err", err)
		}
	}

	// Route the outcome through the enforcer to update task state.
	verdict, err := p.enforcer.ProcessReplayOutcome(
		outcome, agentID, resultHash, title, verifiedValue, generationEligible,
	)
	if err != nil {
		return outcome, nil, err
	}

	slog.Info("submission-processor: submission processed",
		"job_id", sub.JobID,
		"task_id", sub.TaskID,
		"outcome_status", outcome.Status,
		"verdict_action", verdict.Action,
		"severity", verdict.SeverityScore,
		"submitter_id", sub.SubmitterID,
	)

	return outcome, verdict, nil
}

// validateBinding checks that the submission's binding fields are consistent
// with the stored job. Returns ErrBindingMismatch on any mismatch.
func validateBinding(job *ReplayJob, sub *ReplaySubmission) error {
	if sub.TaskID != job.TaskID {
		return fmt.Errorf("%w: task_id got=%q want=%q",
			ErrBindingMismatch, sub.TaskID, job.TaskID)
	}
	if sub.Category != job.Category {
		return fmt.Errorf("%w: category got=%q want=%q",
			ErrBindingMismatch, sub.Category, job.Category)
	}

	// Normalize empty PolicyVersion to "v1" before comparing.
	subPV := sub.PolicyVersion
	if subPV == "" {
		subPV = "v1"
	}
	jobPV := job.PolicyVersion
	if jobPV == "" {
		jobPV = "v1"
	}
	if subPV != jobPV {
		return fmt.Errorf("%w: policy_version got=%q want=%q",
			ErrBindingMismatch, subPV, jobPV)
	}

	// AcceptanceContractHash: validate only when both sides are non-empty.
	// Older jobs without the field (empty string) are accepted without this check.
	if job.AcceptanceContractHash != "" && sub.AcceptanceContractHash != "" {
		if sub.AcceptanceContractHash != job.AcceptanceContractHash {
			return fmt.Errorf("%w: acceptance_contract_hash got=%q want=%q",
				ErrBindingMismatch, sub.AcceptanceContractHash, job.AcceptanceContractHash)
		}
	}
	return nil
}
