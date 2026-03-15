package assurance

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// challengeCfg returns an AssuranceConfig for challenge tests.
func challengeCfg() *config.AssuranceConfig {
	cfg := config.DefaultConfig()
	return &cfg.Assurance
}

// ---------------------------------------------------------------------------
// TestComputeBond_AboveFloor
// ---------------------------------------------------------------------------

func TestComputeBond_AboveFloor(t *testing.T) {
	cfg := challengeCfg() // rate=0.01, floor=1_000_000
	// budget = 500_000_000 → computed = 0.01 × 500_000_000 = 5_000_000 > floor
	bond := ComputeBond(500_000_000, cfg)
	if bond != 5_000_000 {
		t.Errorf("ComputeBond: got %d, want 5_000_000", bond)
	}
}

// ---------------------------------------------------------------------------
// TestComputeBond_Floor
// ---------------------------------------------------------------------------

func TestComputeBond_Floor(t *testing.T) {
	cfg := challengeCfg() // rate=0.01, floor=1_000_000
	// budget = 50_000 → computed = 0.01 × 50_000 = 500 < floor → returns floor
	bond := ComputeBond(50_000, cfg)
	if bond != cfg.ChallengeBondFloor {
		t.Errorf("ComputeBond: got %d, want floor %d", bond, cfg.ChallengeBondFloor)
	}
}

// ---------------------------------------------------------------------------
// TestChallengeManager_OpenChallenge
// ---------------------------------------------------------------------------

func TestChallengeManager_OpenChallenge(t *testing.T) {
	mgr := NewChallengeManager(challengeCfg(), nil)

	c, err := mgr.OpenChallenge("task-1", "challenger-1", "target-validator-1", 5_000_000)
	if err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}
	if c.ID == "" {
		t.Error("Challenge ID must not be empty")
	}
	if c.TaskID != "task-1" {
		t.Errorf("TaskID: got %q, want task-1", c.TaskID)
	}
	if c.ChallengerID != "challenger-1" {
		t.Errorf("ChallengerID: got %q, want challenger-1", c.ChallengerID)
	}
	if c.TargetID != "target-validator-1" {
		t.Errorf("TargetID: got %q, want target-validator-1", c.TargetID)
	}
	if c.Bond != 5_000_000 {
		t.Errorf("Bond: got %d, want 5_000_000", c.Bond)
	}
	if c.Status != ChallengeOpen {
		t.Errorf("Status: got %q, want open", c.Status)
	}
	if c.CreatedAt.IsZero() {
		t.Error("CreatedAt must not be zero")
	}
}

// ---------------------------------------------------------------------------
// TestChallengeManager_ResolveChallenge_Succeeded
// ---------------------------------------------------------------------------

func TestChallengeManager_ResolveChallenge_Succeeded(t *testing.T) {
	mgr := NewChallengeManager(challengeCfg(), nil)
	c, _ := mgr.OpenChallenge("task-2", "challenger-2", "target-2", 4_000_000)

	res, err := mgr.ResolveChallenge(c.ID, ChallengeSucceeded, 10_000_000)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if res.Outcome != ChallengeSucceeded {
		t.Errorf("Outcome: got %q, want succeeded", res.Outcome)
	}
	if res.FraudBounty != 10_000_000 {
		t.Errorf("FraudBounty: got %d, want 10_000_000", res.FraudBounty)
	}
	if res.RefundedBond != 4_000_000 {
		t.Errorf("RefundedBond: got %d, want 4_000_000", res.RefundedBond)
	}
	if res.ForfeitAmount != 0 {
		t.Errorf("ForfeitAmount: got %d, want 0", res.ForfeitAmount)
	}
}

// ---------------------------------------------------------------------------
// TestChallengeManager_ResolveChallenge_Failed
// ---------------------------------------------------------------------------

func TestChallengeManager_ResolveChallenge_Failed(t *testing.T) {
	mgr := NewChallengeManager(challengeCfg(), nil)
	c, _ := mgr.OpenChallenge("task-3", "challenger-3", "target-3", 6_000_000)

	res, err := mgr.ResolveChallenge(c.ID, ChallengeFailed, 0)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if res.Outcome != ChallengeFailed {
		t.Errorf("Outcome: got %q, want failed", res.Outcome)
	}
	if res.ForfeitAmount != 6_000_000 {
		t.Errorf("ForfeitAmount: got %d, want 6_000_000", res.ForfeitAmount)
	}
	// 50/50 split.
	if res.AccusedShare != 3_000_000 {
		t.Errorf("AccusedShare: got %d, want 3_000_000", res.AccusedShare)
	}
	if res.ReserveSplit != 3_000_000 {
		t.Errorf("ReserveSplit: got %d, want 3_000_000", res.ReserveSplit)
	}
	if res.RefundedBond != 0 {
		t.Errorf("RefundedBond: got %d, want 0", res.RefundedBond)
	}
}

// ---------------------------------------------------------------------------
// TestChallengeManager_ResolveChallenge_Partial
// ---------------------------------------------------------------------------

func TestChallengeManager_ResolveChallenge_Partial(t *testing.T) {
	mgr := NewChallengeManager(challengeCfg(), nil)
	c, _ := mgr.OpenChallenge("task-4", "challenger-4", "target-4", 2_000_000)

	res, err := mgr.ResolveChallenge(c.ID, ChallengePartial, 0)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if res.Outcome != ChallengePartial {
		t.Errorf("Outcome: got %q, want partial", res.Outcome)
	}
	if res.RefundedBond != 2_000_000 {
		t.Errorf("RefundedBond: got %d, want 2_000_000 (bond returned on partial)", res.RefundedBond)
	}
	if res.FraudBounty != 0 {
		t.Errorf("FraudBounty: got %d, want 0 for partial", res.FraudBounty)
	}
	if res.ForfeitAmount != 0 {
		t.Errorf("ForfeitAmount: got %d, want 0 for partial", res.ForfeitAmount)
	}
}

// ---------------------------------------------------------------------------
// TestChallengeManager_ResolveChallenge_AlreadyResolved
// ---------------------------------------------------------------------------

func TestChallengeManager_ResolveChallenge_AlreadyResolved(t *testing.T) {
	mgr := NewChallengeManager(challengeCfg(), nil)
	c, _ := mgr.OpenChallenge("task-5", "challenger-5", "target-5", 1_000_000)

	if _, err := mgr.ResolveChallenge(c.ID, ChallengeSucceeded, 0); err != nil {
		t.Fatalf("first ResolveChallenge: %v", err)
	}
	_, err := mgr.ResolveChallenge(c.ID, ChallengeFailed, 0)
	if err == nil {
		t.Fatal("expected error on second resolve")
	}
	if !errors.Is(err, ErrChallengeAlreadyResolved) {
		t.Errorf("expected ErrChallengeAlreadyResolved, got %v", err)
	}
}
