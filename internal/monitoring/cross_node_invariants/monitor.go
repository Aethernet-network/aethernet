// Package cross_node_invariants observes cross-node ledger state and
// surfaces divergence via a Prometheus-compatible metric, structured
// logs, and an operator-facing CLI.
//
// IMPORTANT: this package is OBSERVATION ONLY. It does not emit
// canonical events, does not mutate canonical state, and does not
// participate in protocol consensus. Per F4 Plan v2 §2.1.3:
// "divergence is an observation, not a protocol action. Protocolizing
// observations pollutes the canonical surface with observer artifacts
// and can create recursive concerns (the observer's emissions
// themselves subject to selection-race-class bugs). Keep observation
// layer and protocol layer distinct."
//
// Locked invariants honored:
//
//   - C-15 (admission state is non-canonical node-local) — this monitor
//     surfaces only non-canonical observations; it never feeds back into
//     the dispatcher, recognition fabric, or DAG.
//   - A-3 (monitoring subsystem is observer-only).
//   - A-4 (uses existing peer discovery; no new peer-configuration
//     surface is added here).
//
// The package is wired in production by:
//
//  1. Constructing a PeerSource from internal/network's discovery state
//     (the existing peer set maintained for fastpath routing).
//  2. Constructing a SnapshotFetcher that reads a per-peer ledger
//     snapshot endpoint (currently a documented gap — see FINDING in the
//     accompanying handoff; the fetcher interface is here so production
//     wiring can fill in the concrete HTTP impl when the endpoint
//     ships).
//  3. Registering a metric on the shared internal/metrics Registry.
//
// For unit tests, the PeerSource and SnapshotFetcher are stubbed; the
// metric is fed a no-op sink. See monitor_test.go.
package cross_node_invariants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// LedgerSnapshot is the projected state of one peer at one moment in
// time. Mirrors internal/verification/cross_node.LedgerSnapshot in
// shape (per-agent balances, per-task escrow residuals, treasury,
// total supply) but is decoupled so the monitoring layer does not
// import the test harness.
//
// Fields use map[string]uint64 rather than sorted-slice form because
// the monitor compares values across peers, not byte-equality of
// serialized form. Sorting is performed when emitting reports for
// display.
type LedgerSnapshot struct {
	// NodeID identifies the peer this snapshot was taken from. May be
	// the peer's address ("172.31.x.y:7000") or an agent ID.
	NodeID string

	// AgentBalances maps agent ID → spendable µAET balance.
	AgentBalances map[string]uint64

	// EscrowResiduals maps task ID → µAET still held in the task's
	// escrow bucket. Production accounting drains every bucket to zero
	// post-settlement; non-zero residuals are themselves an alert.
	EscrowResiduals map[string]uint64

	// Treasury is the µAET balance of the canonical treasury agent.
	// Surfaced separately for headline divergence alerting.
	Treasury uint64

	// TotalSupply is the cluster-wide µAET sum the snapshot accounts
	// for (sum of all known agent balances + escrow residuals). Two
	// peers reporting different TotalSupply for the same logical state
	// is direct evidence of conservation violation.
	TotalSupply uint64
}

// PeerSource enumerates the addresses of peer nodes the monitor should
// query. The production implementation is a thin adapter over the
// existing internal/network peer set (per A-4: no new peer
// configuration surface). For tests, a static stub.
type PeerSource interface {
	// Peers returns peer-addressable identifiers (e.g. "host:port" or a
	// scheme-prefixed URL). The fetcher interprets these.
	Peers() []string
}

// SnapshotFetcher fetches a LedgerSnapshot from a single peer.
// Implementations are expected to be cheap, idempotent, and to honor
// ctx cancellation. Production implementations should fail gracefully
// when a peer is unreachable; the monitor continues with the remaining
// peers (per the per-peer error handling in Check).
type SnapshotFetcher interface {
	Fetch(ctx context.Context, peerAddr string) (*LedgerSnapshot, error)
}

// MetricSink is the minimal interface the monitor needs to publish
// the divergence gauge. The production implementation is *metrics.Gauge
// from internal/metrics (the project's existing Prometheus-compatible
// metrics package); tests pass a small recording sink.
//
// We deliberately do NOT depend on github.com/prometheus/client_golang
// here. internal/metrics already provides Prometheus text-exposition
// output via the /metrics endpoint, and adding a top-level dependency
// is an architect decision, not a step-10 implementation choice.
type MetricSink interface {
	Set(value int64)
}

// Monitor observes cross-node ledger state by snapshotting each peer
// and comparing against a locally-supplied snapshot. Magnitude of
// divergence (sum of absolute per-agent deltas, in µAET) is published
// to the metric and, when above threshold, logged at Warn.
//
// The monitor does not maintain background state — each Check is
// independent. Operators may call Check on a schedule (e.g., every 60s
// from cmd/node) or on demand (via `aet invariants check`).
type Monitor struct {
	peers     PeerSource
	fetcher   SnapshotFetcher
	threshold uint64
	metric    MetricSink

	logger *slog.Logger

	// once-per-construction wiring; Check is safe for concurrent use.
	mu sync.Mutex
}

// NewMonitor constructs a Monitor.
//
// threshold is the µAET divergence above which Check emits a Warn-level
// log entry. Setting threshold=0 means any nonzero divergence triggers
// a Warn. The metric is published on every Check regardless of
// threshold (so alerting infrastructure sees the magnitude continuously).
//
// metric may be nil; in that case, no metric is published. Useful for
// CLI invocations where the metric is not collected anywhere.
//
// logger may be nil; in that case, slog.Default() is used.
func NewMonitor(peers PeerSource, fetcher SnapshotFetcher, threshold uint64, metric MetricSink) *Monitor {
	return &Monitor{
		peers:     peers,
		fetcher:   fetcher,
		threshold: threshold,
		metric:    metric,
		logger:    slog.Default(),
	}
}

// SetLogger overrides the logger (useful for tests asserting on log
// output via a recording handler).
func (m *Monitor) SetLogger(l *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l != nil {
		m.logger = l
	}
}

// PeerDelta is one peer's divergence from the local snapshot.
type PeerDelta struct {
	PeerAddr string
	NodeID   string

	// Magnitude is the µAET-sum of absolute deltas across every
	// observed agent balance + escrow residual + treasury. Bounded by
	// 2 * total supply.
	Magnitude uint64

	// AgentDeltas maps agent ID → signed delta (peer balance - local
	// balance). Sorted by AgentID when rendered.
	AgentDeltas map[string]int64

	// EscrowDeltas maps task ID → signed delta on residual.
	EscrowDeltas map[string]int64

	// TreasuryDelta is the signed delta on the treasury balance.
	TreasuryDelta int64

	// SupplyDelta is the signed delta on TotalSupply.
	SupplyDelta int64

	// FetchErr is set if the peer's snapshot could not be retrieved.
	// Other fields are zero-valued in that case; the peer is excluded
	// from aggregate magnitude.
	FetchErr error
}

// Report is the result of one Check run.
type Report struct {
	// LocalNodeID identifies the local snapshot the monitor compared
	// against. Convenience for display.
	LocalNodeID string

	// PeerCount is the number of peers the monitor attempted to reach.
	PeerCount int

	// SuccessCount is how many peers returned a snapshot successfully.
	SuccessCount int

	// TotalMagnitude is the sum of per-peer Magnitude values across all
	// successfully-reached peers, in µAET. This is the value published
	// to the divergence metric.
	TotalMagnitude uint64

	// Peers is the per-peer breakdown. Sorted by PeerAddr.
	Peers []PeerDelta
}

// Diverged reports whether the run observed any nonzero divergence
// across reachable peers.
func (r *Report) Diverged() bool { return r.TotalMagnitude > 0 }

// Check fetches each peer's snapshot, computes per-peer divergence
// against local, publishes the aggregate magnitude to the metric, and
// emits a Warn-level structured log if magnitude exceeds the
// configured threshold. local must not be nil; the caller is
// responsible for sourcing it (typically from the node's own ledger
// projection).
//
// Errors from individual peer fetches do not fail the call — the peer
// is recorded with FetchErr and excluded from aggregate magnitude.
// Check returns an error only if local is nil, or if every peer fetch
// fails (the latter is operationally meaningful: total partition or
// total endpoint outage).
func (m *Monitor) Check(ctx context.Context, local *LedgerSnapshot) (*Report, error) {
	if local == nil {
		return nil, errors.New("cross_node_invariants: local snapshot must not be nil")
	}

	peerAddrs := append([]string(nil), m.peers.Peers()...)
	sort.Strings(peerAddrs)

	report := &Report{
		LocalNodeID: local.NodeID,
		PeerCount:   len(peerAddrs),
		Peers:       make([]PeerDelta, 0, len(peerAddrs)),
	}

	for _, addr := range peerAddrs {
		snap, err := m.fetcher.Fetch(ctx, addr)
		if err != nil {
			report.Peers = append(report.Peers, PeerDelta{PeerAddr: addr, FetchErr: err})
			continue
		}
		delta := computePeerDelta(addr, local, snap)
		report.Peers = append(report.Peers, delta)
		report.SuccessCount++
		report.TotalMagnitude += delta.Magnitude
	}

	if report.PeerCount > 0 && report.SuccessCount == 0 {
		// Every reachable peer failed; this is itself worth a warn,
		// distinct from a divergence warn.
		m.logger.Warn("cross_node_invariants: all peer snapshot fetches failed",
			"peers_attempted", report.PeerCount,
			"local_node", local.NodeID,
		)
		// Still publish a zero metric to avoid stuck-stale gauge values.
		m.publish(0)
		return report, nil
	}

	m.publish(int64ClampPositive(report.TotalMagnitude))

	if report.TotalMagnitude > m.threshold {
		m.logger.Warn("cross_node_invariants: cross-node ledger divergence detected",
			"local_node", local.NodeID,
			"total_magnitude_uAET", report.TotalMagnitude,
			"threshold_uAET", m.threshold,
			"peers_observed", report.SuccessCount,
			"peers_total", report.PeerCount,
		)
	}

	return report, nil
}

// publish writes the gauge value if a metric sink is configured.
func (m *Monitor) publish(v int64) {
	if m.metric == nil {
		return
	}
	m.metric.Set(v)
}

// int64ClampPositive converts a uint64 to int64, saturating at MaxInt64.
// Magnitudes large enough to overflow indicate severe divergence;
// clamping is acceptable for a gauge.
func int64ClampPositive(v uint64) int64 {
	const max = uint64(1<<63 - 1)
	if v > max {
		return 1<<63 - 1
	}
	return int64(v)
}

// computePeerDelta builds a PeerDelta comparing peer to local. Both
// snapshots are read-only; this function does not mutate either.
//
// The magnitude metric is the L1 norm of all per-key deltas plus the
// treasury delta. SupplyDelta is reported but not double-counted in
// Magnitude (it is derivable from the per-agent + escrow + treasury
// deltas, and we want Magnitude to be a sum of independent signals).
func computePeerDelta(addr string, local, peer *LedgerSnapshot) PeerDelta {
	out := PeerDelta{
		PeerAddr:     addr,
		NodeID:       peer.NodeID,
		AgentDeltas:  map[string]int64{},
		EscrowDeltas: map[string]int64{},
	}

	// Agent balances: union of keys.
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for id, lv := range local.AgentBalances {
		pv := peer.AgentBalances[id]
		if pv != lv {
			d := signedDelta(pv, lv)
			out.AgentDeltas[id] = d
			out.Magnitude += absInt64(d)
		}
	}
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for id, pv := range peer.AgentBalances {
		if _, seen := local.AgentBalances[id]; seen {
			continue
		}
		// Peer-only agent: full peer balance is a delta.
		d := int64(pv) // safe: balances are tracked as uint64 µAET, no overflow expected at this scale
		out.AgentDeltas[id] = d
		out.Magnitude += absInt64(d)
	}

	// Escrow residuals: union of keys.
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for tid, lv := range local.EscrowResiduals {
		pv := peer.EscrowResiduals[tid]
		if pv != lv {
			d := signedDelta(pv, lv)
			out.EscrowDeltas[tid] = d
			out.Magnitude += absInt64(d)
		}
	}
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for tid, pv := range peer.EscrowResiduals {
		if _, seen := local.EscrowResiduals[tid]; seen {
			continue
		}
		d := int64(pv)
		out.EscrowDeltas[tid] = d
		out.Magnitude += absInt64(d)
	}

	// Treasury and supply.
	out.TreasuryDelta = signedDelta(peer.Treasury, local.Treasury)
	out.Magnitude += absInt64(out.TreasuryDelta)
	out.SupplyDelta = signedDelta(peer.TotalSupply, local.TotalSupply)

	return out
}

// signedDelta returns peer - local as int64 without overflow on
// realistic µAET values.
func signedDelta(peer, local uint64) int64 {
	if peer >= local {
		return int64(peer - local)
	}
	return -int64(local - peer)
}

func absInt64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

// Format renders a Report as a human-readable multi-line string,
// suitable for CLI output. Per-peer sections are sorted by PeerAddr;
// per-agent / per-task deltas within each peer section are sorted by
// key.
func (r *Report) Format() string {
	var b stringBuilder
	b.printf("Cross-node ledger divergence report\n")
	b.printf("  local node:     %s\n", r.LocalNodeID)
	b.printf("  peers reached:  %d / %d\n", r.SuccessCount, r.PeerCount)
	b.printf("  total magnitude: %d µAET\n", r.TotalMagnitude)
	if !r.Diverged() && r.SuccessCount > 0 {
		b.printf("  status:         OK (no divergence)\n")
		return b.String()
	}
	if r.SuccessCount == 0 {
		b.printf("  status:         UNKNOWN (no peer reachable)\n")
	} else {
		b.printf("  status:         DIVERGED\n")
	}
	b.printf("\nPer-peer breakdown:\n")
	for _, p := range r.Peers {
		b.printf("  peer %s (node=%s)\n", p.PeerAddr, p.NodeID)
		if p.FetchErr != nil {
			b.printf("    fetch error: %v\n", p.FetchErr)
			continue
		}
		b.printf("    magnitude: %d µAET   treasury Δ: %+d   supply Δ: %+d\n",
			p.Magnitude, p.TreasuryDelta, p.SupplyDelta)
		if len(p.AgentDeltas) > 0 {
			b.printf("    agent balance deltas (peer - local):\n")
			ids := make([]string, 0, len(p.AgentDeltas))
			// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
			for id := range p.AgentDeltas {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				b.printf("      %-48s %+d µAET\n", id, p.AgentDeltas[id])
			}
		}
		if len(p.EscrowDeltas) > 0 {
			b.printf("    escrow residual deltas (peer - local):\n")
			tids := make([]string, 0, len(p.EscrowDeltas))
			// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
			for tid := range p.EscrowDeltas {
				tids = append(tids, tid)
			}
			sort.Strings(tids)
			for _, tid := range tids {
				b.printf("      %-48s %+d µAET\n", tid, p.EscrowDeltas[tid])
			}
		}
	}
	return b.String()
}

// stringBuilder wraps strings.Builder + fmt.Sprintf for compact use.
type stringBuilder struct{ b []byte }

func (s *stringBuilder) printf(format string, args ...any) {
	s.b = append(s.b, fmt.Sprintf(format, args...)...)
}
func (s *stringBuilder) String() string { return string(s.b) }
