// Package metrics implements a lightweight Prometheus-compatible metrics
// collector. It provides Counter, Gauge, and Histogram types backed by atomic
// operations, a Registry for managing named metrics, and a Render method that
// produces Prometheus text exposition format for the /metrics endpoint.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing counter.
type Counter struct {
	value uint64
}

func (c *Counter) Inc()             { atomic.AddUint64(&c.value, 1) }
func (c *Counter) Add(delta uint64) { atomic.AddUint64(&c.value, delta) }
func (c *Counter) Value() uint64    { return atomic.LoadUint64(&c.value) }

// Gauge is a value that can go up and down.
type Gauge struct {
	value int64
}

func (g *Gauge) Set(v int64)  { atomic.StoreInt64(&g.value, v) }
func (g *Gauge) Inc()         { atomic.AddInt64(&g.value, 1) }
func (g *Gauge) Dec()         { atomic.AddInt64(&g.value, -1) }
func (g *Gauge) Value() int64 { return atomic.LoadInt64(&g.value) }

// Histogram tracks value distribution with fixed upper-bound buckets.
type Histogram struct {
	mu      sync.Mutex
	buckets []float64 // upper bounds, sorted ascending
	counts  []uint64  // per-bucket counts; last slot is the +Inf bucket
	sum     float64
	count   uint64
}

// NewHistogram creates a histogram with the given bucket upper bounds.
// Buckets are sorted automatically so callers may pass them in any order.
func NewHistogram(buckets []float64) *Histogram {
	sorted := make([]float64, len(buckets))
	copy(sorted, buckets)
	// insertion sort — bucket slices are always short
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return &Histogram{
		buckets: sorted,
		counts:  make([]uint64, len(sorted)+1), // +1 for +Inf
	}
}

// Observe records a single observed value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i, bound := range h.buckets {
		if v <= bound {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.buckets)]++ // +Inf bucket
}

// Registry holds all named metrics and renders them in Prometheus format.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	help       map[string]string
	// order preserves registration order for deterministic Render output.
	counterNames   []string
	gaugeNames     []string
	histogramNames []string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
		help:       make(map[string]string),
	}
}

// Counter returns (or creates) a named counter with the given help string.
// Calling Counter with the same name twice returns the same *Counter.
func (r *Registry) Counter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{}
	r.counters[name] = c
	r.help[name] = help
	r.counterNames = append(r.counterNames, name)
	return c
}

// Gauge returns (or creates) a named gauge with the given help string.
func (r *Registry) Gauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{}
	r.gauges[name] = g
	r.help[name] = help
	r.gaugeNames = append(r.gaugeNames, name)
	return g
}

// Histogram returns (or creates) a named histogram with the given help string
// and bucket upper bounds.
func (r *Registry) Histogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h
	}
	h := NewHistogram(buckets)
	r.histograms[name] = h
	r.help[name] = help
	r.histogramNames = append(r.histogramNames, name)
	return h
}

// Render returns all registered metrics in Prometheus text exposition format.
// Metrics are emitted in registration order within each type group
// (counters, then gauges, then histograms).
func (r *Registry) Render() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var b strings.Builder

	for _, name := range r.counterNames {
		c := r.counters[name]
		if h, ok := r.help[name]; ok && h != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, h)
		}
		fmt.Fprintf(&b, "# TYPE %s counter\n", name)
		fmt.Fprintf(&b, "%s %d\n\n", name, c.Value())
	}

	for _, name := range r.gaugeNames {
		g := r.gauges[name]
		if h, ok := r.help[name]; ok && h != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, h)
		}
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&b, "%s %d\n\n", name, g.Value())
	}

	for _, name := range r.histogramNames {
		h := r.histograms[name]
		if help, ok := r.help[name]; ok && help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		}
		fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
		h.mu.Lock()
		cumulative := uint64(0)
		for i, bound := range h.buckets {
			cumulative += h.counts[i]
			fmt.Fprintf(&b, "%s_bucket{le=\"%.3f\"} %d\n", name, bound, cumulative)
		}
		cumulative += h.counts[len(h.buckets)]
		fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
		fmt.Fprintf(&b, "%s_sum %.3f\n", name, h.sum)
		fmt.Fprintf(&b, "%s_count %d\n\n", name, h.count)
		h.mu.Unlock()
	}

	return b.String()
}
