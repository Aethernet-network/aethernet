package cross_node_invariants

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/metrics"
)

// stubPeerSource backs a PeerSource with a fixed peer list.
type stubPeerSource struct{ addrs []string }

func (s *stubPeerSource) Peers() []string { return append([]string(nil), s.addrs...) }

// stubFetcher returns canned snapshots per peer address. If the
// snapshot for an address is missing, it returns the configured error
// (if any) or a generic "not configured" error. Concurrency-safe.
type stubFetcher struct {
	mu        sync.Mutex
	snapshots map[string]*LedgerSnapshot
	errors    map[string]error
	calls     int32
}

func newStubFetcher() *stubFetcher {
	return &stubFetcher{
		snapshots: map[string]*LedgerSnapshot{},
		errors:    map[string]error{},
	}
}

func (f *stubFetcher) setSnapshot(addr string, s *LedgerSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[addr] = s
}

func (f *stubFetcher) setError(addr string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors[addr] = err
}

func (f *stubFetcher) Fetch(ctx context.Context, addr string) (*LedgerSnapshot, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errors[addr]; ok {
		return nil, err
	}
	if s, ok := f.snapshots[addr]; ok {
		return s, nil
	}
	return nil, errors.New("stub: no snapshot configured")
}

// recordingSink captures gauge values published via MetricSink.
type recordingSink struct {
	mu     sync.Mutex
	values []int64
}

func (r *recordingSink) Set(v int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, v)
}

func (r *recordingSink) last() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return 0, false
	}
	return r.values[len(r.values)-1], true
}

// recordingHandler captures slog records for assertions on log output.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(name string) slog.Handler       { return h }

func (h *recordingHandler) warnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			n++
		}
	}
	return n
}

func (h *recordingHandler) hasWarnContaining(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// makeSnapshot is a small helper for fixture readability.
func makeSnapshot(nodeID string, balances map[string]uint64, escrows map[string]uint64, treasury uint64) *LedgerSnapshot {
	supply := treasury
	for _, v := range balances {
		supply += v
	}
	for _, v := range escrows {
		supply += v
	}
	return &LedgerSnapshot{
		NodeID:          nodeID,
		AgentBalances:   balances,
		EscrowResiduals: escrows,
		Treasury:        treasury,
		TotalSupply:     supply,
	}
}

// ----- Tests --------------------------------------------------------

// TestMonitor_NoDivergence — every peer matches local; magnitude=0,
// no warn log emitted, gauge published at 0.
func TestMonitor_NoDivergence(t *testing.T) {
	local := makeSnapshot("local",
		map[string]uint64{"alice": 1_000_000, "bob": 500_000},
		map[string]uint64{"task-1": 0},
		10_000,
	)
	peers := &stubPeerSource{addrs: []string{"peer-a", "peer-b"}}
	fetcher := newStubFetcher()
	fetcher.setSnapshot("peer-a", makeSnapshot("peer-a",
		map[string]uint64{"alice": 1_000_000, "bob": 500_000},
		map[string]uint64{"task-1": 0},
		10_000,
	))
	fetcher.setSnapshot("peer-b", makeSnapshot("peer-b",
		map[string]uint64{"alice": 1_000_000, "bob": 500_000},
		map[string]uint64{"task-1": 0},
		10_000,
	))

	sink := &recordingSink{}
	handler := &recordingHandler{}

	m := NewMonitor(peers, fetcher, 0, sink)
	m.SetLogger(slog.New(handler))

	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.TotalMagnitude != 0 {
		t.Fatalf("expected magnitude 0, got %d", report.TotalMagnitude)
	}
	if report.SuccessCount != 2 {
		t.Fatalf("expected 2 successful peers, got %d", report.SuccessCount)
	}
	if report.Diverged() {
		t.Fatalf("expected no divergence")
	}
	if v, ok := sink.last(); !ok || v != 0 {
		t.Fatalf("expected gauge published at 0, got value=%d ok=%v", v, ok)
	}
	if handler.warnCount() != 0 {
		t.Fatalf("expected zero Warn logs, got %d", handler.warnCount())
	}
}

// TestMonitor_DivergenceDetected — one peer holds different balances;
// magnitude reflects sum of absolute deltas; per-peer breakdown
// surfaces the divergent agent.
func TestMonitor_DivergenceDetected(t *testing.T) {
	local := makeSnapshot("local",
		map[string]uint64{"alice": 1_000_000, "bob": 500_000, "treasury": 10_000},
		map[string]uint64{"task-1": 0},
		10_000,
	)
	peers := &stubPeerSource{addrs: []string{"peer-divergent"}}
	fetcher := newStubFetcher()
	// Peer settled differently: alice gained 100k, bob lost 100k,
	// treasury gained 5k. Magnitude = 100k + 100k + 5k = 205,000.
	fetcher.setSnapshot("peer-divergent", makeSnapshot("peer-divergent",
		map[string]uint64{"alice": 1_100_000, "bob": 400_000, "treasury": 10_000},
		map[string]uint64{"task-1": 0},
		15_000,
	))

	sink := &recordingSink{}
	handler := &recordingHandler{}
	m := NewMonitor(peers, fetcher, 0, sink)
	m.SetLogger(slog.New(handler))

	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !report.Diverged() {
		t.Fatalf("expected divergence")
	}
	const expectedMagnitude uint64 = 100_000 + 100_000 + 5_000
	if report.TotalMagnitude != expectedMagnitude {
		t.Fatalf("expected magnitude %d, got %d", expectedMagnitude, report.TotalMagnitude)
	}
	if v, _ := sink.last(); v != int64(expectedMagnitude) {
		t.Fatalf("expected gauge=%d, got %d", expectedMagnitude, v)
	}
	if !handler.hasWarnContaining("divergence detected") {
		t.Fatalf("expected Warn log about divergence, got none")
	}
	if len(report.Peers) != 1 {
		t.Fatalf("expected 1 peer in report, got %d", len(report.Peers))
	}
	pd := report.Peers[0]
	if pd.AgentDeltas["alice"] != 100_000 {
		t.Fatalf("expected alice delta=+100000, got %d", pd.AgentDeltas["alice"])
	}
	if pd.AgentDeltas["bob"] != -100_000 {
		t.Fatalf("expected bob delta=-100000, got %d", pd.AgentDeltas["bob"])
	}
	if pd.TreasuryDelta != 5_000 {
		t.Fatalf("expected treasury delta=+5000, got %d", pd.TreasuryDelta)
	}
}

// TestMonitor_PeerFetchError — one peer fails, others continue;
// failed peer recorded with FetchErr; successful peers contribute to
// magnitude.
func TestMonitor_PeerFetchError(t *testing.T) {
	local := makeSnapshot("local",
		map[string]uint64{"alice": 1_000_000},
		nil, 0,
	)
	peers := &stubPeerSource{addrs: []string{"peer-ok", "peer-fail", "peer-also-ok"}}
	fetcher := newStubFetcher()
	fetcher.setSnapshot("peer-ok", makeSnapshot("peer-ok",
		map[string]uint64{"alice": 1_000_000}, nil, 0,
	))
	fetcher.setError("peer-fail", errors.New("simulated network error"))
	fetcher.setSnapshot("peer-also-ok", makeSnapshot("peer-also-ok",
		map[string]uint64{"alice": 999_999}, nil, 0,
	))

	sink := &recordingSink{}
	handler := &recordingHandler{}
	m := NewMonitor(peers, fetcher, 0, sink)
	m.SetLogger(slog.New(handler))

	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.PeerCount != 3 {
		t.Fatalf("expected 3 peer attempts, got %d", report.PeerCount)
	}
	if report.SuccessCount != 2 {
		t.Fatalf("expected 2 successful peers, got %d", report.SuccessCount)
	}
	// Only peer-also-ok contributes (alice diff of 1).
	if report.TotalMagnitude != 1 {
		t.Fatalf("expected magnitude 1, got %d", report.TotalMagnitude)
	}
	// Find peer-fail and assert FetchErr is set.
	var seenFail bool
	for _, p := range report.Peers {
		if p.PeerAddr == "peer-fail" {
			if p.FetchErr == nil {
				t.Fatalf("expected FetchErr on peer-fail")
			}
			seenFail = true
		}
	}
	if !seenFail {
		t.Fatalf("peer-fail not present in report")
	}
}

// TestMonitor_AllPeersFail — when every peer fetch errors, Check
// returns nil error but logs a distinct warn and publishes 0.
func TestMonitor_AllPeersFail(t *testing.T) {
	local := makeSnapshot("local", map[string]uint64{"alice": 1}, nil, 0)
	peers := &stubPeerSource{addrs: []string{"a", "b"}}
	fetcher := newStubFetcher()
	fetcher.setError("a", errors.New("down"))
	fetcher.setError("b", errors.New("down"))

	sink := &recordingSink{}
	handler := &recordingHandler{}
	m := NewMonitor(peers, fetcher, 0, sink)
	m.SetLogger(slog.New(handler))

	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.SuccessCount != 0 {
		t.Fatalf("expected 0 successes, got %d", report.SuccessCount)
	}
	if !handler.hasWarnContaining("all peer snapshot fetches failed") {
		t.Fatalf("expected 'all peer snapshot fetches failed' warn")
	}
	if v, ok := sink.last(); !ok || v != 0 {
		t.Fatalf("expected gauge=0, got value=%d ok=%v", v, ok)
	}
}

// TestMonitor_ThresholdRespected — divergence below threshold does NOT
// trigger a Warn; above threshold DOES.
func TestMonitor_ThresholdRespected(t *testing.T) {
	cases := []struct {
		name       string
		threshold  uint64
		peerAlice  uint64 // peer's alice balance
		expectWarn bool
	}{
		{"below threshold no warn", 100_000, 1_000_001, false}, // diff=1 < 100k
		{"at threshold no warn", 1, 1_000_001, false},          // diff=1, threshold=1, "above" is strict >
		{"above threshold warn", 0, 1_000_001, true},           // diff=1 > 0
		{"large divergence warn", 1_000, 1_005_000, true},      // diff=5000 > 1000
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local := makeSnapshot("local", map[string]uint64{"alice": 1_000_000}, nil, 0)
			peers := &stubPeerSource{addrs: []string{"peer"}}
			fetcher := newStubFetcher()
			fetcher.setSnapshot("peer", makeSnapshot("peer",
				map[string]uint64{"alice": tc.peerAlice}, nil, 0,
			))

			sink := &recordingSink{}
			handler := &recordingHandler{}
			m := NewMonitor(peers, fetcher, tc.threshold, sink)
			m.SetLogger(slog.New(handler))

			if _, err := m.Check(context.Background(), local); err != nil {
				t.Fatalf("Check error: %v", err)
			}
			gotWarn := handler.hasWarnContaining("divergence detected")
			if gotWarn != tc.expectWarn {
				t.Fatalf("expected warn=%v, got warn=%v (warnCount=%d)",
					tc.expectWarn, gotWarn, handler.warnCount())
			}
		})
	}
}

// TestMonitor_NilLocalErrors — Check refuses a nil local snapshot.
func TestMonitor_NilLocalErrors(t *testing.T) {
	peers := &stubPeerSource{}
	fetcher := newStubFetcher()
	m := NewMonitor(peers, fetcher, 0, nil)
	if _, err := m.Check(context.Background(), nil); err == nil {
		t.Fatalf("expected error on nil local snapshot")
	}
}

// TestMonitor_NoPeers — empty peer set; no error, magnitude=0.
func TestMonitor_NoPeers(t *testing.T) {
	local := makeSnapshot("local", map[string]uint64{"alice": 1}, nil, 0)
	peers := &stubPeerSource{}
	fetcher := newStubFetcher()
	sink := &recordingSink{}
	m := NewMonitor(peers, fetcher, 0, sink)

	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if report.PeerCount != 0 || report.TotalMagnitude != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

// TestMonitor_PeerOnlyAgent — peer reports an agent local doesn't
// know; full peer balance becomes a delta.
func TestMonitor_PeerOnlyAgent(t *testing.T) {
	local := makeSnapshot("local", map[string]uint64{"alice": 100}, nil, 0)
	peers := &stubPeerSource{addrs: []string{"peer"}}
	fetcher := newStubFetcher()
	fetcher.setSnapshot("peer", makeSnapshot("peer",
		map[string]uint64{"alice": 100, "ghost": 42}, nil, 0,
	))

	sink := &recordingSink{}
	m := NewMonitor(peers, fetcher, 0, sink)
	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	if report.TotalMagnitude != 42 {
		t.Fatalf("expected magnitude=42, got %d", report.TotalMagnitude)
	}
	if d := report.Peers[0].AgentDeltas["ghost"]; d != 42 {
		t.Fatalf("expected ghost delta=+42, got %d", d)
	}
}

// TestReportFormat — Format renders deterministic, human-readable
// output and includes per-peer breakdown.
func TestReportFormat(t *testing.T) {
	local := makeSnapshot("local",
		map[string]uint64{"alice": 1_000_000},
		map[string]uint64{"task-1": 0}, 0,
	)
	peers := &stubPeerSource{addrs: []string{"peer-x"}}
	fetcher := newStubFetcher()
	fetcher.setSnapshot("peer-x", makeSnapshot("peer-x",
		map[string]uint64{"alice": 999_500}, map[string]uint64{"task-1": 500}, 0,
	))
	m := NewMonitor(peers, fetcher, 0, nil)
	report, err := m.Check(context.Background(), local)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	out := report.Format()
	for _, want := range []string{
		"Cross-node ledger divergence report",
		"local node:     local",
		"peers reached:  1 / 1",
		"DIVERGED",
		"peer peer-x",
		"alice",
		"task-1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected Format output to contain %q, got:\n%s", want, out)
		}
	}
}

// TestRegisterGauge — exercises the metric.go adapter end-to-end:
// register on a real internal/metrics Registry, verify the rendered
// Prometheus-format output reflects updates published by the adapter.
func TestRegisterGauge(t *testing.T) {
	reg := metrics.NewRegistry()
	sink := RegisterGauge(reg)
	if sink == nil {
		t.Fatalf("RegisterGauge returned nil")
	}
	sink.Set(12345)
	out := reg.Render()
	if !strings.Contains(out, MetricName) {
		t.Fatalf("rendered metrics missing %s:\n%s", MetricName, out)
	}
	if !strings.Contains(out, "12345") {
		t.Fatalf("rendered metrics missing gauge value:\n%s", out)
	}
	// Confirms that nil Registry yields nil sink (no panic).
	if RegisterGauge(nil) != nil {
		t.Fatalf("RegisterGauge(nil) should return nil")
	}
}
