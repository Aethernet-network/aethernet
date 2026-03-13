package replay

import (
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

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
	policy    ReplayPolicy
	store     replayStore
	mu        sync.Mutex
	scheduled map[string]bool // taskID → already scheduled (dedup guard)
	rng       *rand.Rand
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
	_ string, // agentID — reserved for future policy extensions
	_ string, // category — reserved for per-category policy
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

	// g. Baseline random sample of ordinary tasks.
	if c.rng.Float64() < c.policy.SampleRate {
		return true, "sampled"
	}

	return false, ""
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
