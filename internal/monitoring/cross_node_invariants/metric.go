package cross_node_invariants

import (
	"github.com/Aethernet-network/aethernet/internal/metrics"
)

// MetricName is the canonical name of the cross-node ledger divergence
// gauge. Matches the F4 Plan v2 §2.1.3 contract:
// "aethernet_cross_node_ledger_divergence — gauge reporting magnitude
// of observed divergence across known peers."
const MetricName = "aethernet_cross_node_ledger_divergence"

// MetricHelp is the Prometheus HELP string for the gauge. Co-located
// with the metric name so callers wire identical text.
const MetricHelp = "Magnitude (in microAET) of observed cross-node ledger divergence; sum of absolute per-agent + per-escrow + treasury deltas across reachable peers. Observation only — does not participate in canonical state."

// RegisterGauge attaches the standard divergence gauge to the given
// internal/metrics Registry and returns it as a MetricSink suitable
// for NewMonitor. The Registry's existing /metrics-endpoint plumbing
// publishes the gauge automatically.
//
// This indirection lets the package depend only on
// internal/metrics (already a project dependency) rather than on
// github.com/prometheus/client_golang. The gauge appears in the
// /metrics endpoint's Prometheus text exposition format identically to
// a client_golang gauge.
func RegisterGauge(reg *metrics.Registry) MetricSink {
	if reg == nil {
		return nil
	}
	g := reg.Gauge(MetricName, MetricHelp)
	return gaugeAdapter{g: g}
}

// gaugeAdapter implements MetricSink atop *metrics.Gauge.
type gaugeAdapter struct {
	g *metrics.Gauge
}

func (a gaugeAdapter) Set(v int64) {
	if a.g == nil {
		return
	}
	a.g.Set(v)
}
