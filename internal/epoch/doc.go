// Package epoch owns the protocol's round-derived epoch clock.
//
// A RoundCounter tracks the total number of finalized
// TaskVerificationConsensus events and derives the current epoch as
// totalFinalizedRounds / EpochLength, where EpochLength is the plan §16
// locked value of 1000 rounds per epoch.
//
// The counter is itself a Canonical projection: its state derives from
// the DAG (via the RoundCountConsumer registered on the recognition
// fabric), persists in BadgerDB so it survives restarts, and feeds the
// projection registry's PR-5 eligibility window via a lazy epochFn
// closure.
//
// Binding spec: docs/plans/2026-04-12-reputation-and-consensus-integrity.md
// §16 (EpochLength = 1000). Step-2 plan: docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md
// §D1 (resolution: build a real round counter, not a placeholder).
package epoch
