package derivation

import (
	"sort"
)

// AssignOrdinals applies the schema 4-step ordinal-assignment rule to
// `records` in place, then returns them sorted in canonical order:
//
//  1. Group by Purpose.Tag.
//  2. Within each tag group, sort by Recipient.ID lex.
//  3. Tag groups are processed in the fixed order defined by
//     OrdinalAssignmentOrder.
//  4. Ordinal is a single monotone counter from 0 across the full
//     ordered sequence; does NOT reset between tag groups.
//
// Per docs/architecture/payout-artifact-schema.yaml §purpose.ordinal
// `ordinal_assignment_rule` (LOCKED at Gate 5A.4.a). 5B implements TO
// the schema; CI lint at internal/settlement/lint/ + 5D verification
// harness assert byte-equality of the produced (canonical_id, ordinal)
// pairs.
//
// Records of types not in OrdinalAssignmentOrder are an upstream bug
// (the schema's tag enum is closed); they are placed AFTER the locked
// sequence and ordered by Tag lex then Recipient.ID lex, so the
// behavior is deterministic but indicates a contract violation upstream
// (worth surfacing via the canonical_id check + audit).
func AssignOrdinals(records []PayoutRecord) []PayoutRecord {
	if len(records) == 0 {
		return records
	}

	// Step 1: group by Tag.
	byTag := make(map[PurposeTag][]PayoutRecord, len(OrdinalAssignmentOrder))
	for _, r := range records {
		byTag[r.Purpose.Tag] = append(byTag[r.Purpose.Tag], r)
	}

	// Step 2: sort each group by Recipient.ID lex.
	// safe: writes back to byTag[tag] for the same tag we just read; map iteration order does not affect canonical output (Step 3 below re-reads via OrdinalAssignmentOrder)
	for tag := range byTag {
		group := byTag[tag]
		sort.Slice(group, func(i, j int) bool {
			return group[i].Recipient.ID < group[j].Recipient.ID
		})
		byTag[tag] = group
	}

	// Step 3: process tag groups in fixed order; collect into a single
	// ordered slice. Step 4: ordinal is a monotone counter across the
	// full sequence.
	ordered := make([]PayoutRecord, 0, len(records))
	var ordinal uint32

	knownTags := make(map[PurposeTag]struct{}, len(OrdinalAssignmentOrder))
	for _, tag := range OrdinalAssignmentOrder {
		knownTags[tag] = struct{}{}
		group, ok := byTag[tag]
		if !ok {
			continue
		}
		for _, r := range group {
			r.Purpose.Ordinal = ordinal
			ordered = append(ordered, r)
			ordinal++
		}
	}

	// Defensive: any records with a tag not in OrdinalAssignmentOrder
	// (schema-contract violation) get placed after the locked sequence.
	// Sort the unknown groups deterministically by Tag then Recipient.ID
	// so the behavior is canonical even on broken input.
	var unknownTags []PurposeTag
	// safe: collected then sorted before iteration; sort.Slice on the next line establishes canonical order
	for tag := range byTag {
		if _, known := knownTags[tag]; known {
			continue
		}
		unknownTags = append(unknownTags, tag)
	}
	sort.Slice(unknownTags, func(i, j int) bool { return unknownTags[i] < unknownTags[j] })
	for _, tag := range unknownTags {
		for _, r := range byTag[tag] {
			r.Purpose.Ordinal = ordinal
			ordered = append(ordered, r)
			ordinal++
		}
	}

	return ordered
}
