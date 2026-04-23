package dispatch

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ErrPrerequisiteForgery is returned when a consumer declares a
// prerequisite EventID that is not DAG-reachable from the triggering
// event. Per D-4: forged prerequisites fail admission immediately; the
// dispatcher does not defer. This closes the self-stall blind spot.
var ErrPrerequisiteForgery = errors.New("dispatch: prerequisite forgery — EventID not DAG-reachable from triggering event")

// prerequisiteResult describes the outcome of a prerequisite check.
type prerequisiteResult struct {
	allProjected bool            // true if every prerequisite is already in the DAG
	missing      []event.EventID // valid prereqs not yet projected locally
}

// checkPrerequisites validates and checks projection status for each
// prerequisite returned by the consumer. Called outside the BadgerDB
// transaction per D-8.
//
// For each prerequisite EventID:
//  1. Validate it is DAG-reachable from the triggering event (D-4).
//     If not, return ErrPrerequisiteForgery.
//  2. Check if the prerequisite event exists in the local DAG.
//     If yes, it is projected. If not, it is missing.
//
// Returns the check result or an error on forgery/storage failure.
func (d *Dispatcher) checkPrerequisites(ev *event.Event, consumers []Consumer) (*prerequisiteResult, error) {
	prereqSet := make(map[event.EventID]struct{})
	for _, c := range consumers {
		for _, pid := range c.Prerequisites(ev) {
			prereqSet[pid] = struct{}{}
		}
	}

	if len(prereqSet) == 0 {
		return &prerequisiteResult{allProjected: true}, nil
	}

	// E.P1.A1 (F4 plan §6): iterate prereqSet in lex-sorted EventID order so
	// the resulting `missing` slice (which lands in AdmissionRecord.MissingPrerequisites)
	// is byte-equal across nodes for the same input prereq set. Without this,
	// nodes with the same DAG state could persist admission records that
	// differ only by missing-prereq ordering — non-canonical drift but
	// observable in operator triage and replay-conformance comparisons.
	prereqIDs := make([]event.EventID, 0, len(prereqSet))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for pid := range prereqSet {
		prereqIDs = append(prereqIDs, pid)
	}
	sort.Slice(prereqIDs, func(i, j int) bool { return prereqIDs[i] < prereqIDs[j] })

	var missing []event.EventID
	for _, pid := range prereqIDs {
		isAnc, err := d.dag.IsAncestor(pid, ev.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: consumer prerequisite %s for event %s: %v",
				ErrPrerequisiteForgery, pid, ev.ID, err)
		}
		if !isAnc {
			return nil, fmt.Errorf("%w: %s is not an ancestor of %s",
				ErrPrerequisiteForgery, pid, ev.ID)
		}

		if _, dagErr := d.dag.Get(pid); dagErr != nil {
			missing = append(missing, pid)
		}
	}

	return &prerequisiteResult{
		allProjected: len(missing) == 0,
		missing:      missing,
	}, nil
}
