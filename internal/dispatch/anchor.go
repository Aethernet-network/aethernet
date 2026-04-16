package dispatch

import (
	"errors"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ErrCorruptedAdmissionState indicates the stored DAG anchor is not an
// ancestor of (or equal to) any current DAG tip, which means the
// admission state may have been corrupted or the DAG was rolled back.
var ErrCorruptedAdmissionState = errors.New("dispatch: corrupted admission state — DAG anchor not reachable from any current tip")

// DAGAnchorReader is the subset of *dag.DAG used by the dispatcher for
// anchor verification. *dag.DAG satisfies this interface.
type DAGAnchorReader interface {
	Tips() []event.EventID
	IsAncestor(ancestor, descendant event.EventID) (bool, error)
	Get(id event.EventID) (*event.Event, error)
}

// VerifyAnchor confirms that storedAnchor is an ancestor of (or equal to)
// at least one current DAG tip. Per C-6: verified on every admission, not
// just at startup. Per C-13: the anchor is the canonical content-addressed
// identifier of the DAG tip at reservation time.
//
// An empty storedAnchor (first admission / empty store) passes unconditionally.
//
// Exported for reuse by Parts A and B (applicator and escrow load-time
// DAG-anchor verification).
func VerifyAnchor(dag DAGAnchorReader, storedAnchor event.EventID) error {
	if storedAnchor == "" {
		return nil
	}
	tips := dag.Tips()
	if len(tips) == 0 {
		return ErrCorruptedAdmissionState
	}
	for _, tip := range tips {
		if tip == storedAnchor {
			return nil
		}
		isAnc, err := dag.IsAncestor(storedAnchor, tip)
		if err != nil {
			continue
		}
		if isAnc {
			return nil
		}
	}
	return ErrCorruptedAdmissionState
}
