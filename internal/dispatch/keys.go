package dispatch

import (
	"encoding/hex"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
	"lukechampine.com/blake3"
)

const admissionKeyPrefix = "dispatch:"

// AdmissionKey computes the dispatcher's content-hash admission key for
// an event. Per C-3: the key is the BLAKE3 hash of the JCS-canonicalized
// event bytes, prefixed with "dispatch:".
//
// Canonicalization is performed BEFORE the BadgerDB admission transaction
// is opened (C-5). The returned key is used inside the transaction.
func AdmissionKey(ev *event.Event) (string, error) {
	canonical, err := event.CanonicalBytes(ev)
	if err != nil {
		return "", fmt.Errorf("dispatch: canonicalize event %s: %w", ev.ID, err)
	}
	return admissionKeyFromBytes(canonical), nil
}

func admissionKeyFromBytes(canonicalBytes []byte) string {
	sum := blake3.Sum256(canonicalBytes)
	return admissionKeyPrefix + hex.EncodeToString(sum[:])
}
