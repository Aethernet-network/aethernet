// Package escrow_testhelpers provides test-only combined fund-and-register
// primitives that production code cannot import.
//
// The module is structurally quarantined: its module path is declared in a
// separate go.mod, and the main module's go.mod does not depend on it. The
// production binary's dependency tree (go list -deps ./cmd/node) therefore
// excludes this package by construction. See
// docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §6.1 and
// docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §1, §3.
package escrow_testhelpers

import (
	"errors"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// FundAndRegisterEscrowForTest performs the legacy escrow.Hold side effect —
// transfer funds from poster to the escrow bucket, then register metadata —
// in one call. Test fixtures and integration tests use it where they need the
// combined behavior without a DAG or protocol client.
//
// Production code cannot import this module (separate go.mod, not declared in
// main go.mod). The no-bypass CI lint enforces the boundary structurally.
func FundAndRegisterEscrowForTest(
	tl *ledger.TransferLedger,
	esc *escrow.Escrow,
	taskID string,
	poster crypto.AgentID,
	amount uint64,
) error {
	if err := tl.TransferFromBucket(poster, crypto.AgentID("escrow:"+taskID), amount); err != nil {
		return err
	}
	ref := event.EventID("test-funding:" + taskID)
	return esc.RegisterEscrowForTest(taskID, poster, amount, ref)
}

// MockDAG is an in-memory DAGReader for external-package tests that exercise
// RegisterEscrow (the DAG-validated path). Satisfies escrow.DAGReader.
type MockDAG struct {
	Events map[event.EventID]*event.Event
}

// NewMockDAG returns an empty MockDAG.
func NewMockDAG() *MockDAG {
	return &MockDAG{Events: make(map[event.EventID]*event.Event)}
}

// Get implements escrow.DAGReader.
func (m *MockDAG) Get(id event.EventID) (*event.Event, error) {
	if e, ok := m.Events[id]; ok {
		return e, nil
	}
	return nil, errors.New("mock-dag: event not found: " + string(id))
}
