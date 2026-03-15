package validator

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// assignCfg returns a ValidatorConfig tuned for testing.
func assignCfg() *config.ValidatorConfig {
	return &config.ValidatorConfig{
		StakeBaseMinimum:              10_000_000_000,
		GenesisSkipProbation:          true,
		AssignmentCalibrationWeak:     0.7,
		AssignmentCalibrationModerate: 1.0,
		AssignmentCalibrationStrong:   1.2,
		AssignmentProbationModifier:   0.3,
		CapBelow10Validators:          0.20,
		CapAtOrAbove10Validators:      0.15,
		CapEnforcementMinValidators:   5,
		CapEpochHours:                 24,
		ClusterPairwiseThresholdDeterministic:    0.98,
		ClusterPairwiseThresholdNonDeterministic: 0.95,
		ClusterPairwiseMinShared:                 50,
		ClusterReplayRate:                        1.0,
	}
}

// makeActiveValidator creates an active validator in r with the given agentID.
func makeActiveValidator(t *testing.T, r *ValidatorRegistry, agentID string, categories []string) *Validator {
	t.Helper()
	v, err := r.Register(agentID, 10_000_000_000, categories, true) // genesis → StatusActive
	if err != nil {
		t.Fatalf("Register %s: %v", agentID, err)
	}
	return v
}

// makeProbationaryValidator creates a probationary validator.
func makeProbationaryValidator(t *testing.T, r *ValidatorRegistry, agentID string) *Validator {
	t.Helper()
	v, err := r.Register(agentID, 10_000_000_000, nil, false) // non-genesis → StatusProbationary
	if err != nil {
		t.Fatalf("Register %s: %v", agentID, err)
	}
	return v
}

// newEngine is a convenience constructor that wires registry + config.
func newEngine(r *ValidatorRegistry) *AssignmentEngine {
	return NewAssignmentEngine(r, assignCfg())
}

// newRegistry builds an empty in-memory ValidatorRegistry.
func newRegistry() *ValidatorRegistry {
	return NewValidatorRegistry(assignCfg(), nil)
}

// ---------------------------------------------------------------------------
// calibrationSource stub
// ---------------------------------------------------------------------------

type stubCalibration struct {
	// agentCalibration[agentID+":"+category] = (accuracy, signals)
	data map[string][2]float64 // [0]=accuracy, [1]=signals
}

func (s *stubCalibration) CategoryCalibrationForActor(agentID, category string) (*CalibrationData, error) {
	key := agentID + ":" + category
	if entry, ok := s.data[key]; ok {
		return &CalibrationData{
			TotalSignals: int(entry[1]),
			Accuracy:     entry[0],
		}, nil
	}
	return nil, nil
}

func newStubCalibration() *stubCalibration {
	return &stubCalibration{data: make(map[string][2]float64)}
}

func (s *stubCalibration) set(agentID, category string, accuracy float64, signals int) {
	s.data[agentID+":"+category] = [2]float64{accuracy, float64(signals)}
}

// ---------------------------------------------------------------------------
// Assignment tests
// ---------------------------------------------------------------------------

// Test 1: 5 equal-weight validators → roughly equal distribution over 100 draws
func TestSelectValidator_EqualWeights_RoughlyEven(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	ids := make([]string, 5)
	for i := 0; i < 5; i++ {
		v := makeActiveValidator(t, r, fmt.Sprintf("agent-%d", i), nil)
		ids[i] = v.ID
	}

	counts := make(map[string]int)
	for n := 0; n < 100; n++ {
		v, err := eng.SelectValidator("", nil, false)
		if err != nil {
			t.Fatalf("SelectValidator: %v", err)
		}
		counts[v.ID]++
	}
	// Each validator should get between 5 and 40 assignments (≈20% ± generous tolerance).
	for _, id := range ids {
		if counts[id] < 5 || counts[id] > 40 {
			t.Errorf("validator %s: %d/100 assignments — outside expected range [5,40]", id, counts[id])
		}
	}
}

// Test 2: 1 strong (1.2x), 1 weak (0.7x), 3 moderate → strong gets more
func TestSelectValidator_CalibrationWeights(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	eng := NewAssignmentEngine(r, cfg)
	cal := newStubCalibration()
	eng.SetCalibrationSource(cal)

	vStrong := makeActiveValidator(t, r, "strong-agent", nil)
	vWeak := makeActiveValidator(t, r, "weak-agent", nil)
	mod1 := makeActiveValidator(t, r, "mod-1", nil)
	mod2 := makeActiveValidator(t, r, "mod-2", nil)
	mod3 := makeActiveValidator(t, r, "mod-3", nil)

	// strong: accuracy=0.95 → strong modifier (1.2)
	cal.set("strong-agent", "", 0.95, 30)
	// weak: accuracy=0.50 → weak modifier (0.7)
	cal.set("weak-agent", "", 0.50, 30)
	// moderate: no calibration data → moderate modifier (1.0)
	_ = mod1
	_ = mod2
	_ = mod3

	counts := make(map[string]int)
	const draws = 1000
	for n := 0; n < draws; n++ {
		v, err := eng.SelectValidator("", nil, false)
		if err != nil {
			t.Fatalf("SelectValidator: %v", err)
		}
		counts[v.ID]++
	}

	// Expected: total weight = 1.2 + 0.7 + 3×1.0 = 4.9
	// strong share ≈ 1.2/4.9 ≈ 24.5%
	// weak share ≈ 0.7/4.9 ≈ 14.3%
	strongShare := float64(counts[vStrong.ID]) / draws
	weakShare := float64(counts[vWeak.ID]) / draws

	if strongShare < 0.18 || strongShare > 0.32 {
		t.Errorf("strong validator share=%.2f, expected ≈0.245 ± tolerance", strongShare)
	}
	if weakShare > strongShare {
		t.Errorf("weak validator (%d) should get fewer than strong (%d)", counts[vWeak.ID], counts[vStrong.ID])
	}
}

// Test 3: 1 probationary (0.3 modifier) among 4 active → probationary gets far fewer
func TestSelectValidator_ProbationaryGetsLess(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	// Disable cap enforcement so it doesn't dominate the result.
	cfg.CapEnforcementMinValidators = 100
	eng := NewAssignmentEngine(r, cfg)
	vProb := makeProbationaryValidator(t, r, "prob-agent")
	for i := 0; i < 4; i++ {
		makeActiveValidator(t, r, fmt.Sprintf("active-%d", i), nil)
	}

	counts := make(map[string]int)
	const draws = 500
	for n := 0; n < draws; n++ {
		v, _ := eng.SelectValidator("", nil, false)
		counts[v.ID]++
	}

	// Probationary weight=0.3; 4 active each weight=1.0 → total=4.3
	// Expected probationary share ≈ 0.3/4.3 ≈ 7.0%
	probShare := float64(counts[vProb.ID]) / draws
	if probShare > 0.15 {
		t.Errorf("probationary share=%.2f, expected < 0.15", probShare)
	}
}

// Test 4: excludeIDs — excluded validator never selected
func TestSelectValidator_ExcludesIDs(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	vExcluded := makeActiveValidator(t, r, "excluded", nil)
	for i := 0; i < 3; i++ {
		makeActiveValidator(t, r, fmt.Sprintf("ok-%d", i), nil)
	}

	for n := 0; n < 100; n++ {
		v, err := eng.SelectValidator("", []string{vExcluded.ID}, false)
		if err != nil {
			t.Fatalf("SelectValidator: %v", err)
		}
		if v.ID == vExcluded.ID {
			t.Fatalf("excluded validator was selected on draw %d", n)
		}
	}
}

// Test 5: no eligible validators → returns ErrNoEligibleValidators
func TestSelectValidator_NoEligible_Error(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	// No validators registered — should error.
	_, err := eng.SelectValidator("code", nil, false)
	if !errors.Is(err, ErrNoEligibleValidators) {
		t.Errorf("expected ErrNoEligibleValidators, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cap tests
// ---------------------------------------------------------------------------

// Test 6: cap enforcement below 10 validators → 20% max share
func TestCap_Below10Validators_20Percent(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEnforcementMinValidators = 1 // activate cap with just a few validators
	eng := NewAssignmentEngine(r, cfg)

	// 6 validators (< 10 → 20% cap)
	for i := 0; i < 6; i++ {
		makeActiveValidator(t, r, fmt.Sprintf("v%d", i), []string{"code"})
	}

	counts := make(map[string]int)
	const draws = 200
	for n := 0; n < draws; n++ {
		v, err := eng.SelectValidator("code", nil, false)
		if err != nil {
			t.Fatalf("draw %d: %v", n, err)
		}
		counts[v.ID]++
	}

	// No single validator should exceed 20% × 200 = 40 assignments.
	// Give a small tolerance for the last draw before cap kicks in.
	for id, cnt := range counts {
		if cnt > 50 { // 25% of 200 — slightly above cap to account for boundary
			t.Errorf("validator %s: %d/%d assignments — exceeds 20%% cap (expected <= ~40)", id, cnt, draws)
		}
	}
}

// Test 7: cap enforcement at 10+ validators → 15% max share
func TestCap_AtOrAbove10Validators_15Percent(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEnforcementMinValidators = 1
	eng := NewAssignmentEngine(r, cfg)

	// 10 validators (≥ 10 → 15% cap)
	for i := 0; i < 10; i++ {
		makeActiveValidator(t, r, fmt.Sprintf("v%d", i), []string{"code"})
	}

	counts := make(map[string]int)
	const draws = 200
	for n := 0; n < draws; n++ {
		v, err := eng.SelectValidator("code", nil, false)
		if err != nil {
			t.Fatalf("draw %d: %v", n, err)
		}
		counts[v.ID]++
	}

	// No single validator should exceed 15% × 200 = 30 assignments.
	for id, cnt := range counts {
		if cnt > 40 { // slightly above theoretical 30 to account for boundary effects
			t.Errorf("validator %s: %d/%d assignments — exceeds 15%% cap", id, cnt, draws)
		}
	}
}

// Test 8: fewer than CapEnforcementMinValidators → no cap applied
func TestCap_BelowMinValidators_NoCapApplied(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEnforcementMinValidators = 5
	eng := NewAssignmentEngine(r, cfg)

	// Only 3 validators — below enforcement threshold.
	vOnly := makeActiveValidator(t, r, "only-0", []string{"code"})
	makeActiveValidator(t, r, "only-1", []string{"code"})
	makeActiveValidator(t, r, "only-2", []string{"code"})

	// With cap disabled, one validator could get all draws if it happens to
	// always be selected (probabilistically unlikely but cap must not block it).
	// Just verify no error is returned for many draws.
	for n := 0; n < 30; n++ {
		// Force selection of vOnly by excluding the others.
		v, err := eng.SelectValidator("code", []string{"only-1", "only-2"}, false)
		if err != nil {
			t.Fatalf("draw %d: %v", n, err)
		}
		if v.ID != vOnly.ID {
			// It's fine if another gets selected — the point is no error.
			_ = v
		}
	}
}

// Test 9: capped validator is skipped → next best selected
func TestCap_CappedValidatorSkipped(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEnforcementMinValidators = 1
	cfg.CapBelow10Validators = 0.20
	eng := NewAssignmentEngine(r, cfg)

	v0 := makeActiveValidator(t, r, "v0", []string{"code"})
	v1 := makeActiveValidator(t, r, "v1", []string{"code"})

	// Inject a large background total so v0 is well above the 20% cap while v1
	// stays below cap even after 20 draws.  v0=100/100=100%>20%; after 20 draws
	// to v1: v1=20/120=16.7%<20%, so v1 is never capped during the test.
	eng.mu.Lock()
	eng.assignmentCount["code"] = map[string]int{v0.ID: 100}
	eng.mu.Unlock()

	// v0 is at 100% > 20% → should be skipped; v1 should always win.
	for n := 0; n < 20; n++ {
		v, err := eng.SelectValidator("code", nil, false)
		if err != nil {
			t.Fatalf("draw %d: %v", n, err)
		}
		if v.ID == v0.ID {
			t.Errorf("capped validator v0 was selected on draw %d", n)
		}
		_ = v1
	}
}

// Test 10: epoch reset clears assignment counts
func TestCap_EpochReset_ClearsAssignmentCounts(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEpochHours = 1
	cfg.CapEnforcementMinValidators = 1
	eng := NewAssignmentEngine(r, cfg)

	makeActiveValidator(t, r, "va", []string{"code"})
	makeActiveValidator(t, r, "vb", []string{"code"})

	// Drain 20 draws to build up counts.
	for n := 0; n < 20; n++ {
		_, _ = eng.SelectValidator("code", nil, false)
	}

	// Verify counts are non-zero.
	counts := eng.AssignmentCounts("code")
	total := 0
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		t.Fatal("expected non-zero assignment counts before epoch reset")
	}

	// Force epoch expiry by backdating the epoch start.
	eng.forceEpochStart(time.Now().Add(-2 * time.Hour))

	// Next draw should trigger epoch reset.
	_, err := eng.SelectValidator("code", nil, false)
	if err != nil {
		t.Fatalf("SelectValidator after epoch expire: %v", err)
	}

	// Old counts should have been cleared (new epoch has 1 draw).
	counts = eng.AssignmentCounts("code")
	newTotal := 0
	for _, c := range counts {
		newTotal += c
	}
	if newTotal != 1 {
		t.Errorf("expected 1 assignment in new epoch, got %d", newTotal)
	}
}

// ---------------------------------------------------------------------------
// Cluster tests
// ---------------------------------------------------------------------------

// Test 11: RecordAgreement tracks pair correctly
func TestRecordAgreement_TracksCorrectly(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)

	eng.RecordAgreement("v1", "v2", true)
	eng.RecordAgreement("v1", "v2", true)
	eng.RecordAgreement("v1", "v2", false)

	rec := eng.PairwiseRecord("v1", "v2")
	if rec == nil {
		t.Fatal("expected pairwise record, got nil")
	}
	if rec.SharedTasks != 3 {
		t.Errorf("SharedTasks: got %d, want 3", rec.SharedTasks)
	}
	if rec.Agreements != 2 {
		t.Errorf("Agreements: got %d, want 2", rec.Agreements)
	}

	// Symmetric storage — key order should not matter.
	rec2 := eng.PairwiseRecord("v2", "v1")
	if rec2 == nil || rec2.SharedTasks != 3 {
		t.Error("expected symmetric access to return same record")
	}
}

// Test 12: CheckPairwiseClusters flags pair above threshold
func TestCheckPairwiseClusters_FlagsHighAgreement(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.ClusterPairwiseMinShared = 5 // low minimum for test
	eng := NewAssignmentEngine(r, cfg)

	makeActiveValidator(t, r, "va", nil)
	makeActiveValidator(t, r, "vb", nil)
	vaID, _ := r.GetByAgentID("va")
	vbID, _ := r.GetByAgentID("vb")

	// 9 agreements out of 10 = 90% — above 95% threshold? No. Let's use 98/100 > 95%.
	for i := 0; i < 98; i++ {
		eng.RecordAgreement(vaID.ID, vbID.ID, true)
	}
	for i := 0; i < 2; i++ {
		eng.RecordAgreement(vaID.ID, vbID.ID, false)
	}

	clusters := eng.CheckPairwiseClusters(false) // non-structured → 95% threshold
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if len(clusters[0]) != 2 {
		t.Errorf("expected cluster size 2, got %d", len(clusters[0]))
	}

	// Both validators should now have the same clusterID.
	cidA := eng.ClusterOf(vaID.ID)
	cidB := eng.ClusterOf(vbID.ID)
	if cidA == "" || cidA != cidB {
		t.Errorf("cluster IDs should match: %q vs %q", cidA, cidB)
	}
}

// Test 13: transitive closure (A-B flagged, B-C flagged → A,B,C one cluster)
func TestCheckPairwiseClusters_TransitiveClosure(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.ClusterPairwiseMinShared = 5
	eng := NewAssignmentEngine(r, cfg)

	makeActiveValidator(t, r, "va", nil)
	makeActiveValidator(t, r, "vb", nil)
	makeActiveValidator(t, r, "vc", nil)

	vA, _ := r.GetByAgentID("va")
	vB, _ := r.GetByAgentID("vb")
	vC, _ := r.GetByAgentID("vc")

	// A-B: 98/100 > 95%
	for i := 0; i < 98; i++ {
		eng.RecordAgreement(vA.ID, vB.ID, true)
	}
	for i := 0; i < 2; i++ {
		eng.RecordAgreement(vA.ID, vB.ID, false)
	}
	// B-C: 97/100 > 95%
	for i := 0; i < 97; i++ {
		eng.RecordAgreement(vB.ID, vC.ID, true)
	}
	for i := 0; i < 3; i++ {
		eng.RecordAgreement(vB.ID, vC.ID, false)
	}

	clusters := eng.CheckPairwiseClusters(false) // non-structured
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster (transitive), got %d", len(clusters))
	}
	if len(clusters[0]) != 3 {
		t.Errorf("expected cluster size 3, got %d", len(clusters[0]))
	}

	cidA := eng.ClusterOf(vA.ID)
	cidB := eng.ClusterOf(vB.ID)
	cidC := eng.ClusterOf(vC.ID)
	if cidA == "" || cidA != cidB || cidB != cidC {
		t.Errorf("all three should share a cluster: %q %q %q", cidA, cidB, cidC)
	}
}

// Test 14: cluster cap — validators in same cluster counted together
func TestCap_ClusterCountedTogether(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.CapEnforcementMinValidators = 1
	cfg.CapBelow10Validators = 0.20
	eng := NewAssignmentEngine(r, cfg)

	vA := makeActiveValidator(t, r, "cA", []string{"code"})
	vB := makeActiveValidator(t, r, "cB", []string{"code"})
	vC := makeActiveValidator(t, r, "cC", []string{"code"})

	// Manually put vA and vB in the same cluster.
	// Inject a large background total: cluster (vA+vB) = 100 out of 100 = 100% > 20%.
	// vC = 0 out of 100 = 0%; after 20 draws to vC: vC=20/120=16.7% < 20%.
	eng.mu.Lock()
	eng.clusters[vA.ID] = "cluster:test"
	eng.clusters[vB.ID] = "cluster:test"
	eng.assignmentCount["code"] = map[string]int{vA.ID: 60, vB.ID: 40}
	eng.mu.Unlock()

	// Both cluster members should be capped (cluster total 100/100 = 100% > 20%).
	for n := 0; n < 20; n++ {
		v, err := eng.SelectValidator("code", nil, false)
		if err != nil {
			t.Fatalf("draw %d: %v", n, err)
		}
		if v.ID == vA.ID || v.ID == vB.ID {
			t.Errorf("cluster member was selected despite cluster being capped (draw %d)", n)
		}
		_ = vC
	}
}

// Test 15: deterministic vs non-deterministic threshold difference
func TestCheckPairwiseClusters_DifferentThresholds(t *testing.T) {
	r := newRegistry()
	cfg := assignCfg()
	cfg.ClusterPairwiseMinShared = 5
	eng := NewAssignmentEngine(r, cfg)

	makeActiveValidator(t, r, "da", nil)
	makeActiveValidator(t, r, "db", nil)
	vD, _ := r.GetByAgentID("da")
	vE, _ := r.GetByAgentID("db")

	// 96/100 = 96% agreement — above 95% but below 98%
	for i := 0; i < 96; i++ {
		eng.RecordAgreement(vD.ID, vE.ID, true)
	}
	for i := 0; i < 4; i++ {
		eng.RecordAgreement(vD.ID, vE.ID, false)
	}

	// Non-deterministic threshold = 95% → should flag.
	clusters := eng.CheckPairwiseClusters(false)
	if len(clusters) == 0 {
		t.Error("96% should be flagged at non-deterministic threshold (95%)")
	}

	// Reset clusters.
	eng.mu.Lock()
	eng.clusters = make(map[string]string)
	eng.mu.Unlock()

	// Deterministic threshold = 98% → 96% should NOT flag.
	clusters = eng.CheckPairwiseClusters(true)
	if len(clusters) != 0 {
		t.Errorf("96%% should NOT be flagged at deterministic threshold (98%%), got %d clusters", len(clusters))
	}
}

// ---------------------------------------------------------------------------
// Replay independence tests
// ---------------------------------------------------------------------------

// Test 16: SelectReplayExecutor excludes original verifier
func TestSelectReplayExecutor_ExcludesOriginalVerifier(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	vOrig := makeActiveValidator(t, r, "orig", nil)
	makeActiveValidator(t, r, "exec-1", nil)
	makeActiveValidator(t, r, "exec-2", nil)

	for n := 0; n < 50; n++ {
		v, err := eng.SelectReplayExecutor("", vOrig.ID, "")
		if err != nil {
			t.Fatalf("SelectReplayExecutor: %v", err)
		}
		if v.ID == vOrig.ID {
			t.Fatalf("original verifier selected as replay executor on draw %d", n)
		}
	}
}

// Test 17: SelectReplayExecutor excludes entire cluster of original verifier
func TestSelectReplayExecutor_ExcludesCluster(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	vOrig := makeActiveValidator(t, r, "orig-clust", nil)
	vCluster := makeActiveValidator(t, r, "cluster-mate", nil)
	vIndep := makeActiveValidator(t, r, "independent", nil)

	// Put orig and cluster-mate in the same cluster.
	eng.mu.Lock()
	eng.clusters[vOrig.ID] = "cluster:ABC"
	eng.clusters[vCluster.ID] = "cluster:ABC"
	eng.mu.Unlock()

	for n := 0; n < 50; n++ {
		v, err := eng.SelectReplayExecutor("", vOrig.ID, "")
		if err != nil {
			t.Fatalf("SelectReplayExecutor: %v", err)
		}
		if v.ID == vOrig.ID || v.ID == vCluster.ID {
			t.Fatalf("cluster member %s selected as replay executor on draw %d", v.ID, n)
		}
		if v.ID != vIndep.ID {
			t.Errorf("expected independent validator, got %s", v.ID)
		}
	}
}

// Test 18: SelectReplayExecutor with only clustered validators → ErrNoEligibleValidators
func TestSelectReplayExecutor_AllClustered_Error(t *testing.T) {
	r := newRegistry()
	eng := newEngine(r)
	vOrig := makeActiveValidator(t, r, "orig-all", nil)
	vMate := makeActiveValidator(t, r, "mate-all", nil)

	// Both in the same cluster.
	eng.mu.Lock()
	eng.clusters[vOrig.ID] = "cluster:ALL"
	eng.clusters[vMate.ID] = "cluster:ALL"
	eng.mu.Unlock()

	_, err := eng.SelectReplayExecutor("", vOrig.ID, "")
	if !errors.Is(err, ErrNoEligibleValidators) {
		t.Errorf("expected ErrNoEligibleValidators, got %v", err)
	}
}
