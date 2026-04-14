// Package projections defines the protocol-primitive registry of Canonical
// and Advisory projections that derive protocol-affecting state from the DAG.
//
// It closes the "writer exists, caller doesn't" class of bugs by requiring
// every durable, consensus-adjacent store to be registered with a live
// consumer, replay consumer, integration test, and observability surface
// before the node can claim healthy status.
//
// Binding spec:
//   - docs/plans/2026-04-12-reputation-and-consensus-integrity.md §9
//     (projection registry), §2.3 (CR-8/CR-9 named exceptions),
//     §16 (EligibilityWindow = 3 epochs).
//   - docs/plans/2026-04-12-reputation-step-1-projection-registry.md
//     (this step's implementation plan, including idempotency and
//     restart semantics clarifications).
//
// Step 1 registers zero projections. The retrofit of existing stores
// (EvidenceStore, CalibrationStore, EscrowStore, LedgerStore,
// GenerationLedgerCalculator, AgentReputation, BlobServingReputation)
// is Step 2.
package projections
