package replay

import (
	"fmt"
	"strings"
	"time"
)

// ComparisonExecutor performs the protocol-side comparison between original
// claimed check results (stored in a ReplayJob) and replay-submitted check
// results (provided in a ReplaySubmission), producing a real ReplayOutcome
// with per-check CheckComparisons.
//
// ComparisonExecutor does NOT execute any domain-specific code. External
// replay executors run the checks and submit results via ReplaySubmission;
// this type compares those submitted results against the original claims.
//
// Comparison rules:
//  1. All checks listed in ReplayJob.ChecksToReplay must be present in the
//     submission. Missing required checks → Status="error".
//  2. For each required check, the original claim is always pass=true
//     (all checks in ChecksToReplay were claimed to pass at submission time).
//     A replay report of pass=false is a direct contradiction → mismatch.
//  3. When both original and replay artifact hashes are non-empty and differ,
//     the check is a mismatch even if pass=true (hash divergence is suspicious).
//  4. Machine-readable numeric fields are compared conservatively: when the
//     relative delta of any numeric field exceeds 15%, an anomaly flag is added
//     but the check is NOT failed solely on this basis.
//
// Overall status derivation:
//
//	0 mismatches                    → "match"
//	0 < mismatches ≤ half of total  → "partial_match"
//	mismatches > half of total      → "mismatch"
type ComparisonExecutor struct{}

// Compare compares sub against the original claims in job and returns a
// ReplayOutcome. Callers (SubmissionProcessor) are responsible for validating
// binding before calling Compare; this method trusts that sub belongs to job.
func (e *ComparisonExecutor) Compare(job *ReplayJob, sub *ReplaySubmission) *ReplayOutcome {
	// Index submitted results by check type for O(1) lookup.
	submitted := make(map[string]*SubmittedCheckResult, len(sub.CheckResults))
	for i := range sub.CheckResults {
		submitted[sub.CheckResults[i].CheckType] = &sub.CheckResults[i]
	}

	// Index original artifact hashes by check type.
	originalHashes := make(map[string]string, len(job.ArtifactRefs))
	for _, ref := range job.ArtifactRefs {
		originalHashes[ref.CheckType] = ref.Hash
	}

	// Verify all required checks are present in the submission.
	var missingChecks []string
	for _, check := range job.ChecksToReplay {
		if _, ok := submitted[check]; !ok {
			missingChecks = append(missingChecks, check)
		}
	}
	if len(missingChecks) > 0 {
		return &ReplayOutcome{
			JobID:      job.ID,
			TaskID:     job.TaskID,
			Status:     "error",
			ReplayedAt: time.Now(),
			ReplayerID: sub.SubmitterID,
			Notes:      "missing required checks: " + strings.Join(missingChecks, ", "),
		}
	}

	// Perform per-check comparison.
	comparisons := make([]CheckComparison, 0, len(job.ChecksToReplay))
	var anomalyFlags []string

	for _, checkType := range job.ChecksToReplay {
		res := submitted[checkType]
		cmp := compareCheck(checkType, res, originalHashes[checkType])
		comparisons = append(comparisons, cmp)

		// Artifact hash mismatch with pass=true is a suspicious anomaly.
		if !cmp.Match && res.Pass &&
			res.ArtifactHash != "" && originalHashes[checkType] != "" {
			anomalyFlags = appendUnique(anomalyFlags, "artifact_hash_divergence")
		}

		// Numeric field comparison: informational anomaly, does not affect Match.
		if flag := compareNumericFields(checkType, job.MachineReadableResults, res.MachineReadableResult); flag != "" {
			anomalyFlags = appendUnique(anomalyFlags, flag)
		}
	}

	return &ReplayOutcome{
		JobID:        job.ID,
		TaskID:       job.TaskID,
		Status:       deriveStatus(comparisons),
		Comparisons:  comparisons,
		AnomalyFlags: anomalyFlags,
		ReplayedAt:   time.Now(),
		ReplayerID:   sub.SubmitterID,
	}
}

// compareCheck produces a CheckComparison for a single required check.
// The original claim is always pass=true; any deviation is recorded.
func compareCheck(checkType string, res *SubmittedCheckResult, originalHash string) CheckComparison {
	cmp := CheckComparison{
		CheckType:    checkType,
		OriginalHash: originalHash,
		ReplayedHash: res.ArtifactHash,
	}

	// Pass/fail: original claim is always pass=true.
	if !res.Pass {
		cmp.Match = false
		cmp.ScoreDelta = 1.0
		cmp.Detail = fmt.Sprintf(
			"check %q: original claimed pass=true, replay reports pass=false (exit_code=%d)",
			checkType, res.ExitCode,
		)
		return cmp
	}

	// Artifact hash: mismatch when both are non-empty and differ.
	if originalHash != "" && res.ArtifactHash != "" && originalHash != res.ArtifactHash {
		cmp.Match = false
		cmp.ScoreDelta = 0.5
		cmp.Detail = fmt.Sprintf(
			"check %q: artifact hash mismatch (original=%s, replay=%s)",
			checkType, originalHash, res.ArtifactHash,
		)
		return cmp
	}

	cmp.Match = true
	return cmp
}

// deriveStatus computes the overall outcome status from per-check comparisons.
//
//	0 mismatches              → "match"
//	mismatches > half total   → "mismatch"
//	otherwise                 → "partial_match"
func deriveStatus(comparisons []CheckComparison) string {
	if len(comparisons) == 0 {
		return "match"
	}
	mismatches := 0
	for _, c := range comparisons {
		if !c.Match {
			mismatches++
		}
	}
	if mismatches == 0 {
		return "match"
	}
	if mismatches*2 > len(comparisons) {
		return "mismatch"
	}
	return "partial_match"
}

// compareNumericFields returns an anomaly flag if a significant numeric
// divergence (>15% relative) is detected between original and replay
// machine-readable results for the given check type. Returns "" when no
// anomaly is detected or when fields are absent / non-numeric.
//
// Only top-level numeric values within the per-check result map are compared.
// This is deliberately conservative: a check is never failed solely on
// numeric divergence.
func compareNumericFields(checkType string, origMRR map[string]interface{}, replayFields map[string]interface{}) string {
	if len(origMRR) == 0 || len(replayFields) == 0 {
		return ""
	}
	origVal, ok := origMRR[checkType]
	if !ok {
		return ""
	}
	// Try to interpret the original per-check value as a map of named fields.
	origMap, ok := origVal.(map[string]interface{})
	if !ok {
		return ""
	}
	for key, ov := range origMap {
		rv, ok := replayFields[key]
		if !ok {
			continue
		}
		ovNum, ok1 := toFloat64(ov)
		rvNum, ok2 := toFloat64(rv)
		if !ok1 || !ok2 {
			continue
		}
		divisor := ovNum
		if divisor < 0 {
			divisor = -divisor
		}
		if divisor < 1.0 {
			divisor = 1.0
		}
		delta := rvNum - ovNum
		if delta < 0 {
			delta = -delta
		}
		if delta/divisor > 0.15 {
			return "numeric_divergence"
		}
	}
	return ""
}

// toFloat64 converts common numeric types to float64.
// After JSON round-trips all numbers arrive as float64; other types are
// supported for direct construction in tests.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// appendUnique appends s to slice only when s is not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
