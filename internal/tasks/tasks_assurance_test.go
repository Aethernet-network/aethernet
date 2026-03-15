package tasks

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/assurance"
	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// fixedFloor is a securityFloorChecker stub that always returns a fixed lane.
type fixedFloor struct {
	offered assurance.AssuranceLane
}

func (f *fixedFloor) CheckLane(_ string, _ assurance.AssuranceLane) *assurance.AssuranceFeeResult {
	return &assurance.AssuranceFeeResult{Lane: f.offered}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newAssuranceTaskManager() *TaskManager {
	d := config.DefaultConfig()
	m := NewTaskManager()
	m.SetAssuranceConfig(&d.Assurance)
	return m
}

// ---------------------------------------------------------------------------
// Unassured tasks
// ---------------------------------------------------------------------------

// TestPostTask_Unassured_GenerationNotEligible verifies that when no
// AssuranceLane is provided the task has GenerationEligible = false.
func TestPostTask_Unassured_GenerationNotEligible(t *testing.T) {
	m := newAssuranceTaskManager()
	task, err := m.PostTask("poster-1", "Do something", "", "code", 1_000_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if task.Contract.GenerationEligible {
		t.Error("GenerationEligible = true for unassured task; want false")
	}
	if task.AssuranceFee != 0 {
		t.Errorf("AssuranceFee = %d; want 0", task.AssuranceFee)
	}
	if task.Contract.AssuranceLane != "" {
		t.Errorf("AssuranceLane = %q; want %q", task.Contract.AssuranceLane, "")
	}
}

// TestPostTask_Unassured_ExplicitGenerationEligible_Preserved verifies that an
// explicit GenerationEligible=true opt is respected even for unassured tasks.
func TestPostTask_Unassured_ExplicitGenerationEligible_Preserved(t *testing.T) {
	m := newAssuranceTaskManager()
	yes := true
	task, err := m.PostTask("poster-1", "Do something", "", "code", 1_000_000, PostTaskOpts{
		GenerationEligible: &yes,
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if !task.Contract.GenerationEligible {
		t.Error("GenerationEligible = false; explicit true opt should be preserved")
	}
}

// ---------------------------------------------------------------------------
// Assured tasks
// ---------------------------------------------------------------------------

// TestPostTask_StandardLane_FeeComputed verifies that a standard-lane task
// has AssuranceFee > 0 and WorkerNetPayout = Budget - AssuranceFee.
func TestPostTask_StandardLane_FeeComputed(t *testing.T) {
	m := newAssuranceTaskManager()
	// budget = 100 AET; standard fee = 3% = 3_000_000 µAET
	budget := uint64(100_000_000)
	task, err := m.PostTask("poster-1", "Write code", "", "code", budget, PostTaskOpts{
		AssuranceLane: "standard",
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if task.AssuranceFee == 0 {
		t.Error("AssuranceFee = 0 for standard lane; want > 0")
	}
	if task.WorkerNetPayout != budget-task.AssuranceFee {
		t.Errorf("WorkerNetPayout = %d; want %d", task.WorkerNetPayout, budget-task.AssuranceFee)
	}
	if task.Contract.AssuranceLane != "standard" {
		t.Errorf("Contract.AssuranceLane = %q; want %q", task.Contract.AssuranceLane, "standard")
	}
	if !task.Contract.GenerationEligible {
		t.Error("GenerationEligible = false for standard lane; want true")
	}
}

// TestPostTask_HighAssuranceLane_FeeComputed verifies that a high_assurance
// task has the correct fee (6% / 4 AET floor).
func TestPostTask_HighAssuranceLane_FeeComputed(t *testing.T) {
	m := newAssuranceTaskManager()
	budget := uint64(100_000_000)
	task, err := m.PostTask("poster-1", "Analyse data", "", "data", budget, PostTaskOpts{
		AssuranceLane: "high_assurance",
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	wantFee := uint64(6_000_000) // 6%
	if task.AssuranceFee != wantFee {
		t.Errorf("AssuranceFee = %d; want %d", task.AssuranceFee, wantFee)
	}
}

// TestPostTask_EnterpriseLane_FeeComputed verifies that an enterprise task has
// the correct fee (8% / 8 AET floor).
func TestPostTask_EnterpriseLane_FeeComputed(t *testing.T) {
	m := newAssuranceTaskManager()
	budget := uint64(200_000_000)
	task, err := m.PostTask("poster-1", "Build infra", "", "infrastructure", budget, PostTaskOpts{
		AssuranceLane: "enterprise",
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	wantFee := uint64(16_000_000) // 8%
	if task.AssuranceFee != wantFee {
		t.Errorf("AssuranceFee = %d; want %d", task.AssuranceFee, wantFee)
	}
}

// TestPostTask_AssuredLane_BudgetBelowMinimum_Error verifies that an assured
// lane with a budget below MinTaskBudgetAssured returns an error.
func TestPostTask_AssuredLane_BudgetBelowMinimum_Error(t *testing.T) {
	m := newAssuranceTaskManager()
	// MinTaskBudgetAssured = 25_000_000 µAET; use 1_000_000 (below minimum)
	_, err := m.PostTask("poster-1", "Do task", "", "code", 1_000_000, PostTaskOpts{
		AssuranceLane: "standard",
	})
	if err == nil {
		t.Error("expected error for budget below minimum on assured task, got nil")
	}
}

// TestPostTask_InvalidLane_Error verifies that an unrecognised assurance lane
// returns an error.
func TestPostTask_InvalidLane_Error(t *testing.T) {
	m := newAssuranceTaskManager()
	_, err := m.PostTask("poster-1", "Do task", "", "code", 100_000_000, PostTaskOpts{
		AssuranceLane: "platinum",
	})
	if err == nil {
		t.Error("expected error for invalid lane, got nil")
	}
}

// ---------------------------------------------------------------------------
// Security floor
// ---------------------------------------------------------------------------

// TestPostTask_SecurityFloor_Rejected_SecurityFloorError verifies that when
// the security floor returns a downgraded lane, PostTask returns a
// *SecurityFloorError with the correct fields.
func TestPostTask_SecurityFloor_Rejected_SecurityFloorError(t *testing.T) {
	m := newAssuranceTaskManager()
	// Floor only offers LaneStandard but we request LaneHighAssurance.
	m.SetSecurityFloor(&fixedFloor{offered: assurance.LaneStandard})

	_, err := m.PostTask("poster-1", "Analyse data", "", "data", 100_000_000, PostTaskOpts{
		AssuranceLane: "high_assurance",
	})
	if err == nil {
		t.Fatal("expected SecurityFloorError, got nil")
	}
	var sfe *SecurityFloorError
	if !errors.As(err, &sfe) {
		t.Fatalf("err type = %T; want *SecurityFloorError", err)
	}
	if sfe.RequestedLane != "high_assurance" {
		t.Errorf("RequestedLane = %q; want %q", sfe.RequestedLane, "high_assurance")
	}
	if sfe.OfferedLane != "standard" {
		t.Errorf("OfferedLane = %q; want %q", sfe.OfferedLane, "standard")
	}
}

// TestPostTask_SecurityFloor_Approved_NoError verifies that when the security
// floor approves the requested lane PostTask succeeds.
func TestPostTask_SecurityFloor_Approved_NoError(t *testing.T) {
	m := newAssuranceTaskManager()
	// Floor approves the exact requested lane.
	m.SetSecurityFloor(&fixedFloor{offered: assurance.LaneHighAssurance})

	task, err := m.PostTask("poster-1", "Analyse data", "", "data", 100_000_000, PostTaskOpts{
		AssuranceLane: "high_assurance",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Contract.AssuranceLane != "high_assurance" {
		t.Errorf("Contract.AssuranceLane = %q; want %q", task.Contract.AssuranceLane, "high_assurance")
	}
}

// TestPostTask_SecurityFloor_NoFloorWired_AssuredLane_Passes verifies that
// when no security floor is wired, assured tasks succeed without a floor check.
func TestPostTask_SecurityFloor_NoFloorWired_AssuredLane_Passes(t *testing.T) {
	m := newAssuranceTaskManager()
	// No SetSecurityFloor call.
	task, err := m.PostTask("poster-1", "Do task", "", "code", 100_000_000, PostTaskOpts{
		AssuranceLane: "enterprise",
	})
	if err != nil {
		t.Fatalf("unexpected error (no floor wired): %v", err)
	}
	if task.Contract.AssuranceLane != "enterprise" {
		t.Errorf("AssuranceLane = %q; want %q", task.Contract.AssuranceLane, "enterprise")
	}
}

// ---------------------------------------------------------------------------
// Without assurance config wired
// ---------------------------------------------------------------------------

// TestPostTask_NoAssuranceConfig_LaneSetsLaneNoFee verifies that when
// SetAssuranceConfig is not called, an assured lane is stored on the contract
// but no fee is computed (zero assurance fee).
func TestPostTask_NoAssuranceConfig_LaneSetsLaneNoFee(t *testing.T) {
	m := NewTaskManager() // no SetAssuranceConfig
	task, err := m.PostTask("poster-1", "Do task", "", "code", 100_000_000, PostTaskOpts{
		AssuranceLane: "standard",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.AssuranceFee != 0 {
		t.Errorf("AssuranceFee = %d; want 0 (no config wired)", task.AssuranceFee)
	}
	if task.Contract.AssuranceLane != "standard" {
		t.Errorf("Contract.AssuranceLane = %q; want %q", task.Contract.AssuranceLane, "standard")
	}
}
