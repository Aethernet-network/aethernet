package dispatch

import (
	"errors"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ErrCorruptedAdmissionState indicates the stored DAG anchor is not an
// ancestor of (or equal to) any current DAG tip, which means the
// admission state may have been corrupted or the DAG was rolled back.
var ErrCorruptedAdmissionState = errors.New("dispatch: corrupted admission state — DAG anchor not reachable from any current tip")

// ErrAdmissionSchemaTooNew indicates an AdmissionRecord persisted with
// SchemaVersion > AdmissionCurrentVersion. Returned by store.GetAdmission
// and store.AllAdmissions when the running binary cannot decode a record
// written by a newer binary. Mixed-binary clusters MUST resolve this by
// upgrading the binary; the dispatcher will not silently mis-decode the
// newer record into the older struct shape (FINDING #5 fix).
//
// Operator action: upgrade the binary to one whose AdmissionCurrentVersion
// is ≥ the persisted SchemaVersion. No canonical ledger rollback is
// implied — admission state is non-canonical node-local per C-15.
var ErrAdmissionSchemaTooNew = errors.New("dispatch: admission record schema_version exceeds binary's AdmissionCurrentVersion — operator action: upgrade binary")

// ErrUnknownAdmissionState indicates an AdmissionRecord persisted with a
// State value not in the AdmissionState enum (per IsKnownAdmissionState).
// Returned by store.GetAdmission and store.AllAdmissions for the same
// reason as ErrAdmissionSchemaTooNew: silently surfacing an unknown state
// would let the dispatcher's switch-statement default branch make an
// incorrect lifecycle decision (FINDING #6 fix).
//
// Adding a new AdmissionState value requires bumping AdmissionCurrentVersion
// in the same commit (so SchemaVersion gating catches the situation
// upstream of state-value gating).
var ErrUnknownAdmissionState = errors.New("dispatch: admission record state value not in AdmissionState enum — operator action: upgrade binary")

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
