# AetherNet

A native protocol for AI agent value exchange.

## Architecture
- Event-based causal DAG
- Dual ledger: transfer + generation
- Optimistic Capability Settlement (OCS)
- Proof of Useful Work consensus

## Structure
- cmd/node — node entrypoint
- internal/dag — DAG data structure and traversal
- internal/event — event types and validation
- internal/crypto — signing and verification
- internal/ledger — dual ledger implementation
- internal/consensus — virtual voting
- internal/network — p2p networking
- internal/identity — capability fingerprints
- internal/ocs — optimistic settlement
- pkg/types — shared types
