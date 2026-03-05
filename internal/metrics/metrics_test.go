package metrics_test

import (
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/metrics"
)

// TestCounter_IncAndAdd verifies that Inc and Add both accumulate correctly.
func TestCounter_IncAndAdd(t *testing.T) {
	reg := metrics.NewRegistry()
	c := reg.Counter("test_counter", "test help")

	c.Inc()
	c.Inc()
	c.Add(5)

	if got := c.Value(); got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
}

// TestGauge_SetIncDec verifies Set, Inc, and Dec all work correctly.
func TestGauge_SetIncDec(t *testing.T) {
	reg := metrics.NewRegistry()
	g := reg.Gauge("test_gauge", "test help")

	g.Set(10)
	g.Inc()
	g.Dec()
	g.Dec()

	if got := g.Value(); got != 9 {
		t.Fatalf("expected 9, got %d", got)
	}
}

// TestHistogram_Observe verifies that observed values are placed in the correct
// buckets and the sum and count are tracked accurately.
func TestHistogram_Observe(t *testing.T) {
	reg := metrics.NewRegistry()
	h := reg.Histogram("test_hist", "test help", []float64{10, 50, 100})

	h.Observe(5)   // bucket le=10
	h.Observe(20)  // bucket le=50
	h.Observe(75)  // bucket le=100
	h.Observe(200) // +Inf bucket

	rendered := reg.Render()
	if !strings.Contains(rendered, `test_hist_count 4`) {
		t.Errorf("expected count 4 in output:\n%s", rendered)
	}
	if !strings.Contains(rendered, `test_hist_bucket{le="+Inf"} 4`) {
		t.Errorf("expected +Inf bucket = 4 in output:\n%s", rendered)
	}
	if !strings.Contains(rendered, `test_hist_bucket{le="10.000"} 1`) {
		t.Errorf("expected le=10 bucket = 1 in output:\n%s", rendered)
	}
	if !strings.Contains(rendered, `test_hist_bucket{le="100.000"} 3`) {
		t.Errorf("expected le=100 bucket = 3 (cumulative) in output:\n%s", rendered)
	}
}

// TestRegistry_Render verifies that Render produces valid Prometheus exposition
// format with HELP lines, TYPE lines, and metric values.
func TestRegistry_Render(t *testing.T) {
	reg := metrics.NewRegistry()
	c := reg.Counter("my_counter", "A test counter")
	g := reg.Gauge("my_gauge", "A test gauge")

	c.Add(42)
	g.Set(7)

	out := reg.Render()

	checks := []string{
		"# HELP my_counter A test counter",
		"# TYPE my_counter counter",
		"my_counter 42",
		"# HELP my_gauge A test gauge",
		"# TYPE my_gauge gauge",
		"my_gauge 7",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q\nFull output:\n%s", want, out)
		}
	}
}

// TestRegistry_DuplicateNames verifies that calling Counter/Gauge/Histogram with
// the same name returns the same metric instance (not a new one).
func TestRegistry_DuplicateNames(t *testing.T) {
	reg := metrics.NewRegistry()

	c1 := reg.Counter("dup_counter", "help")
	c1.Add(10)
	c2 := reg.Counter("dup_counter", "ignored help")
	c2.Inc()

	// c1 and c2 must be the same pointer — mutations on c2 are visible via c1.
	if c1.Value() != 11 {
		t.Fatalf("expected 11 (shared instance), got %d", c1.Value())
	}
	if c1 != c2 {
		t.Fatal("expected Counter with duplicate name to return the same pointer")
	}

	g1 := reg.Gauge("dup_gauge", "help")
	g1.Set(5)
	g2 := reg.Gauge("dup_gauge", "ignored")
	if g1 != g2 {
		t.Fatal("expected Gauge with duplicate name to return the same pointer")
	}
	if g2.Value() != 5 {
		t.Fatalf("expected 5 via second handle, got %d", g2.Value())
	}
}
