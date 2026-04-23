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

// logicalAdmissionKeyPrefix marks logical-key admission records (F4B,
// AdmissionStrategyLogicalKey). The "lk:" prefix sits inside the
// "dispatch:" namespace and prevents collision with content-hash
// admission keys (which are 64 hex characters, never starting with
// "lk:").
const logicalAdmissionKeyPrefix = "lk:"

// LogicalAdmissionKey returns the admission-store key for the
// (consumer, logical_key) pair. Format:
//
//	"dispatch:lk:" + consumer_name + ":" + key
//
// The "dispatch:" prefix is the global admission-store namespace shared
// with content-hash admissions; the "lk:" sub-prefix avoids collision
// with content-hash admission keys (which are BLAKE3 hex). The
// colon-separated form is operator-readable and allows prefix scans by
// consumer.
//
// Per locked-invariant review §3.5: "the admission store's primary key
// for logical-key admissions is (consumer_name, logical_key)."
func LogicalAdmissionKey(consumerName string, key LogicalKey) string {
	return admissionKeyPrefix + logicalAdmissionKeyPrefix + consumerName + ":" + string(key)
}
