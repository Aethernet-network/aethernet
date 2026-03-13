package replay

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ReplayRunner polls for pending replay jobs on a fixed interval, executes each
// through a ReplayExecutor, and submits the resulting outcome to a
// ReplayEnforcer. It is safe to start and stop once; subsequent Start calls are
// no-ops.
type ReplayRunner struct {
	coordinator *ReplayCoordinator
	executor    ReplayExecutor
	enforcer    *ReplayEnforcer
	taskDetails TaskDetailsProvider // optional; zero values used when nil
	interval    time.Duration
	stop        chan struct{}
	once        sync.Once
}

// NewReplayRunner returns a ReplayRunner. taskDetails may be nil; when nil the
// enforcer is called with empty agentID/resultHash/title, verifiedValue=0, and
// generationEligible=false.
func NewReplayRunner(
	coordinator *ReplayCoordinator,
	executor ReplayExecutor,
	enforcer *ReplayEnforcer,
	taskDetails TaskDetailsProvider,
	interval time.Duration,
) *ReplayRunner {
	return &ReplayRunner{
		coordinator: coordinator,
		executor:    executor,
		enforcer:    enforcer,
		taskDetails: taskDetails,
		interval:    interval,
		stop:        make(chan struct{}),
	}
}

// Start launches the polling goroutine. Safe to call once.
func (r *ReplayRunner) Start() {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.processOnce()
			case <-r.stop:
				return
			}
		}
	}()
	slog.Info("replay-runner: started", "interval", r.interval)
}

// Stop shuts down the background goroutine. Safe to call multiple times.
func (r *ReplayRunner) Stop() {
	r.once.Do(func() { close(r.stop) })
}

// processOnce loads all pending replay jobs and processes each through the
// executor and enforcer. Errors are logged; a single job failure does not
// prevent other jobs from being processed.
func (r *ReplayRunner) processOnce() {
	jobs, err := r.coordinator.PendingJobs()
	if err != nil {
		slog.Error("replay-runner: failed to load pending jobs", "err", err)
		return
	}
	for _, job := range jobs {
		r.processJob(job)
	}
}

// processJob executes one replay job and submits the outcome to the enforcer.
func (r *ReplayRunner) processJob(job *ReplayJob) {
	ctx := context.Background()
	outcome, err := r.executor.Execute(ctx, job)
	if err != nil {
		slog.Error("replay-runner: executor failed",
			"job_id", job.ID, "task_id", job.TaskID, "err", err)
		return
	}

	// Look up task details for the enforcer. Absence is non-fatal — the
	// enforcer still records the outcome and updates job state; generation
	// credit is withheld (generationEligible=false default) when the task
	// cannot be found.
	var (
		agentID            string
		resultHash         string
		title              string
		verifiedValue      uint64
		generationEligible bool
	)
	if r.taskDetails != nil {
		agentID, resultHash, title, verifiedValue, generationEligible, err =
			r.taskDetails.GetReplayDetails(job.TaskID)
		if err != nil {
			slog.Warn("replay-runner: task details unavailable",
				"job_id", job.ID, "task_id", job.TaskID, "err", err)
		}
	}

	verdict, err := r.enforcer.ProcessReplayOutcome(
		outcome, agentID, resultHash, title, verifiedValue, generationEligible,
	)
	if err != nil {
		slog.Error("replay-runner: enforcer failed",
			"job_id", job.ID, "task_id", job.TaskID, "err", err)
		return
	}

	slog.Info("replay-runner: job processed",
		"job_id", job.ID,
		"task_id", job.TaskID,
		"verdict", verdict.Action,
		"severity", verdict.SeverityScore,
	)
}
