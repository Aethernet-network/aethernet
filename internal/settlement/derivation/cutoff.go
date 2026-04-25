package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// cutoffResult bundles the two canonical cutoff handles produced by
// computeCutoff: the anchor (EventID | nil per Fix A) and the epoch
// (uint64). Per Plan v3 §2.3 step 1 + canonical-epoch sub-spec §6.4
// orthogonality: the anchor and the epoch are distinct canonical
// values serving distinct purposes.
type cutoffResult struct {
	// anchor is the canonical_cutoff_anchor field on Provenance per
	// Fix A. Empty (nil semantic) iff ReputationActivation is NOT a
	// canonical ancestor of round.CanonicalSealContext. Non-empty when
	// the locked Reputation-and-Consensus-Integrity workstream's
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

// computeCutoff produces the canonical cutoff (anchor + epoch) for
// round R per Plan v3 §2.3 step 1.
//
// useRealW is the result of the V-1 ActivationCheck for W (computed
// separately and passed in to avoid duplicate ActivationCheck calls).
// When useRealW is true, the caller has already established that
// ReputationActivation is a canonical ancestor of R.CanonicalSealContext,
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
func computeCutoff(round *taskverification.TaskVerificationRound, useRealW bool) cutoffResult {
	var epoch uint64
	if round.EpochAtFinalization > 0 {
		epoch = round.EpochAtFinalization - 1
	}
	// epoch is uint64, so when EpochAtFinalization == 0 we keep epoch=0
	// (the max-clamp in the formula). Pre-activation rounds always have
	// EpochAtFinalization == 0 (no canonical EpochBoundary events
	// committed yet); pre-activation stub W/quality ignore epoch
	// anyway, so this is benign.

	if !useRealW {
		// Pre-activation per Fix A: cutoff anchor is nil.
		return cutoffResult{anchor: "", anchorIsNil: true, epoch: epoch}
	}

	// Post-activation: cutoff anchor would be the locked workstream's
	// snapshot EventID at end of cutoff_epoch. The snapshot read
	// primitive is forward work (FORWARD_NOTES.md §1 + §2). Today this
	// branch is unreachable; surface the placeholder via empty anchor
	// with anchorIsNil=false so the test harness can detect the
	// forward-work signal if the branch ever fires.
	return cutoffResult{anchor: "", anchorIsNil: false, epoch: epoch}
}
