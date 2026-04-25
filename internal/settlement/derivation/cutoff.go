package derivation

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// cutoffResult bundles the two canonical cutoff handles produced by
// computeCutoff: the anchor (EventID | nil per Fix A) and the epoch
// (uint64). Per Plan v3 §2.3 step 1 + canonical-epoch sub-spec §6.4
// orthogonality: the anchor and the epoch are distinct canonical
// values serving distinct purposes.
type cutoffResult struct {
	// anchor is the canonical_cutoff_anchor field on Provenance per
	// Fix A. Empty (nil semantic) iff ReputationActivation is NOT a
	// canonical ancestor of the consensus event being settled. Non-empty
	// when the locked Reputation-and-Consensus-Integrity workstream's
	// snapshot infrastructure ships and provides a concrete EventID.
	anchor event.EventID

	// anchorIsNil discriminates the Fix A nil case from the empty-
	// EventID-with-different-meaning case. True iff the cutoff anchor
	// was determined to be nil per Fix A.
	anchorIsNil bool

	// epoch is the cutoff_epoch passed to W.Lookup / Quality.Lookup.
	// Per canonical-epoch sub-spec §4.4: cutoff_epoch =
	// max(epoch_of(R) - 1, 0). For genesis rounds (epoch_of=0), the
	// max-clamp keeps cutoff_epoch >= 0 in uint64 space; pre-activation
	// stub W/quality ignore the epoch argument anyway, so the clamp is
	// benign.
	epoch uint64
}

// computeCutoff produces the canonical cutoff (anchor + epoch) for the
// settlement of consensusEventID per Plan v3 §2.3 step 1.
//
// **Shape 3 (founder direction 2026-04-25)**: epoch_of(R) is computed
// at call time via the canonical CountAncestorsByType primitive on the
// consensus event ID, NOT read from round.EpochAtFinalization (a
// recognition-fabric projection that races with dispatcher LK consumer
// dispatch). Same canonical value (sub-spec §3); cluster-uniform; race-
// free dispatch (does not depend on the recognition fabric having
// populated round.EpochAtFinalization before the LK consumer fires).
//
// useRealW is the result of the V-1 ActivationCheck for W (computed
// separately and passed in to avoid duplicate ActivationCheck calls).
// When useRealW is true, the caller has already established that
// ReputationActivation is a canonical ancestor of consensusEventID,
// so the cutoff anchor is non-nil per Fix A.
//
// Today (F5 5B ship) the locked Reputation-and-Consensus-Integrity
// workstream has not yet shipped its snapshot infrastructure. When
// useRealW is true (which is unreachable today because
// ReputationActivationEventID is the empty-string placeholder per
// internal/settlement/derivation/activation.go), the snapshot EventID
// for the cutoff anchor is not computable. The function returns the
// cutoff anchor as empty with anchorIsNil=false in that case, signaling
// to the caller that the path is a forward-work hole; combined with
// the V-1 selection of stub W today, this branch is not exercised.
//
// When the locked workstream ships:
//   - Add a snapshot-read primitive to DerivationInputs (e.g.,
//     SnapshotEventIDForEpoch(epoch uint64) (event.EventID, error)).
//   - In the useRealW branch below, query the primitive for the
//     cutoff_epoch and populate `anchor` with the returned EventID.
//   - Update FORWARD_NOTES.md §1 (V-1 const-flip) with closure note.
//
// Returns dag.ErrEventNotFound (or wrapping error) when
// CountAncestorsByType cannot resolve the consensus event's ancestry
// — caller (DeriveSettlement) converts to Status=StatusDeferred per
// the materialization-lag deferral pattern.
func computeCutoff(reader AnchorReader, consensusEventID event.EventID, useRealW bool) (cutoffResult, error) {
	// Shape 3: canonical epoch_of(R) via the canonical DAG primitive.
	// Sub-spec §3 + §4.4: epoch_of(R) = CountAncestorsByType(R,
	// EventTypeEpochBoundary). This is the SAME value the recognition
	// fabric populates into round.EpochAtFinalization at terminal-
	// transition time — but computed independently from the canonical
	// DAG, so the dispatcher LK consumer is not gated on the recognition
	// fabric having run yet.
	epochOf, err := reader.CountAncestorsByType(consensusEventID, event.EventTypeEpochBoundary)
	if err != nil {
		return cutoffResult{}, fmt.Errorf("computeCutoff: CountAncestorsByType(%s, EpochBoundary): %w", consensusEventID, err)
	}

	// cutoff_epoch = max(epoch_of(R) - 1, 0). epoch is uint64, so when
	// epoch_of==0 we keep epoch=0 (the max-clamp in the formula).
	// Pre-activation rounds always have epoch_of==0 (no canonical
	// EpochBoundary events committed yet); pre-activation stub
	// W/quality ignore epoch anyway, so this is benign.
	var epoch uint64
	if epochOf > 0 {
		epoch = epochOf - 1
	}

	if !useRealW {
		// Pre-activation per Fix A: cutoff anchor is nil.
		return cutoffResult{anchor: "", anchorIsNil: true, epoch: epoch}, nil
	}

	// Post-activation: cutoff anchor would be the locked workstream's
	// snapshot EventID at end of cutoff_epoch. The snapshot read
	// primitive is forward work (FORWARD_NOTES.md §1 + §2). Today this
	// branch is unreachable; surface the placeholder via empty anchor
	// with anchorIsNil=false so the test harness can detect the
	// forward-work signal if the branch ever fires.
	return cutoffResult{anchor: "", anchorIsNil: false, epoch: epoch}, nil
}
