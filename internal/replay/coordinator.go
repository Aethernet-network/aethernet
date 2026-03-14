package replay

import (
	"encoding/json"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// calibrationSource is satisfied by *canary.CanaryManager and any test stub.
// It is defined locally to avoid coupling the coordinator to CanaryManager
// construction details.
type calibrationSource interface {
	CategoryCalibrationForActor(actorID string, category string) (*canary.CategoryCalibration, error)
}

// CalibrationDecision records the calibration-aware scrutiny adjustment made
// during a ShouldReplay call. It is stored in memory (not persisted) for
// inspection and logging.
type CalibrationDecision struct {
	ActorID      string    `json:"actor_id"`
	Category     string    `json:"category"`
	SampleCount  int       `json:"sample_count"`
	Accuracy     float64   `json:"accuracy"`
	AvgSeverity  float64   `json:"avg_severity"`
	BaseRate     float64   `json:"base_rate"`
	AdjustedRate float64   `json:"adjusted_rate"`
	// Reason describes the adjustment applied:
	//   "below_threshold_2x"  — not enough signals; 2× scrutiny
	//   "weak_calibration_3x" — accuracy < 0.6; 3× scrutiny
	//   "strong_calibration_0.5x" — accuracy > 0.9; 0.5× scrutiny
	//   "no_calibration_data" — calibration source returned an error or no data
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// ReplayPolicy controls which tasks the ReplayCoordinator schedules for replay.
type ReplayPolicy struct {
	// SampleRate is the fraction of ordinary tasks randomly sampled for replay.
	// Default: 0.05 (5%).
	SampleRate float64

	// NewAgentSampleRate is the sample rate for agents with fewer than 10
	// completed tasks (probationary period). Default: 0.25 (25%).
	NewAgentSampleRate float64

	// GenerationSampleRate is the sample rate applied to generation-eligible
	// tasks (tasks whose outputs may seed future training). Default: 0.15 (15%).
	GenerationSampleRate float64

	// AlwaysReplayChallenged causes every challenged task to be scheduled for
	// replay regardless of sample rates. Default: true.
	AlwaysReplayChallenged bool

	// AlwaysReplayAnomalies causes every anomaly-flagged task to be scheduled
	// for replay regardless of sample rates. Default: true.
	AlwaysReplayAnomalies bool

	// LowConfidenceThreshold triggers replay when the verifier's confidence
	// score falls below this value. Default: 0.50.
	LowConfidenceThreshold float64

	// SubmissionGracePeriod is the window granted to external replay executors
	// to submit results via POST /v1/replay/submit before InspectionExecutor
	// is allowed to process the job as a fallback.
	//
	// Default: 2 hours — long enough for typical code builds, test suites, or
	// ML inference runs to complete and submit results.
	//
	// Zero disables the grace period: InspectionExecutor processes jobs
	// immediately (legacy / testnet mode).
	SubmissionGracePeriod time.Duration
}

// DefaultReplayPolicy returns a ReplayPolicy with sensible production defaults.
func DefaultReplayPolicy() ReplayPolicy {
	return ReplayPolicy{
		SampleRate:             0.05,
		NewAgentSampleRate:     0.25,
		GenerationSampleRate:   0.15,
		AlwaysReplayChallenged: true,
		AlwaysReplayAnomalies:  true,
		LowConfidenceThreshold: 0.50,
		SubmissionGracePeriod:  2 * time.Hour,
	}
}

// ReplayCoordinator decides which tasks need replay and schedules ReplayJobs
// for them. It does not execute replays — that requires domain-specific logic.
// The coordinator is safe for concurrent use.
type ReplayCoordinator struct {
	policy              ReplayPolicy
	store               replayStore
	mu                  sync.Mutex
	scheduled           map[string]bool      // taskID → already scheduled (dedup guard)
	rng                 *rand.Rand
	calibration         calibrationSource    // optional; nil = no calibration-aware adjustments
	calibrationEnabled  bool                 // gates calibration adjustment; default false (opt-in)
	lastCalibDecision   *CalibrationDecision // most recent calibration decision (in-memory only)
}

// NewReplayCoordinator returns a ReplayCoordinator configured with policy and
// backed by store.
func NewReplayCoordinator(policy ReplayPolicy, store replayStore) *ReplayCoordinator {
	return &ReplayCoordinator{
		policy:    policy,
		store:     store,
		scheduled: make(map[string]bool),
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec
	}
}

// SetCalibrationEnabled toggles calibration-aware scrutiny adjustments.
// When false (the default), ShouldReplay always uses the base SampleRate;
// the calibration source is never queried. Call before the first ShouldReplay
// call. Mirrors SetCalibrationRoutingEnabled on the Router.
func (c *ReplayCoordinator) SetCalibrationEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calibrationEnabled = enabled
}

// SetCalibrationSource wires a calibration source. When set, ShouldReplay
// adjusts the effective sample rate based on the actor's category accuracy:
//   - no data / below MinCalibrationSamples → 2× base rate (more scrutiny)
//   - accuracy < 0.6 → 3× base rate (weak actors)
//   - accuracy > 0.9 → 0.5× base rate (strong actors)
//
// If not called, ShouldReplay behaves identically to its pre-calibration
// behaviour — fully backward compatible.
func (c *ReplayCoordinator) SetCalibrationSource(src calibrationSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calibration = src
}

// LastCalibrationDecision returns the most recent CalibrationDecision computed
// by ShouldReplay, or nil if no calibration-aware call has been made yet.
// The result is in-memory only and is not persisted.
func (c *ReplayCoordinator) LastCalibrationDecision() *CalibrationDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCalibDecision
}

// ShouldReplay determines whether the given task should be scheduled for
// replay. It returns (true, reason) when replay is warranted and (false, "")
// when it is not. Decisions are evaluated in priority order:
//
//	a. Already scheduled for this taskID → (false, "")            [dedup]
//	b. challenged && AlwaysReplayChallenged → (true, "challenged")
//	c. len(anomalyFlags) > 0 && AlwaysReplayAnomalies → (true, "anomaly")
//	d. confidence < LowConfidenceThreshold → (true, "low_confidence")
//	e. agentTaskCount < 10 → sample at NewAgentSampleRate → (true, "probation")
//	f. generationEligible → sample at GenerationSampleRate → (true, "sampled")
//	g. sample at SampleRate → (true, "sampled")
//	h. (false, "")
func (c *ReplayCoordinator) ShouldReplay(
	taskID string,
	agentID string,
	category string,
	confidence float64,
	generationEligible bool,
	challenged bool,
	anomalyFlags []string,
	agentTaskCount int,
) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// a. Dedup: never schedule the same task twice.
	if c.scheduled[taskID] {
		return false, ""
	}

	// b. Challenged tasks are always replayed when the policy enables it.
	if challenged && c.policy.AlwaysReplayChallenged {
		return true, "challenged"
	}

	// c. Anomaly-flagged tasks are always replayed when the policy enables it.
	if len(anomalyFlags) > 0 && c.policy.AlwaysReplayAnomalies {
		return true, "anomaly"
	}

	// d. Low-confidence evidence triggers replay.
	if confidence < c.policy.LowConfidenceThreshold {
		return true, "low_confidence"
	}

	// e. New-agent probationary sampling (elevated rate for agents with < 10 tasks).
	if agentTaskCount < 10 {
		if c.rng.Float64() < c.policy.NewAgentSampleRate {
			return true, "probation"
		}
	}

	// f. Generation-eligible tasks are sampled at an elevated rate because their
	// outputs may seed future model training.
	if generationEligible {
		if c.rng.Float64() < c.policy.GenerationSampleRate {
			return true, "sampled"
		}
	}

	// g. Calibration-aware scrutiny adjustment.
	// Modifies the effective base sample rate before the random roll.
	// Only applies when both a calibration source is configured AND
	// calibrationEnabled is true; records a CalibrationDecision in all cases
	// when source is set (even when disabled, for explainability).
	effectiveRate := c.policy.SampleRate
	if c.calibration != nil {
		effectiveRate = c.applyCalibrationAdjustment(agentID, category, c.policy.SampleRate)
	}

	// h. Baseline random sample of ordinary tasks (calibration-adjusted rate).
	if c.rng.Float64() < effectiveRate {
		return true, "sampled"
	}

	return false, ""
}

// applyCalibrationAdjustment computes the calibration-adjusted sample rate for
// the given actor+category and records a CalibrationDecision. Must be called
// with c.mu held. Never returns a negative rate.
//
// When calibrationEnabled is false, the base rate is returned unchanged and a
// CalibrationDecision with reason "calibration_disabled" is recorded so
// operators can see the flag is off.
//
// Adjustment rules (when enabled):
//   - error or not actionable (nil / < MinCalibrationSamples): 2× base rate
//   - actionable, accuracy < 0.6: 3× base rate (weak actor)
//   - actionable, accuracy > 0.9: 0.5× base rate (strong actor)
//   - actionable, accuracy 0.6–0.9: 1× base rate (no adjustment)
func (c *ReplayCoordinator) applyCalibrationAdjustment(agentID, category string, baseRate float64) float64 {
	decision := CalibrationDecision{
		ActorID:   agentID,
		Category:  category,
		BaseRate:  baseRate,
		Timestamp: time.Now(),
	}

	// When scrutiny is disabled, record why and return the base rate unchanged.
	if !c.calibrationEnabled {
		decision.AdjustedRate = baseRate
		decision.Reason = "calibration_disabled"
		c.lastCalibDecision = &decision
		return baseRate
	}

	cal, err := c.calibration.CategoryCalibrationForActor(agentID, category)

	var multiplier float64
	switch {
	case err != nil:
		multiplier = 2
		decision.Reason = "no_calibration_data"

	case !canary.CalibrationActionable(cal):
		// nil or too few samples — uncertain actor, apply 2× scrutiny.
		multiplier = 2
		if cal != nil {
			decision.SampleCount = cal.TotalSignals
		}
		decision.Reason = "below_threshold_2x"

	case cal.Accuracy < 0.6:
		multiplier = 3
		decision.SampleCount = cal.TotalSignals
		decision.Accuracy = cal.Accuracy
		decision.AvgSeverity = cal.AvgSeverity
		decision.Reason = "weak_calibration_3x"

	case cal.Accuracy > 0.9:
		multiplier = 0.5
		decision.SampleCount = cal.TotalSignals
		decision.Accuracy = cal.Accuracy
		decision.AvgSeverity = cal.AvgSeverity
		decision.Reason = "strong_calibration_0.5x"

	default:
		// Moderate accuracy (0.6–0.9): no adjustment to base rate.
		multiplier = 1
		decision.SampleCount = cal.TotalSignals
		decision.Accuracy = cal.Accuracy
		decision.AvgSeverity = cal.AvgSeverity
		decision.Reason = "calibration_nominal"
	}

	adjustedRate := baseRate * multiplier
	if adjustedRate < 0 {
		adjustedRate = 0
	}
	decision.AdjustedRate = adjustedRate

	slog.Debug("canary: calibration-adjusted replay rate",
		"actor_id", agentID,
		"category", category,
		"accuracy", decision.Accuracy,
		"adjusted_rate", adjustedRate,
		"reason", decision.Reason,
	)

	c.lastCalibDecision = &decision
	return adjustedRate
}

// ScheduleReplay creates a ReplayJob for the given task, persists it to the
// store, and marks the taskID as scheduled to prevent future duplicates.
//
// When policy.SubmissionGracePeriod > 0, the job's SubmissionDeadline is set
// to CreatedAt + SubmissionGracePeriod. The ReplayRunner will not process the
// job via InspectionExecutor until this deadline has passed, giving external
// replay executors the full grace window to submit real results.
func (c *ReplayCoordinator) ScheduleReplay(
	taskID, packetHash, category, policyVersion string,
	reqs *verification.ReplayRequirements,
	reason string,
	agentID string,
) (*ReplayJob, error) {
	job := NewReplayJob(taskID, packetHash, category, policyVersion, agentID, reason, reqs, time.Now())
	if c.policy.SubmissionGracePeriod > 0 {
		job.SubmissionDeadline = job.CreatedAt.Add(c.policy.SubmissionGracePeriod)
	}

	// Copy the most recent calibration decision into the job so operators can
	// inspect why this task was scrutinized at a given rate. Fields are omitempty
	// so jobs created without a calibration source are unaffected.
	c.mu.Lock()
	if c.lastCalibDecision != nil {
		job.CalibrationReason = c.lastCalibDecision.Reason
		job.CalibrationAdjustedRate = c.lastCalibDecision.AdjustedRate
		job.CalibrationSampleCount = c.lastCalibDecision.SampleCount
		job.CalibrationAccuracy = c.lastCalibDecision.Accuracy
	}
	c.mu.Unlock()

	data, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	if err := c.store.PutReplayJob(job.ID, data); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.scheduled[taskID] = true
	c.mu.Unlock()

	return job, nil
}

// PendingJobs returns all ReplayJobs whose Status is "pending" from the store.
func (c *ReplayCoordinator) PendingJobs() ([]*ReplayJob, error) {
	blobs, err := c.store.AllReplayJobs()
	if err != nil {
		return nil, err
	}
	var pending []*ReplayJob
	for _, data := range blobs {
		var job ReplayJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		if job.Status == "pending" {
			pending = append(pending, &job)
		}
	}
	return pending, nil
}
