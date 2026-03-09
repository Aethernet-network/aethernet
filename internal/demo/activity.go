// Package demo provides testnet utilities for keeping the explorer alive.
//
// ActivityGenerator periodically fires small transfers between a fixed set of
// seed agents so the explorer's activity feed and recent-events list always
// show live data — even when no real users are transacting.
//
// It is intentionally lightweight: a single goroutine, no locks, no external
// dependencies beyond the transfer closure supplied by cmd/node/main.go.
package demo

import (
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// ActivityGenerator fires synthetic transfers between seed agents on a fixed
// interval. The actual event construction is delegated to transferFunc so this
// package has no dependency on the core event/ledger packages.
type ActivityGenerator struct {
	transferFunc func(from, to string, amount uint64, memo string) error
	agents       []string
	memos        []string
	interval     time.Duration
	stop         chan struct{}
	once         sync.Once
}

// defaultMemos is a list of realistic task-completion memo strings used to
// make synthetic transfers look indistinguishable from real agent activity.
var defaultMemos = []string{
	"AI inference compute",
	"NLP task completion",
	"Data pipeline execution",
	"Model fine-tuning job",
	"Image classification batch",
	"Sentiment analysis run",
	"Vector embedding generation",
	"Code review completion",
	"Research summary delivery",
	"SQL query optimisation",
	"Translation task",
	"Dataset annotation",
	"API integration test",
	"Evaluation harness run",
	"Benchmark report delivery",
	"Document parsing job",
	"Retrieval-augmented answer",
	"Automated regression test",
	"Schema validation service",
	"Multi-step agent workflow",
}

// NewActivityGenerator returns a generator that calls transferFunc with random
// (from, to, amount, memo) tuples drawn from agents on every tick.
//
// interval controls how often a transfer is attempted; 30 s is the recommended
// testnet default. The generator does not start until Start is called.
func NewActivityGenerator(
	transferFunc func(from, to string, amount uint64, memo string) error,
	agents []string,
	interval time.Duration,
) *ActivityGenerator {
	return &ActivityGenerator{
		transferFunc: transferFunc,
		agents:       agents,
		memos:        defaultMemos,
		interval:     interval,
		stop:         make(chan struct{}),
	}
}

// Start launches the background activity loop. It is safe to call exactly once;
// subsequent calls are no-ops.
func (g *ActivityGenerator) Start() {
	go g.run()
}

// Stop shuts down the background loop. It is safe to call from any goroutine
// and may be called multiple times.
func (g *ActivityGenerator) Stop() {
	g.once.Do(func() { close(g.stop) })
}

func (g *ActivityGenerator) run() {
	if len(g.agents) < 2 {
		return
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // testnet only
	// Initial delay — wait for the OCS engine and auto-validator to settle the
	// genesis state before we start firing synthetic events.
	select {
	case <-g.stop:
		return
	case <-time.After(30 * time.Second):
	}
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.generateActivity(r)
		}
	}
}

func (g *ActivityGenerator) generateActivity(r *rand.Rand) {
	n := len(g.agents)
	fromIdx := r.Intn(n)
	// Ensure toIdx != fromIdx by picking from the remaining n-1 slots.
	toIdx := r.Intn(n - 1)
	if toIdx >= fromIdx {
		toIdx++
	}
	from := g.agents[fromIdx]
	to := g.agents[toIdx]
	// Vary amount: 1 000 – 25 000 micro-AET (0.001 – 0.025 AET).
	amount := uint64(1_000 + r.Intn(24_000))
	memo := g.memos[r.Intn(len(g.memos))]
	if err := g.transferFunc(from, to, amount, memo); err != nil {
		slog.Warn("activity generator: transfer skipped", "from", from, "to", to, "err", err)
	}
}
