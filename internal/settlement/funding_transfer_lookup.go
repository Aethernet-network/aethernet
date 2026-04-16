package settlement

import (
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ErrFundingTransferNotFound is returned by LookupEscrowLockTransfer when no
// matching canonical Transfer event is projected in the DAG. Callers classify
// this as a prerequisite failure handled by Part D deferral (once Part D lands).
var ErrFundingTransferNotFound = errors.New("settlement: funding transfer not found in DAG")

// DAGScanner is the subset of *dag.DAG used by LookupEscrowLockTransfer.
// Exported for test stubs. *dag.DAG satisfies it via its All() method.
type DAGScanner interface {
	All() []*event.Event
}

// LookupEscrowLockTransfer scans the DAG for the canonical Transfer event that
// funded the escrow identified by (taskID, poster, amount). Used by the
// verification-consensus settler's escrow catch-up path (audit C1-2) and by
// the task-settler closure in cmd/node/main.go (audit C1-3) to supply a
// fundingTransferRef to RegisterEscrow.
//
// Matches on (Type=Transfer, Reason=escrow-lock, FromAgent=poster,
// ToAgent="escrow:"+taskID, Amount=amount, TaskID=taskID). Returns
// ErrFundingTransferNotFound (wrapped) if no match exists; returns the first
// match otherwise — the canonicalization layer guarantees at most one
// matching event per (poster, taskID) tuple because escrow-lock transfers
// are unique per task.
//
// O(N) in DAG size. Acceptable for catch-up paths which fire at most once
// per task. If profiling indicates hot-path scans, add a secondary index.
func LookupEscrowLockTransfer(
	scanner DAGScanner,
	taskID string,
	poster crypto.AgentID,
	amount uint64,
) (event.EventID, error) {
	expectedBucket := "escrow:" + taskID
	for _, e := range scanner.All() {
		if e.Type != event.EventTypeTransfer {
			continue
		}
		tp, err := event.GetPayload[event.TransferPayload](e)
		if err != nil {
			continue
		}
		if tp.Reason != "escrow-lock" {
			continue
		}
		if tp.FromAgent != string(poster) {
			continue
		}
		if tp.ToAgent != expectedBucket {
			continue
		}
		if tp.Amount != amount {
			continue
		}
		if tp.TaskID != taskID {
			continue
		}
		return e.ID, nil
	}
	return "", fmt.Errorf("%w: task=%s poster=%s amount=%d",
		ErrFundingTransferNotFound, taskID, poster, amount)
}
