package verification

import (
	"fmt"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// DeterministicVerifier produces a DeterministicReport for a piece of evidence.
// It runs structural hard-gate checks (content presence, length, hash) and
// delegates scoring to the wrapped VerifierRegistry (falling back to the
// keyword verifier when the registry is nil).
//
// The "threshold" hard gate captures the registry's per-category pass/fail
// decision so that ConsensusSufficiencyChecker does not need to replicate the
// threshold table that lives inside VerifierRegistry.thresholdFor().
type DeterministicVerifier struct {
	registry *evidence.VerifierRegistry
}

// NewDeterministicVerifier wraps r. Pass nil to use the keyword-verifier
// fallback (same behaviour as a VerifierRegistry with no category match).
func NewDeterministicVerifier(r *evidence.VerifierRegistry) *DeterministicVerifier {
	return &DeterministicVerifier{registry: r}
}

// Verify runs deterministic checks on ev and returns a DeterministicReport.
// HardGates are ordered: "threshold" first (so InProcessVerifier can rely on
// HardGates[0] for the primary pass/fail decision), followed by structural
// gates that are informational only.
func (dv *DeterministicVerifier) Verify(category string, ev *evidence.Evidence, title, description string, budget uint64) *DeterministicReport {
	// Structural gates — evaluated before calling the registry so failures are
	// visible even when the registry short-circuits on empty input.
	hasOutput := ev != nil && (strings.TrimSpace(ev.Summary) != "" || strings.TrimSpace(ev.OutputPreview) != "" || ev.OutputSize > 0)
	hashValid := ev != nil && ev.Hash != ""

	contentLen := 0
	if ev != nil {
		contentLen = len(ev.Summary) + len(ev.OutputPreview)
	}
	outputSize := uint64(0)
	if ev != nil {
		outputSize = ev.OutputSize
	}
	minLength := contentLen >= 10 || outputSize >= 10

	// Delegate scoring to the registry (or keyword-verifier fallback).
	var score *evidence.Score
	var thresholdPassed bool
	if dv.registry != nil {
		score, thresholdPassed = dv.registry.Verify(ev, title, description, budget, category)
	} else {
		score, thresholdPassed = evidence.NewVerifier().Verify(ev, title, description, budget)
	}
	if score == nil {
		score = &evidence.Score{}
	}

	// "threshold" gate is first so callers can read HardGates[0] for the
	// primary verdict without searching by name.
	gates := []GateResult{
		{
			Name:   "threshold",
			Pass:   thresholdPassed,
			Detail: fmt.Sprintf("overall=%.3f", score.Overall),
		},
		{
			Name:   "has_output",
			Pass:   hasOutput,
			Detail: "evidence must contain non-empty Summary, OutputPreview, or OutputSize",
		},
		{
			Name:   "min_length",
			Pass:   minLength,
			Detail: "content length (summary+preview) or OutputSize must be >= 10 bytes",
		},
		{
			Name:   "hash_valid",
			Pass:   hashValid,
			Detail: "evidence Hash must be non-empty",
		},
	}

	return &DeterministicReport{
		HardGates: gates,
		NumericScores: map[string]float64{
			"relevance":    score.Relevance,
			"completeness": score.Completeness,
			"quality":      score.Quality,
			"overall":      score.Overall,
		},
	}
}
