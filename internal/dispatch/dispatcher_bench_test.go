package dispatch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Benchmarks captured by F4B step 0.3 to establish the F4A-end performance
// baseline for dispatcher latency. Captured baseline is the FIXED comparator
// for F4B's D-5 regression gate (>10% regression on any metric is a halt-
// and-surface trigger). DO NOT recapture later — F4A delta (including
// step-11 sort-fix timing) is part of the baseline intentionally.
//
// All benchmarks use a synthetic consumer (no real settlement work), so
// the measurement isolates dispatcher overhead. Real-consumer latency is
// dominated by settlement (escrow + ledger), which is out of dispatcher
// scope.

// makeSyntheticEvent constructs a unique TaskPosted event for benchmarking.
// Different seq values produce different canonical bytes → distinct
// content-hash admission keys.
func makeSyntheticEvent(seq uint64) *event.Event {
	ev, _ := event.New(
		event.EventTypeTaskPosted,
		nil,
		event.TaskPostedPayload{
			Version:     1,
			TaskID:      fmt.Sprintf("bench-task-%020d", seq),
			PosterID:    "bench-poster",
			Title:       "bench",
			Description: "bench",
			Category:    "research",
			Budget:      100,
		},
		"bench-poster",
		nil,
		seq,
	)
	return ev
}

// newBenchDispatcher constructs a fresh dispatcher with a synthetic
// always-interested consumer registered. The consumer's Apply is a single
// atomic increment — minimal work, isolating dispatcher overhead.
func newBenchDispatcher(b *testing.B) (*Dispatcher, *atomic.Int64) {
	b.Helper()
	store := newMemStore()
	dag := &stubDAG{
		tips:      []event.EventID{"bench-tip"},
		ancestors: map[[2]event.EventID]bool{},
		events:    make(map[event.EventID]*event.Event),
	}
	d := NewDispatcher(store, dag, func() uint64 { return 1 })
	c := newSyntheticConsumer("bench-consumer")
	if err := d.Register(c); err != nil {
		b.Fatalf("register: %v", err)
	}
	return d, &c.applyCount
}

// BenchmarkAdmit_FreshContentHash measures the per-event latency of
// admitting a unique event through the full dispatcher path:
// AdmissionKey → VerifyAnchor → snapshotInterestedConsumers →
// reserveOrLoad (creates new record) → checkPrerequisites →
// invokeConsumers → safeApply → persist record.
//
// This is the steady-state cost on the production code path for events
// the cluster has not seen before.
func BenchmarkAdmit_FreshContentHash(b *testing.B) {
	d, count := newBenchDispatcher(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := makeSyntheticEvent(uint64(i))
		if err := d.Admit(ctx, ev); err != nil {
			b.Fatalf("admit %d: %v", i, err)
		}
	}
	b.StopTimer()

	if count.Load() != int64(b.N) {
		b.Fatalf("apply count = %d, want %d", count.Load(), b.N)
	}
}

// BenchmarkAdmit_DuplicateContentHash measures the idempotent re-admit
// path: same event presented N times. After the first admission the
// record is in StateApplied; subsequent calls short-circuit at the
// "rec.State == StateApplied → return nil" branch in Admit.
//
// Represents the cost of the dispatcher's exactly-once guarantee on
// repeated delivery (which happens routinely in production via the
// fastpath + sync paths re-emitting the same event).
func BenchmarkAdmit_DuplicateContentHash(b *testing.B) {
	d, _ := newBenchDispatcher(b)
	ctx := context.Background()

	ev := makeSyntheticEvent(0)
	if err := d.Admit(ctx, ev); err != nil {
		b.Fatalf("initial admit: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.Admit(ctx, ev); err != nil {
			b.Fatalf("re-admit %d: %v", i, err)
		}
	}
}

// BenchmarkAdmit_ConcurrentDifferentEvents measures dispatcher latency
// under concurrent load with each goroutine admitting unique events
// (no per-task contention). Captures the cost of d.mu RLock/RUnlock
// in snapshotInterestedConsumers under concurrency.
func BenchmarkAdmit_ConcurrentDifferentEvents(b *testing.B) {
	d, _ := newBenchDispatcher(b)
	ctx := context.Background()

	var seq atomic.Uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ev := makeSyntheticEvent(seq.Add(1))
			if err := d.Admit(ctx, ev); err != nil {
				b.Errorf("admit: %v", err)
				return
			}
		}
	})
}

// BenchmarkAdmit_ConcurrentSameEvent measures the contended path: many
// goroutines admitting the SAME event simultaneously. After the first
// admission, all subsequent goroutines hit the StateApplied short-circuit;
// we measure the cost of the d.mu.RLock + admission-store read under
// contention.
func BenchmarkAdmit_ConcurrentSameEvent(b *testing.B) {
	d, _ := newBenchDispatcher(b)
	ctx := context.Background()

	ev := makeSyntheticEvent(0)
	if err := d.Admit(ctx, ev); err != nil {
		b.Fatalf("initial admit: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := d.Admit(ctx, ev); err != nil {
				b.Errorf("admit: %v", err)
				return
			}
		}
	})
}

// BenchmarkAdmit_StreamWithBackpressure simulates a representative
// production workload: a sustained stream of unique admissions across a
// fixed set of goroutines (modeling fastpath + sync handlers + the
// recognition bus all calling Admit concurrently for different events).
//
// Workload shape: 10 producer goroutines, each admitting 1/10th of b.N
// unique events. Each Admit is full-path (fresh content-hash). Wall-
// clock/op captures the contention shape under realistic load.
func BenchmarkAdmit_StreamWithBackpressure(b *testing.B) {
	const producers = 10

	d, _ := newBenchDispatcher(b)
	ctx := context.Background()

	perProducer := b.N / producers
	if perProducer == 0 {
		perProducer = 1
	}

	var seq atomic.Uint64
	var wg sync.WaitGroup

	b.ResetTimer()
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				ev := makeSyntheticEvent(seq.Add(1))
				if err := d.Admit(ctx, ev); err != nil {
					b.Errorf("admit: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
