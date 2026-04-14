package projections

import (
	"context"
	"fmt"
	"sort"
)

// healthSnapshot is a point-in-time copy of the fields HealthCheck needs
// from each registered entry, captured under the registry lock so probes
// can be called without holding it.
type healthSnapshot struct {
	name              string
	classification    Classification
	registeredAtEpoch uint64
	allowIdle         bool
	idleReason        string
	probe             StateProbe
}

// ProjectionHealth classifies the health of a single projection against
// plan §9.5 PR-5 and its exception rules.
type ProjectionHealth uint8

const (
	// HealthOK: Canonical projection is past the eligibility window and
	// its StateProbe reports populated state. Advisory projections are
	// never HealthOK — they are HealthAdvisory.
	HealthOK ProjectionHealth = iota

	// HealthNotYetEligible: Canonical projection has been registered for
	// no more than EligibilityWindow epochs. PR-5 does not yet apply.
	HealthNotYetEligible

	// HealthAllowedIdle: Canonical projection is past the window but has
	// the CR-9 named exception set. Empty state is permitted indefinitely.
	HealthAllowedIdle

	// HealthEmpty: Canonical projection is past the window, not idle-
	// exempt, and its StateProbe returned empty=true. This is the exact
	// situation PR-5 is designed to catch.
	HealthEmpty

	// HealthProbeFailed: StateProbe returned an error. Treated as fatal
	// for Overall because a probe that cannot report state is itself a
	// diagnostic gap.
	HealthProbeFailed

	// HealthAdvisory: Advisory classification. PR-5 does not apply; this
	// is informational only and never affects Overall.
	HealthAdvisory
)

// String renders a ProjectionHealth for diagnostics.
func (h ProjectionHealth) String() string {
	switch h {
	case HealthOK:
		return "OK"
	case HealthNotYetEligible:
		return "NotYetEligible"
	case HealthAllowedIdle:
		return "AllowedIdle"
	case HealthEmpty:
		return "Empty"
	case HealthProbeFailed:
		return "ProbeFailed"
	case HealthAdvisory:
		return "Advisory"
	default:
		return fmt.Sprintf("unknown(%d)", h)
	}
}

// ProjectionStatus is the per-entry HealthCheck result.
type ProjectionStatus struct {
	Name   string
	Health ProjectionHealth
	Reason string
}

// HealthStatus is the aggregate HealthCheck result. Overall is HealthOK
// unless any Canonical check is HealthEmpty or HealthProbeFailed, in which
// case Overall reflects the worst failing status (HealthProbeFailed ranks
// above HealthEmpty).
type HealthStatus struct {
	Overall ProjectionHealth
	Checks  []ProjectionStatus
}

// HealthCheck evaluates every registered projection against plan §9.5 PR-5.
// Advisory projections are classified as HealthAdvisory and do not affect
// Overall. Canonical projections are evaluated per the algorithm below:
//
//  1. If ageEpochs <= EligibilityWindow, HealthNotYetEligible.
//     (Strict inequality: "more than EligibilityWindow" per plan §9.5.)
//  2. Else if AllowIdleWithJustification, HealthAllowedIdle.
//  3. Else call StateProbe:
//     - probe error        -> HealthProbeFailed
//     - probe empty=true   -> HealthEmpty
//     - probe empty=false  -> HealthOK
//
// Clock-backwards safety: if epochFn() returns a value less than
// registeredAtEpoch, the check treats the entry as HealthNotYetEligible
// rather than overflowing the subtraction.
//
// An empty registry returns {Overall: HealthOK, Checks: nil}.
func (r *ProjectionRegistry) HealthCheck(ctx context.Context) HealthStatus {
	currentEpoch := r.epochFn()

	// Snapshot the entries under the read lock so probes are not called
	// while holding the registry lock. Probes may take arbitrary time.
	r.mu.RLock()
	snaps := make([]healthSnapshot, 0, len(r.entries))
	for _, e := range r.entries {
		snaps = append(snaps, healthSnapshot{
			name:              e.projection.Name,
			classification:    e.projection.Classification,
			registeredAtEpoch: e.registeredAtEpoch,
			allowIdle:         e.projection.AllowIdleWithJustification,
			idleReason:        e.projection.IdleJustification,
			probe:             e.projection.StateProbe,
		})
	}
	r.mu.RUnlock()

	if len(snaps) == 0 {
		return HealthStatus{Overall: HealthOK, Checks: nil}
	}

	// Sort for deterministic ordering (same as List).
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].name < snaps[j].name })

	checks := make([]ProjectionStatus, 0, len(snaps))
	worst := HealthOK
	for _, s := range snaps {
		status := ProjectionStatus{Name: s.name}

		if s.classification == Advisory {
			status.Health = HealthAdvisory
			status.Reason = "advisory projection; PR-5 does not apply"
			checks = append(checks, status)
			continue
		}

		// Canonical. Compute age with clock-backwards safety.
		var ageEpochs uint64
		if currentEpoch < s.registeredAtEpoch {
			// Clock went backwards. Treat as fresh registration: within window.
			ageEpochs = 0
		} else {
			ageEpochs = currentEpoch - s.registeredAtEpoch
		}

		if ageEpochs <= EligibilityWindow {
			status.Health = HealthNotYetEligible
			status.Reason = fmt.Sprintf(
				"canonical projection within eligibility window: ageEpochs=%d <= EligibilityWindow=%d",
				ageEpochs, EligibilityWindow,
			)
			checks = append(checks, status)
			continue
		}

		if s.allowIdle {
			status.Health = HealthAllowedIdle
			status.Reason = s.idleReason
			checks = append(checks, status)
			continue
		}

		// V11 guarantees probe is non-nil here, but be defensive.
		if s.probe == nil {
			status.Health = HealthProbeFailed
			status.Reason = "StateProbe is nil (projection-authoring bug: Canonical without idle exception must provide a probe)"
			worst = rankWorse(worst, HealthProbeFailed)
			checks = append(checks, status)
			continue
		}

		empty, err := s.probe(ctx)
		if err != nil {
			status.Health = HealthProbeFailed
			status.Reason = err.Error()
			worst = rankWorse(worst, HealthProbeFailed)
			checks = append(checks, status)
			continue
		}
		if empty {
			status.Health = HealthEmpty
			status.Reason = fmt.Sprintf(
				"canonical projection empty after %d epochs (PR-5)",
				ageEpochs,
			)
			worst = rankWorse(worst, HealthEmpty)
			checks = append(checks, status)
			continue
		}
		status.Health = HealthOK
		status.Reason = ""
		checks = append(checks, status)
	}

	return HealthStatus{Overall: worst, Checks: checks}
}

// rankWorse returns the "worse" of two Overall candidates, where
// HealthProbeFailed > HealthEmpty > HealthOK. Other values are not used
// for Overall and are passed through as HealthOK (they do not flip the
// status for Advisory/NotYetEligible/AllowedIdle/HealthOK entries).
func rankWorse(current, candidate ProjectionHealth) ProjectionHealth {
	if candidate == HealthProbeFailed {
		return HealthProbeFailed
	}
	if current == HealthProbeFailed {
		return HealthProbeFailed
	}
	if candidate == HealthEmpty {
		return HealthEmpty
	}
	if current == HealthEmpty {
		return HealthEmpty
	}
	return current
}

