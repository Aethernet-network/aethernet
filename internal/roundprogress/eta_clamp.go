package roundprogress

// ETA clamping bounds per phase. Hardcoded for v1; future versions will
// use observed p99 latencies from rolling windows.
const (
	maxETAFetchingBlobSec = 30
	maxETAAnalyzingSec    = 60
)

// ClampETA bounds a self-reported ETA to the protocol maximum for the
// given phase. Returns the clamped ETA (unix seconds).
//
// Rules:
//   - FetchingBlob: max now + 30s
//   - Analyzing: max now + 60s
//   - Zero ETA ("unknown") passes through as 0 (does not extend leases)
//   - Terminal phases and other phases: ETA is irrelevant, returns 0
func ClampETA(phase ProgressPhase, reportedETA int64, nowUnix int64) int64 {
	if reportedETA == 0 {
		return 0 // "unknown" — no ETA
	}

	switch phase {
	case ProgressPhaseFetchingBlob:
		maxETA := nowUnix + maxETAFetchingBlobSec
		if reportedETA > maxETA {
			return maxETA
		}
		return reportedETA

	case ProgressPhaseAnalyzing:
		maxETA := nowUnix + maxETAAnalyzingSec
		if reportedETA > maxETA {
			return maxETA
		}
		return reportedETA

	default:
		return 0
	}
}
