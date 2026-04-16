package escrow

import "github.com/Aethernet-network/aethernet/internal/event"

// DAGReader is the subset of *dag.DAG used by RegisterEscrow for funding-
// transfer validation. *dag.DAG from internal/dag satisfies this interface.
//
// Injected via Escrow.SetDAGReader. RegisterEscrow returns
// ErrDAGReaderNotConfigured if invoked with no reader attached.
type DAGReader interface {
	Get(id event.EventID) (*event.Event, error)
}
