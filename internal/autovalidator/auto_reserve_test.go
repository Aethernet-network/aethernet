package autovalidator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/config"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// Stub replay reserve
// ---------------------------------------------------------------------------

// capturingReserve records every Accrue call made by the autovalidator so
// tests can assert on the category and amount without a real store.
type capturingReserve struct {
	mu      sync.Mutex
	entries []reserveEntry
}

type reserveEntry struct {
	category string
	amount   uint64
}

func (r *capturingReserve) Accrue(category string, amount uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, reserveEntry{category: category, amount: amount})
	return nil
}

func (r *capturingReserve) all() []reserveEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]reserveEntry, len(r.entries))
	copy(cp, r.entries)
	return cp
}

// ---------------------------------------------------------------------------
// Always-pass verification service stub
// ---------------------------------------------------------------------------

// alwaysPassService is a VerificationService that always returns a passing
// result with a fixed high score. Used in reserve-accrual tests where the
// goal is to exercise the accrual wiring, not the evidence scoring logic.
type alwaysPassService struct{}

func (s *alwaysPassService) Verify(_ context.Context, req verification.VerificationRequest) (*verification.VerificationResult, error) {
	return &verification.VerificationResult{
		TaskID: req.TaskID,
		DeterministicReport: verification.DeterministicReport{
			HardGates: []verification.GateResult{{Name: "threshold", Pass: true}},
			NumericScores: map[string]float64{
				"relevance":    0.80,
				"completeness": 0.80,
				"quality":      0.80,
				"overall":      0.80,
			},
		},
		SubjectiveReport: verification.SubjectiveReport{
			Relevance:    0.80,
			Completeness: 0.80,
			Quality:      0.80,
			Overall:      0.80,
		},
		Confidence:    0.80,
		PolicyVersion: "v1",
		VerifierID:    "always-pass-stub",
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildAssuredTask creates a TaskManager with an AssuranceConfig and posts a
// standard-lane task. Returns the task + budget + manager + escrow.
func buildAssuredTask(t *testing.T) (*tasks.TaskManager, *escrow.Escrow, *tasks.Task, uint64) {
	t.Helper()
	d := config.DefaultConfig()
	tm := tasks.NewTaskManager()
	tm.SetAssuranceConfig(&d.Assurance)

	posterID := crypto.AgentID("poster-res")
	claimerID := crypto.AgentID("worker-res")
	budget := uint64(100_000_000) // 100 AET — above MinTaskBudgetAssured

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget*2); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)

	task, err := tm.PostTask(string(posterID), "Write code", "Code task for reserve test", "code", budget, tasks.PostTaskOpts{
		AssuranceLane: "standard",
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	note := "Analysis complete. The code task was thoroughly reviewed with automated checks, static analysis, and test coverage. All required tests pass and results are documented comprehensively."
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:abc", note, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	return tm, esc, task, budget
}

// ---------------------------------------------------------------------------
// TestAutoValidator_ReplayReserve_Accrues_AssuredTask
// ---------------------------------------------------------------------------

// TestAutoValidator_ReplayReserve_Accrues_AssuredTask verifies that when the
// autovalidator settles an assured task (AssuranceFee > 0) and a
// replayReserveAccruer is wired, Accrue is called with a non-zero contribution.
func TestAutoValidator_ReplayReserve_Accrues_AssuredTask(t *testing.T) {
	d := config.DefaultConfig()
	tm, esc, task, _ := buildAssuredTask(t)

	posterID := crypto.AgentID("poster-res")
	validatorID := crypto.AgentID("testnet-validator-res")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, 200_000_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	cr := &capturingReserve{}

	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)
	// Wire an always-pass service so this test exercises reserve accrual wiring
	// rather than the CodeVerifier's keyword-scoring heuristics.
	av.SetVerificationService(&alwaysPassService{})
	av.SetReplayReserve(cr, d.Assurance.ReplayReserveShare)
	av.Start()
	t.Cleanup(av.Stop)

	waitForCompleted(t, tm, task.ID, 2*time.Second)
	// settleTask calls ApproveTask (which sets Completed status) and THEN calls
	// Accrue. Give the autovalidator goroutine a moment to finish the Accrue call
	// after waitForCompleted sees Completed status.
	deadline := time.Now().Add(500 * time.Millisecond)
	for len(cr.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	entries := cr.all()
	if len(entries) == 0 {
		t.Fatal("expected at least one Accrue call for assured task; got none")
	}
	found := false
	for _, e := range entries {
		if e.category == "code" && e.amount > 0 {
			found = true
			// Contribution should be AssuranceFee × ReplayReserveShare.
			// task.AssuranceFee = 3% of 100 AET = 3_000_000; share=0.25 → 750_000.
			wantApprox := uint64(float64(task.AssuranceFee) * d.Assurance.ReplayReserveShare)
			if e.amount != wantApprox {
				t.Errorf("Accrue amount: got %d, want %d", e.amount, wantApprox)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected Accrue(category=code, amount>0); entries = %+v", entries)
	}
}

// ---------------------------------------------------------------------------
// TestAutoValidator_ReplayReserve_NotCalled_UnassuredTask
// ---------------------------------------------------------------------------

// TestAutoValidator_ReplayReserve_NotCalled_UnassuredTask verifies that for
// an unassured task (no AssuranceLane, AssuranceFee == 0), Accrue is NOT
// called — the reserve only accrues for tasks that carry assurance fees.
func TestAutoValidator_ReplayReserve_NotCalled_UnassuredTask(t *testing.T) {
	tm := tasks.NewTaskManager()

	posterID := crypto.AgentID("poster-unassured")
	claimerID := crypto.AgentID("worker-unassured")
	validatorID := crypto.AgentID("validator-unassured")
	budget := uint64(1_000_000)

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget*2); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	task, err := tm.PostTask(string(posterID), "Quick research task", "Short research with findings", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	note := "Research complete with detailed findings, comprehensive analysis, and well-documented results. All required items have been addressed thoroughly."
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:xyz", note, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	cr := &capturingReserve{}
	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)
	av.SetReplayReserve(cr, 0.25)
	av.Start()
	t.Cleanup(av.Stop)

	waitForCompleted(t, tm, task.ID, 2*time.Second)

	if entries := cr.all(); len(entries) != 0 {
		t.Errorf("expected no Accrue calls for unassured task; got %+v", entries)
	}
}
