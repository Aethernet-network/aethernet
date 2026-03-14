package router

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Stub calibration source
// ---------------------------------------------------------------------------

// stubCalibSource satisfies calibrationSource for testing. It returns a fixed
// *CalibrationData (or nil) for any agentID/category pair.
type stubCalibSource struct {
	data *CalibrationData
	err  error
}

func (s *stubCalibSource) CategoryCalibrationForActor(_, _ string) (*CalibrationData, error) {
	return s.data, s.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newCalibRouter returns a Router with calibration enabled and the given
// source wired. Boost/penalty/thresholds use the library defaults (1.1, 0.85,
// 0.9, 0.6) so tests can rely on those values directly.
func newCalibRouter(src calibrationSource, enabled bool) *Router {
	r := &Router{
		calibration:       src,
		calibEnabled:      enabled,
		calibBoost:        1.1,
		calibPenalty:      0.85,
		calibStrongThresh: 0.9,
		calibWeakThresh:   0.6,
	}
	return r
}

const eps = 1e-9

func almostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCalibration_DisabledByDefault_NoEffect verifies that when calibEnabled
// is false, applyCalibrationModifier returns the score unchanged even when a
// calibration source is configured.
func TestCalibration_DisabledByDefault_NoEffect(t *testing.T) {
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.2} // would be weak → penalty
	r := newCalibRouter(&stubCalibSource{data: cal}, false /* disabled */)

	got := r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", 0.7)
	if !almostEqual(got, 0.7) {
		t.Errorf("score = %v; want 0.7 (disabled, no adjustment)", got)
	}
}

// TestCalibration_NoSource_NoEffect verifies that when calibEnabled is true
// but no calibration source is wired, the score is returned unchanged.
func TestCalibration_NoSource_NoEffect(t *testing.T) {
	r := newCalibRouter(nil /* no source */, true)

	got := r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", 0.6)
	if !almostEqual(got, 0.6) {
		t.Errorf("score = %v; want 0.6 (no source, no adjustment)", got)
	}
}

// TestCalibration_StrongActor_ScoreBoosted verifies that an actor with
// accuracy > StrongThreshold (0.9) receives a score × BoostFactor (1.1).
func TestCalibration_StrongActor_ScoreBoosted(t *testing.T) {
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.95}
	r := newCalibRouter(&stubCalibSource{data: cal}, true)

	base := 0.6
	got := r.applyCalibrationModifier(crypto.AgentID("strong-agent"), "code", base)
	want := base * 1.1 // 0.66
	if !almostEqual(got, want) {
		t.Errorf("score = %v; want %v (strong boost ×1.1)", got, want)
	}
}

// TestCalibration_WeakActor_ScorePenalized verifies that an actor with
// accuracy < WeakThreshold (0.6) receives a score × PenaltyFactor (0.85).
func TestCalibration_WeakActor_ScorePenalized(t *testing.T) {
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.3}
	r := newCalibRouter(&stubCalibSource{data: cal}, true)

	base := 0.8
	got := r.applyCalibrationModifier(crypto.AgentID("weak-agent"), "code", base)
	want := base * 0.85 // 0.68
	if !almostEqual(got, want) {
		t.Errorf("score = %v; want %v (weak penalty ×0.85)", got, want)
	}
}

// TestCalibration_BelowMinSamples_NoEffect verifies that when TotalSignals is
// below calibrationMinSamples (5), the score is returned unchanged even with
// actionable-looking accuracy.
func TestCalibration_BelowMinSamples_NoEffect(t *testing.T) {
	cal := &CalibrationData{TotalSignals: calibrationMinSamples - 1, Accuracy: 0.2} // would be weak
	r := newCalibRouter(&stubCalibSource{data: cal}, true)

	base := 0.7
	got := r.applyCalibrationModifier(crypto.AgentID("new-agent"), "code", base)
	if !almostEqual(got, base) {
		t.Errorf("score = %v; want %v (below min samples, no adjustment)", got, base)
	}
}

// TestCalibration_NilData_NoEffect verifies that when the calibration source
// returns nil (no data for actor/category), the score is returned unchanged.
func TestCalibration_NilData_NoEffect(t *testing.T) {
	r := newCalibRouter(&stubCalibSource{data: nil}, true)

	base := 0.5
	got := r.applyCalibrationModifier(crypto.AgentID("unknown-agent"), "writing", base)
	if !almostEqual(got, base) {
		t.Errorf("score = %v; want %v (nil data, no adjustment)", got, base)
	}
}

// TestCalibration_ModerateAccuracy_NoEffect verifies that accuracy in the
// moderate band (0.6–0.9) produces no score adjustment.
func TestCalibration_ModerateAccuracy_NoEffect(t *testing.T) {
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.75} // between thresholds
	r := newCalibRouter(&stubCalibSource{data: cal}, true)

	base := 0.65
	got := r.applyCalibrationModifier(crypto.AgentID("moderate-agent"), "research", base)
	if !almostEqual(got, base) {
		t.Errorf("score = %v; want %v (moderate accuracy, no adjustment)", got, base)
	}
}

// ---------------------------------------------------------------------------
// Cache tests
// ---------------------------------------------------------------------------

// countingCalibSource wraps a fixed CalibrationData and counts how many times
// the calibration source is actually queried.
type countingCalibSource struct {
	data  *CalibrationData
	count *int
}

func (c *countingCalibSource) CategoryCalibrationForActor(_, _ string) (*CalibrationData, error) {
	*c.count++
	return c.data, nil
}

// TestRouterCache_ReturnsCachedData verifies that two consecutive calls to
// applyCalibrationModifier with the same (agentID, category) result in only
// one invocation of the calibration source (second call is a cache hit).
func TestRouterCache_ReturnsCachedData(t *testing.T) {
	callCount := 0
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.5} // moderate — no adjustment
	r := newCalibRouter(&countingCalibSource{data: cal, count: &callCount}, true)

	base := 0.6
	r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", base)
	if callCount != 1 {
		t.Errorf("after first call: source invoked %d times; want 1", callCount)
	}

	// Second call within TTL — cache hit, source must NOT be re-queried.
	r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", base)
	if callCount != 1 {
		t.Errorf("after second call (within TTL): source invoked %d times; want still 1", callCount)
	}
}

// TestRouterCache_ExpiryRefreshes verifies that once the cache entry's
// fetchedAt is backdated past calibCacheTTL, the next call re-queries the
// calibration source.
func TestRouterCache_ExpiryRefreshes(t *testing.T) {
	callCount := 0
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.5}
	r := newCalibRouter(&countingCalibSource{data: cal, count: &callCount}, true)

	base := 0.6
	r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", base)
	if callCount != 1 {
		t.Errorf("after first call: source invoked %d times; want 1", callCount)
	}

	// Backdate the cache entry to simulate TTL expiry.
	key := "agent1|code"
	entry := r.calibCache[key]
	entry.fetchedAt = time.Now().Add(-2 * calibCacheTTL)
	r.calibCache[key] = entry

	// Second call — stale cache → source must be re-queried.
	r.applyCalibrationModifier(crypto.AgentID("agent1"), "code", base)
	if callCount != 2 {
		t.Errorf("after cache expiry: source invoked %d times; want 2", callCount)
	}
}

// TestCalibration_BoostCappedAt1 verifies that the boosted score is clamped
// to 1.0 even when base × boostFactor would exceed 1.0.
func TestCalibration_BoostCappedAt1(t *testing.T) {
	cal := &CalibrationData{TotalSignals: 20, Accuracy: 0.98} // strong
	r := newCalibRouter(&stubCalibSource{data: cal}, true)

	// base 0.99 × 1.1 = 1.089 → must be clamped to 1.0
	got := r.applyCalibrationModifier(crypto.AgentID("top-agent"), "code", 0.99)
	if got > 1.0+eps {
		t.Errorf("score = %v; want ≤ 1.0 (boost clamped)", got)
	}
	if !almostEqual(got, 1.0) {
		t.Errorf("score = %v; want 1.0 (boost capped at 1.0)", got)
	}
}
