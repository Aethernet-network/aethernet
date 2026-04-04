package recognition

import "github.com/Aethernet-network/aethernet/internal/event"

// DAGReader is the minimal DAG interface needed by the ReadModel adapter.
// *dag.DAG satisfies this interface.
type DAGReader interface {
	Get(id event.EventID) (*event.Event, error)
}

// DAGReadModel adapts a DAG to the ReadModel interface required by
// CommitConsumer.Ready calls.
type DAGReadModel struct {
	dag DAGReader
}

// NewDAGReadModel creates a ReadModel backed by the given DAG.
func NewDAGReadModel(d DAGReader) *DAGReadModel {
	return &DAGReadModel{dag: d}
}

// HasEvent reports whether the event exists in the DAG.
func (rm *DAGReadModel) HasEvent(id event.EventID) bool {
	_, err := rm.dag.Get(id)
	return err == nil
}

// GetEvent retrieves an event from the DAG. Returns nil if not found.
func (rm *DAGReadModel) GetEvent(id event.EventID) *event.Event {
	ev, err := rm.dag.Get(id)
	if err != nil {
		return nil
	}
	return ev
}
