// Package validator — assignment.go
//
// AssignmentEngine selects verification nodes for OCS tasks using a
// weighted-random draw. Weights are the product of three factors:
//
//  1. Calibration modifier (0.7 / 1.0 / 1.2) — based on the validator's
//     historical accuracy for the task's category.
//  2. Probation modifier (0.3 / 1.0) — penalises new entrants still in the
//     probation phase.
//  3. Cap enforcement — validators that have received too large a share of
//     category assignments in the current epoch are excluded from the draw.
//
// An affiliated-cluster detection layer tracks pairwise agreement rates and
// groups validators that suspiciously agree on every decision, then treats
// the cluster as a single unit for cap purposes.
package validator

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Calibration interface (L2-local — does not import L3 canary directly)
// ---------------------------------------------------------------------------

// CalibrationData holds the per-actor per-category calibration snapshot that
// the AssignmentEngine uses to compute weight modifiers. It is exported so
// cmd/node can construct it from *canary.CanaryManager without creating an
// import-cycle.
type CalibrationData struct {
	TotalSignals int
	Accuracy     float64
	AvgSeverity  float64
}

// calibrationSource is the minimal interface required by AssignmentEngine to
// fetch per-validator calibration metrics. The cmd/node adapter satisfies
// this with a thin wrapper around *canary.CanaryManager.
type calibrationSource interface {
	CategoryCalibrationForActor(agentID string, category string) (*CalibrationData, error)
}

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

// pairwiseRecord tracks the agreement history between two validators.
type pairwiseRecord struct {
	SharedTasks int
	Agreements  int
	LastUpdated time.Time
}

// agreementRate returns Agreements / SharedTasks, or 0 if SharedTasks == 0.
func (p *pairwiseRecord) agreementRate() float64 {
	if p.SharedTasks == 0 {
		return 0
	}
	return float64(p.Agreements) / float64(p.SharedTasks)
}

// ---------------------------------------------------------------------------
// AssignmentEngine
// ---------------------------------------------------------------------------

// AssignmentEngine selects validators for verification work using weighted
// random draws with calibration, probation, and cap modifiers.
//
// It is safe for concurrent use by multiple goroutines.
type AssignmentEngine struct {
	registry    *ValidatorRegistry
	calibration calibrationSource
	cfg         *config.ValidatorConfig

	mu sync.Mutex

	// assignmentCount[category][validatorID] = count in current epoch.
	assignmentCount map[string]map[string]int
	epochStart      time.Time

	// clusters[validatorID] = clusterID.
	clusters map[string]string

	// pairwise[minID][maxID] = *pairwiseRecord.
	pairwise map[string]map[string]*pairwiseRecord

	rng *rand.Rand
}

// NewAssignmentEngine creates an AssignmentEngine backed by registry and cfg.
// cfg must not be nil. SetCalibrationSource may be called before use.
func NewAssignmentEngine(registry *ValidatorRegistry, cfg *config.ValidatorConfig) *AssignmentEngine {
	return &AssignmentEngine{
		registry:        registry,
		cfg:             cfg,
		assignmentCount: make(map[string]map[string]int),
		epochStart:      time.Now(),
		clusters:        make(map[string]string),
		pairwise:        make(map[string]map[string]*pairwiseRecord),
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetCalibrationSource wires an optional calibration data provider.
// When nil (the default), all validators receive the moderate weight.
func (e *AssignmentEngine) SetCalibrationSource(cs calibrationSource) {
	e.mu.Lock()
	e.calibration = cs
	e.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Epoch management
// ---------------------------------------------------------------------------

// maybeResetEpoch resets assignment counts when the current epoch has expired.
// Must be called with e.mu held.
func (e *AssignmentEngine) maybeResetEpoch() {
	epochDur := time.Duration(e.cfg.CapEpochHours) * time.Hour
	if time.Since(e.epochStart) >= epochDur {
		e.assignmentCount = make(map[string]map[string]int)
		e.epochStart = time.Now()
	}
}

// categoryTotal returns the total assignments in this epoch for category.
// Must be called with e.mu held.
func (e *AssignmentEngine) categoryTotal(category string) int {
	total := 0
	for _, cnt := range e.assignmentCount[category] {
		total += cnt
	}
	return total
}

// clusterTotal returns the sum of all assignment counts for the cluster that
// validatorID belongs to. If the validator has no cluster it returns only its
// own count. Must be called with e.mu held.
func (e *AssignmentEngine) clusterTotal(category string, validatorID string) int {
	clusterID, hasCluster := e.clusters[validatorID]
	if !hasCluster {
		return e.assignmentCount[category][validatorID]
	}
	total := 0
	for vid, cid := range e.clusters {
		if cid == clusterID {
			total += e.assignmentCount[category][vid]
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// Calibration modifier
// ---------------------------------------------------------------------------

// getCalibrationModifier returns the weight multiplier for validator based on
// its historical accuracy in category. Returns moderate (1.0) when there is no
// calibration source or insufficient data.
func (e *AssignmentEngine) getCalibrationModifier(validator *Validator, category string) float64 {
	// Minimum signals threshold borrowed from the canary subsystem (20 signals).
	const minCalibrationSignals = 20

	if e.calibration == nil {
		return e.cfg.AssignmentCalibrationModerate
	}
	cal, err := e.calibration.CategoryCalibrationForActor(validator.AgentID, category)
	if err != nil || cal == nil || cal.TotalSignals < minCalibrationSignals {
		return e.cfg.AssignmentCalibrationModerate
	}
	// Calibration strong/weak thresholds mirror the router calibration config.
	const strongThreshold = 0.9
	const weakThreshold = 0.6
	switch {
	case cal.Accuracy >= strongThreshold:
		return e.cfg.AssignmentCalibrationStrong
	case cal.Accuracy < weakThreshold:
		return e.cfg.AssignmentCalibrationWeak
	default:
		return e.cfg.AssignmentCalibrationModerate
	}
}

// ---------------------------------------------------------------------------
// SelectValidator
// ---------------------------------------------------------------------------

// ErrNoEligibleValidators is returned when no validator passes all selection
// filters for the given category.
var ErrNoEligibleValidators = errors.New("validator: no eligible validators available for assignment")

// SelectValidator picks a validator for a verification task using weighted
// random selection. excludeIDs are skipped unconditionally (used for replay
// independence — pass the original verifier's ID and any known cluster mates).
// isStructured is unused in the selection itself but is forwarded to callers
// that need it for cluster threshold decisions.
func (e *AssignmentEngine) SelectValidator(category string, excludeIDs []string, _ bool) (*Validator, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.maybeResetEpoch()

	// Build exclude set.
	excluded := make(map[string]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excluded[id] = true
	}

	// Expand exclusions: also exclude all cluster mates of any excluded validator.
	for excludedID := range excluded {
		clusterID, ok := e.clusters[excludedID]
		if !ok {
			continue
		}
		for vid, cid := range e.clusters {
			if cid == clusterID {
				excluded[vid] = true
			}
		}
	}

	// Get candidate validators from registry.
	var candidates []*Validator
	if category == "" {
		candidates = e.registry.ActiveValidators()
	} else {
		candidates = e.registry.ActiveValidatorsForCategory(category)
	}

	// Total eligible count (for cap computation).
	totalEligible := e.registry.ActiveEligibleCount()
	applyCAP := totalEligible >= e.cfg.CapEnforcementMinValidators
	capFraction := e.cfg.CapBelow10Validators
	if totalEligible >= 10 {
		capFraction = e.cfg.CapAtOrAbove10Validators
	}
	catTotal := e.categoryTotal(category)

	type weightedCandidate struct {
		v      *Validator
		weight float64
	}
	var pool []weightedCandidate

	for _, v := range candidates {
		if excluded[v.ID] {
			continue
		}

		// Cap enforcement: skip if this validator (or its cluster) has consumed
		// more than capFraction of the epoch assignments.
		if applyCAP && catTotal > 0 {
			share := float64(e.clusterTotal(category, v.ID)) / float64(catTotal)
			if share > capFraction {
				continue
			}
		}

		// Compute weight.
		calMod := e.getCalibrationModifier(v, category)
		probMod := 1.0
		if v.Status == StatusProbationary {
			probMod = e.cfg.AssignmentProbationModifier
		}
		weight := calMod * probMod
		if weight <= 0 {
			continue
		}
		pool = append(pool, weightedCandidate{v: v, weight: weight})
	}

	if len(pool) == 0 {
		return nil, ErrNoEligibleValidators
	}

	// Weighted random draw.
	totalWeight := 0.0
	for _, c := range pool {
		totalWeight += c.weight
	}
	pick := e.rng.Float64() * totalWeight
	var selected *Validator
	for _, c := range pool {
		pick -= c.weight
		if pick <= 0 {
			selected = c.v
			break
		}
	}
	if selected == nil {
		// Floating-point edge case: pick the last candidate.
		selected = pool[len(pool)-1].v
	}

	// Increment assignment count.
	if e.assignmentCount[category] == nil {
		e.assignmentCount[category] = make(map[string]int)
	}
	e.assignmentCount[category][selected.ID]++

	// Return a copy to avoid external mutation.
	cp := *selected
	return &cp, nil
}

// ---------------------------------------------------------------------------
// Pairwise agreement tracking
// ---------------------------------------------------------------------------

// pairKey returns the canonical (sorted) key pair for two validator IDs,
// ensuring each unique pair is stored exactly once.
func pairKey(id1, id2 string) (string, string) {
	if id1 <= id2 {
		return id1, id2
	}
	return id2, id1
}

// RecordAgreement records a co-verification outcome between two validators.
// agreed is true when both validators produced the same verdict.
func (e *AssignmentEngine) RecordAgreement(validatorID1, validatorID2 string, agreed bool) {
	k1, k2 := pairKey(validatorID1, validatorID2)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.pairwise[k1] == nil {
		e.pairwise[k1] = make(map[string]*pairwiseRecord)
	}
	rec := e.pairwise[k1][k2]
	if rec == nil {
		rec = &pairwiseRecord{}
		e.pairwise[k1][k2] = rec
	}
	rec.SharedTasks++
	if agreed {
		rec.Agreements++
	}
	rec.LastUpdated = time.Now()
}

// ---------------------------------------------------------------------------
// Cluster detection
// ---------------------------------------------------------------------------

// CheckPairwiseClusters scans all pairwise records, flags pairs whose
// agreement rate exceeds the threshold, and applies transitive closure to
// form clusters. The clusters map on the engine is updated in place.
//
// isStructured selects the deterministic (higher) or non-deterministic
// threshold.
//
// Returns a list of clusters; each cluster is a slice of validator IDs.
func (e *AssignmentEngine) CheckPairwiseClusters(isStructured bool) [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()

	threshold := e.cfg.ClusterPairwiseThresholdNonDeterministic
	if isStructured {
		threshold = e.cfg.ClusterPairwiseThresholdDeterministic
	}

	// Union-Find data structure for transitive closure.
	parent := make(map[string]string)

	var find func(x string) string
	find = func(x string) string {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x]) // path compression
		}
		return parent[x]
	}
	union := func(x, y string) {
		rx, ry := find(x), find(y)
		if rx != ry {
			parent[rx] = ry
		}
	}

	// Ensure all known validators are in the parent map.
	for k1, inner := range e.pairwise {
		find(k1)
		for k2 := range inner {
			find(k2)
		}
	}

	// Flag pairs above threshold.
	for k1, inner := range e.pairwise {
		for k2, rec := range inner {
			if rec.SharedTasks < e.cfg.ClusterPairwiseMinShared {
				continue
			}
			if rec.agreementRate() > threshold {
				union(k1, k2)
			}
		}
	}

	// Build cluster groups.
	groups := make(map[string][]string)
	for vid := range parent {
		root := find(vid)
		groups[root] = append(groups[root], vid)
	}

	// Assign stable cluster IDs (use root ID as cluster ID).
	// Clear old assignments first.
	newClusters := make(map[string]string)
	var result [][]string
	for root, members := range groups {
		if len(members) < 2 {
			continue // singleton — not a cluster
		}
		clusterID := "cluster:" + root
		for _, vid := range members {
			newClusters[vid] = clusterID
		}
		cp := make([]string, len(members))
		copy(cp, members)
		result = append(result, cp)
	}
	e.clusters = newClusters
	return result
}

// ---------------------------------------------------------------------------
// SelectReplayExecutor
// ---------------------------------------------------------------------------

// SelectReplayExecutor selects a replay executor that is independent from the
// original verifier and their cluster. It calls SelectValidator with the
// original verifier's ID (and all their cluster mates) added to excludeIDs.
func (e *AssignmentEngine) SelectReplayExecutor(category string, originalVerifierID string, originalVerifierCluster string) (*Validator, error) {
	e.mu.Lock()
	// Build exclude list: original verifier + all in their cluster.
	excludeIDs := []string{originalVerifierID}
	clusterID := e.clusters[originalVerifierID]
	// Also honour an externally provided cluster ID.
	if originalVerifierCluster != "" && clusterID == "" {
		clusterID = originalVerifierCluster
	}
	if clusterID != "" {
		for vid, cid := range e.clusters {
			if cid == clusterID && vid != originalVerifierID {
				excludeIDs = append(excludeIDs, vid)
			}
		}
	}
	e.mu.Unlock()

	return e.SelectValidator(category, excludeIDs, false)
}

// ---------------------------------------------------------------------------
// Epoch inspection (for tests)
// ---------------------------------------------------------------------------

// AssignmentCount returns the current epoch assignment count for the given
// category and validator ID. Zero is returned if no assignments have been made.
func (e *AssignmentEngine) AssignmentCount(category, validatorID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.assignmentCount[category][validatorID]
}

// EpochStart returns the time the current epoch began.
func (e *AssignmentEngine) EpochStart() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochStart
}

// ClusterOf returns the clusterID for the given validator ID, or "" if none.
func (e *AssignmentEngine) ClusterOf(validatorID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clusters[validatorID]
}

// PairwiseRecord returns a copy of the pairwiseRecord for the two validators,
// or nil if no record exists.
func (e *AssignmentEngine) PairwiseRecord(id1, id2 string) *pairwiseRecord {
	k1, k2 := pairKey(id1, id2)
	e.mu.Lock()
	defer e.mu.Unlock()
	inner, ok := e.pairwise[k1]
	if !ok {
		return nil
	}
	rec := inner[k2]
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

// forceEpochStart overrides epochStart for testing purposes.
func (e *AssignmentEngine) forceEpochStart(t time.Time) {
	e.mu.Lock()
	e.epochStart = t
	e.mu.Unlock()
}

// AssignmentCounts returns a snapshot of the full assignment count map for
// the given category (for testing).
func (e *AssignmentEngine) AssignmentCounts(category string) map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.assignmentCount[category]
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// resetEpochNow forces an immediate epoch reset (for testing).
func (e *AssignmentEngine) resetEpochNow() {
	e.mu.Lock()
	e.assignmentCount = make(map[string]map[string]int)
	e.epochStart = time.Now()
	e.mu.Unlock()
}

// formatError wraps fmt.Errorf for sentinel error composition.
func formatError(msg string) error { return fmt.Errorf("%s", msg) }
