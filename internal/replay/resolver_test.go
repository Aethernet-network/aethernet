package replay

import (
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeOutcome(status string, comparisons []CheckComparison, anomalyFlags []string) *ReplayOutcome {
	return &ReplayOutcome{
		JobID:        "job-ev-1",
		TaskID:       "task-ev-1",
		Status:       status,
		Comparisons:  comparisons,
		AnomalyFlags: anomalyFlags,
		ReplayedAt:   time.Now(),
		ReplayerID:   "replayer-test",
	}
}

// ---------------------------------------------------------------------------
// EvaluateOutcome tests
// ---------------------------------------------------------------------------

func TestEvaluateOutcome_CleanMatch_NoAction(t *testing.T) {
	res := NewReplayResolver(newMemStore())
	outcome := makeOutcome("match", []CheckComparison{
		{CheckType: "go_test", Match: true},
		{CheckType: "lint", Match: true},
	}, nil)

	verdict := res.EvaluateOutcome(outcome)

	if verdict.Action != "no_action" {
		t.Errorf("Action: want %q, got %q", "no_action", verdict.Action)
	}
	if verdict.SeverityScore != 0.0 {
		t.Errorf("SeverityScore: want 0.0, got %v", verdict.SeverityScore)
	}
	if verdict.MismatchCount != 0 {
		t.Errorf("MismatchCount: want 0, got %d", verdict.MismatchCount)
	}
}

func TestEvaluateOutcome_SingleSmallMismatch_FlagForReview(t *testing.T) {
	res := NewReplayResolver(newMemStore())
	outcome := makeOutcome("partial_match", []CheckComparison{
		{CheckType: "go_test", Match: true},
		{CheckType: "lint", Match: false, ScoreDelta: 0.08}, // delta < 0.15
	}, nil)

	verdict := res.EvaluateOutcome(outcome)

	if verdict.Action != "flag_for_review" {
		t.Errorf("Action: want %q, got %q", "flag_for_review", verdict.Action)
	}
	if verdict.SeverityScore != 0.3 {
		t.Errorf("SeverityScore: want 0.3, got %v", verdict.SeverityScore)
	}
	if verdict.MismatchCount != 1 {
		t.Errorf("MismatchCount: want 1, got %d", verdict.MismatchCount)
	}
}

func TestEvaluateOutcome_MultiplesMismatches_OpenChallenge(t *testing.T) {
	res := NewReplayResolver(newMemStore())
	outcome := makeOutcome("partial_match", []CheckComparison{
		{CheckType: "go_test", Match: false, ScoreDelta: 0.10},
		{CheckType: "lint", Match: false, ScoreDelta: 0.12},
	}, nil)

	verdict := res.EvaluateOutcome(outcome)

	if verdict.Action != "open_challenge" {
		t.Errorf("Action: want %q, got %q", "open_challenge", verdict.Action)
	}
	if verdict.SeverityScore != 0.6 {
		t.Errorf("SeverityScore: want 0.6, got %v", verdict.SeverityScore)
	}
	if verdict.MismatchCount != 2 {
		t.Errorf("MismatchCount: want 2, got %d", verdict.MismatchCount)
	}
}

func TestEvaluateOutcome_SevereMismatchWithAnomalyFlags_SlashRecommended(t *testing.T) {
	res := NewReplayResolver(newMemStore())
	// Status "mismatch" → severity 0.8; one anomaly flag → 0.8+0.1=0.9 → slash_recommended.
	outcome := makeOutcome("mismatch", []CheckComparison{
		{CheckType: "go_test", Match: false},
		{CheckType: "lint", Match: false},
	}, []string{"hash_divergence"})

	verdict := res.EvaluateOutcome(outcome)

	if verdict.Action != "slash_recommended" {
		t.Errorf("Action: want %q, got %q", "slash_recommended", verdict.Action)
	}
	if verdict.SeverityScore < 0.9 {
		t.Errorf("SeverityScore: want >= 0.9, got %v", verdict.SeverityScore)
	}
	if len(verdict.AnomalyFlags) != 1 {
		t.Errorf("AnomalyFlags: want 1, got %d", len(verdict.AnomalyFlags))
	}
}

// ---------------------------------------------------------------------------
// RecordOutcome — persists outcome and updates job status
// ---------------------------------------------------------------------------

func TestRecordOutcome_PersistsAndUpdatesJobStatus(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)

	// Create and store a ReplayJob so RecordOutcome can find it.
	job := NewReplayJob("task-ro", "sha256:pkt", "code", "v1", "agent-1", "audit", fullReqs(), time.Now())
	jobData, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal job: %v", err)
	}
	if err := ms.PutReplayJob(job.ID, jobData); err != nil {
		t.Fatalf("PutReplayJob: %v", err)
	}

	outcome := &ReplayOutcome{
		JobID:      job.ID,
		TaskID:     "task-ro",
		Status:     "match",
		ReplayedAt: time.Now(),
		ReplayerID: "replayer-1",
	}
	if err := resolver.RecordOutcome(outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}

	// Verify the outcome was persisted.
	outcomeData, err := ms.GetReplayOutcome(outcome.JobID)
	if err != nil {
		t.Fatalf("GetReplayOutcome: %v", err)
	}
	var persisted ReplayOutcome
	if err := json.Unmarshal(outcomeData, &persisted); err != nil {
		t.Fatalf("Unmarshal outcome: %v", err)
	}
	if persisted.TaskID != "task-ro" {
		t.Errorf("persisted TaskID: want %q, got %q", "task-ro", persisted.TaskID)
	}

	// Verify the job status was updated to "completed".
	updatedJobData, err := ms.GetReplayJob(job.ID)
	if err != nil {
		t.Fatalf("GetReplayJob after RecordOutcome: %v", err)
	}
	var updatedJob ReplayJob
	if err := json.Unmarshal(updatedJobData, &updatedJob); err != nil {
		t.Fatalf("Unmarshal updated job: %v", err)
	}
	if updatedJob.Status != "completed" {
		t.Errorf("job Status: want %q, got %q", "completed", updatedJob.Status)
	}
}

func TestRecordOutcome_ErrorOutcomeSetsJobFailed(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)

	job := NewReplayJob("task-err", "sha256:pkt", "code", "v1", "agent-1", "audit", nil, time.Now())
	jobData, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, jobData)

	outcome := &ReplayOutcome{
		JobID:  job.ID,
		TaskID: "task-err",
		Status: "error",
		Notes:  "executor crashed",
	}
	if err := resolver.RecordOutcome(outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}

	updatedJobData, err := ms.GetReplayJob(job.ID)
	if err != nil {
		t.Fatalf("GetReplayJob: %v", err)
	}
	var updatedJob ReplayJob
	if err := json.Unmarshal(updatedJobData, &updatedJob); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if updatedJob.Status != "failed" {
		t.Errorf("job Status: want %q, got %q", "failed", updatedJob.Status)
	}
}

// ---------------------------------------------------------------------------
// OutcomeToReputationSignal tests
// ---------------------------------------------------------------------------

func TestOutcomeToReputationSignal_Match(t *testing.T) {
	outcome := &ReplayOutcome{
		TaskID:      "task-sig-1",
		Status:      "match",
		Comparisons: []CheckComparison{{CheckType: "go_test", Match: true}},
	}
	sig := OutcomeToReputationSignal(outcome, "agent-1", "code")

	if sig.SignalType != "replay_match" {
		t.Errorf("SignalType: want %q, got %q", "replay_match", sig.SignalType)
	}
	if sig.SeverityScore != 0.0 {
		t.Errorf("SeverityScore: want 0.0, got %v", sig.SeverityScore)
	}
	if sig.AgentID != "agent-1" {
		t.Errorf("AgentID: want %q, got %q", "agent-1", sig.AgentID)
	}
	if sig.Category != "code" {
		t.Errorf("Category: want %q, got %q", "code", sig.Category)
	}
}

func TestOutcomeToReputationSignal_Mismatch(t *testing.T) {
	outcome := &ReplayOutcome{
		TaskID: "task-sig-2",
		Status: "partial_match",
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: false},
		},
	}
	sig := OutcomeToReputationSignal(outcome, "agent-2", "code")

	if sig.SignalType != "replay_mismatch" {
		t.Errorf("SignalType: want %q, got %q", "replay_mismatch", sig.SignalType)
	}
	if sig.SeverityScore == 0.0 {
		t.Error("SeverityScore: want > 0.0 for mismatch")
	}
}

func TestOutcomeToReputationSignal_Anomaly(t *testing.T) {
	outcome := &ReplayOutcome{
		TaskID:       "task-sig-3",
		Status:       "mismatch",
		AnomalyFlags: []string{"timing_anomaly", "env_drift"},
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: false},
		},
	}
	sig := OutcomeToReputationSignal(outcome, "agent-3", "code")

	if sig.SignalType != "replay_anomaly" {
		t.Errorf("SignalType: want %q, got %q", "replay_anomaly", sig.SignalType)
	}
	if sig.SeverityScore <= 0.7 {
		t.Errorf("SeverityScore: want > 0.7 for anomaly with flags, got %v", sig.SeverityScore)
	}
}
