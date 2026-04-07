// Package taskverification implements multi-validator task verification
// consensus as specified in docs/multi-validator-consensus-final-design.md.
//
// A TaskVerificationRound tracks the lifecycle of a single task submission
// through multi-validator scoring and BFT consensus. Each validator
// independently scores the evidence, emits a TaskVerificationVote DAG event,
// and the round aggregates votes until supermajority is reached. The final
// verdict (accept, reject, or disputed) determines settlement.
//
// This package is in scaffolding state (prompt 01 of 9). It provides the
// data model, state machine, and BadgerDB-backed persistence. It is not
// yet wired into any flow — no recognition consumers, no autovalidator
// integration, no settlement hooks. Subsequent prompts build on this
// foundation.
//
// Layer: Infrastructure — importable by any layer.
package taskverification
