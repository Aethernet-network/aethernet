package taskverification

import "github.com/Aethernet-network/aethernet/internal/metrics"

// VerificationMetrics holds counters, gauges, and histograms for the
// multi-validator verification pipeline.
type VerificationMetrics struct {
	// Round lifecycle
	RoundsOpened           *metrics.Counter
	RoundsFinalizedAccept  *metrics.Counter
	RoundsFinalizedReject  *metrics.Counter
	RoundsFinalizedDispute *metrics.Counter
	RoundsExtended         *metrics.Counter
	RoundsExpired          *metrics.Counter
	RoundsOpen             *metrics.Gauge

	// Votes
	VotesEmitted          *metrics.Counter
	VotesApplied          *metrics.Counter
	VotesDuplicate        *metrics.Counter
	VotesEquivocation     *metrics.Counter
	VotesPostFinalization *metrics.Counter

	// Analyzers
	AnalyzerErrors   *metrics.Counter
	AnalyzerTimeouts *metrics.Counter

	// Durations
	RoundDuration    *metrics.Histogram
	AnalyzerDuration *metrics.Histogram
}

// NewVerificationMetrics registers verification metrics with the given registry.
func NewVerificationMetrics(reg *metrics.Registry) *VerificationMetrics {
	if reg == nil {
		return nil
	}
	latencyBuckets := []float64{100, 500, 1000, 5000, 10000, 30000, 60000}
	analyzerBuckets := []float64{10, 50, 100, 500, 1000, 5000, 10000, 30000}

	return &VerificationMetrics{
		RoundsOpened:           reg.Counter("verification_rounds_opened_total", "Total verification rounds opened"),
		RoundsFinalizedAccept:  reg.Counter("verification_rounds_finalized_accept_total", "Rounds finalized as accept"),
		RoundsFinalizedReject:  reg.Counter("verification_rounds_finalized_reject_total", "Rounds finalized as reject"),
		RoundsFinalizedDispute: reg.Counter("verification_rounds_finalized_dispute_total", "Rounds finalized as dispute"),
		RoundsExtended:         reg.Counter("verification_rounds_extended_total", "Rounds extended once"),
		RoundsExpired:          reg.Counter("verification_rounds_expired_total", "Rounds expired without resolution"),
		RoundsOpen:             reg.Gauge("verification_rounds_open", "Current open verification rounds"),

		VotesEmitted:          reg.Counter("verification_votes_emitted_total", "Total votes emitted by this validator"),
		VotesApplied:          reg.Counter("verification_votes_applied_total", "Total votes applied to rounds"),
		VotesDuplicate:        reg.Counter("verification_votes_duplicate_total", "Duplicate votes detected"),
		VotesEquivocation:     reg.Counter("verification_votes_equivocation_total", "Equivocation attempts detected"),
		VotesPostFinalization: reg.Counter("verification_votes_post_finalization_total", "Votes recorded post-finalization"),

		AnalyzerErrors:   reg.Counter("verification_analyzer_errors_total", "Analyzer execution errors"),
		AnalyzerTimeouts: reg.Counter("verification_analyzer_timeouts_total", "Analyzer timeouts"),

		RoundDuration:    reg.Histogram("verification_round_duration_ms", "Round open-to-finalized duration in ms", latencyBuckets),
		AnalyzerDuration: reg.Histogram("verification_analyzer_duration_ms", "Analyzer execution time in ms", analyzerBuckets),
	}
}
