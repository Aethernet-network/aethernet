package projections

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// ProjectionRegistry is the in-memory registry of Canonical and Advisory
// projections. It is constructed per-process (not persisted); entries are
// re-registered on each process start. See the plan doc, Clarification 2,
// for the restart semantic.
type ProjectionRegistry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
	epochFn func() uint64
}

// registryEntry holds a projection along with the epoch it was registered
// at. registeredAtEpoch is captured once at Register time and is immutable
// thereafter, per plan-doc Clarification 2.
type registryEntry struct {
	projection        CanonicalProjection
	registeredAtEpoch uint64
}

// NewProjectionRegistry constructs a registry that uses epochFn to obtain
// the current epoch. epochFn must be non-nil and cheap to call; it is
// invoked from Register (once per registration) and HealthCheck (once per
// invocation plus once per eligible Canonical entry).
func NewProjectionRegistry(epochFn func() uint64) *ProjectionRegistry {
	if epochFn == nil {
		panic("projections: NewProjectionRegistry requires non-nil epochFn")
	}
	return &ProjectionRegistry{
		entries: make(map[string]registryEntry),
		epochFn: epochFn,
	}
}

// Register validates and records a projection.
//
// Validation enforces plan §9.4 invariants PR-1..PR-4 and the Step-1
// CR-9 coupling and StateProbe requirements. See the plan doc's
// validation matrix (rows V1..V16) for the full set.
//
// Idempotency (plan-doc Clarification 3):
//   - If the same Name is re-registered with byte-identical non-function
//     fields, Register returns nil and the existing entry is retained
//     (including its first-registered StateProbe and registeredAtEpoch).
//   - If any non-function field differs, Register returns an error naming
//     the mismatched field.
//   - StateProbe is intentionally excluded from the mismatch comparison;
//     Go does not support function-value equality. Re-registering with a
//     different probe but identical metadata is a no-op and the
//     first-registered probe is retained.
func (r *ProjectionRegistry) Register(p CanonicalProjection) error {
	if err := validateEntry(p); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.entries[p.Name]; ok {
		return checkIdempotentOrMismatch(existing.projection, p)
	}

	r.entries[p.Name] = registryEntry{
		projection:        p,
		registeredAtEpoch: r.epochFn(),
	}
	return nil
}

// MustRegister wraps Register and panics on error. Intended for startup
// wiring in cmd/node/main.go where validation failure is a fatal
// diagnostic per PR-1..PR-4.
func (r *ProjectionRegistry) MustRegister(p CanonicalProjection) {
	if err := r.Register(p); err != nil {
		panic("projection registry: MustRegister failed: " + err.Error())
	}
}

// Len returns the number of registered entries. Safe for concurrent use.
func (r *ProjectionRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Get returns the registered projection for a name. The second return is
// false if no entry exists. Returned entry is a value copy; callers cannot
// mutate registry state through it.
func (r *ProjectionRegistry) Get(name string) (CanonicalProjection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return CanonicalProjection{}, false
	}
	return e.projection, true
}

// List returns all registered projections sorted by Name. Safe for
// concurrent use; each call returns a fresh slice.
func (r *ProjectionRegistry) List() []CanonicalProjection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CanonicalProjection, 0, len(r.entries))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, e := range r.entries {
		out = append(out, e.projection)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ----- internal helpers -------------------------------------------------------

// validateEntry enforces rows V1, V3..V16 of the validation matrix. V2 is
// replaced by the idempotency semantic and is handled in Register itself.
func validateEntry(p CanonicalProjection) error {
	// V1 — Name required.
	if p.Name == "" {
		return fmt.Errorf("projection registry: entry rejected: Name is required")
	}
	// V12 — Package required.
	if p.Package == "" {
		return fmt.Errorf("projection registry: entry %q: Package is required", p.Name)
	}
	// V13 — StoreType required.
	if p.StoreType == "" {
		return fmt.Errorf("projection registry: entry %q: StoreType is required", p.Name)
	}
	// V3 — Classification must be Canonical or Advisory.
	switch p.Classification {
	case Canonical, Advisory:
		// ok
	default:
		return fmt.Errorf("projection registry: entry %q: Classification must be Canonical or Advisory", p.Name)
	}
	// V14 — Owner required.
	if p.Owner == "" {
		return fmt.Errorf("projection registry: entry %q: Owner is required", p.Name)
	}
	// V15 — CreatedAt required.
	if p.CreatedAt == "" {
		return fmt.Errorf("projection registry: entry %q: CreatedAt is required", p.Name)
	}

	// CR-9 coupling (V9/V10) — applies to both Canonical and Advisory.
	if p.AllowIdleWithJustification && p.IdleJustification == "" {
		return fmt.Errorf("projection registry: entry %q: AllowIdleWithJustification requires non-empty IdleJustification (CR-9)", p.Name)
	}
	if !p.AllowIdleWithJustification && p.IdleJustification != "" {
		return fmt.Errorf("projection registry: entry %q: IdleJustification provided without AllowIdleWithJustification (CR-9)", p.Name)
	}

	if p.Classification == Canonical {
		// V4 — LiveConsumerRef required (PR-1).
		if p.LiveConsumerRef == "" {
			return fmt.Errorf("projection registry: canonical entry %q: LiveConsumerRef is required (PR-1)", p.Name)
		}
		// V5 — ReplayConsumerRef required (PR-2).
		if p.ReplayConsumerRef == "" {
			return fmt.Errorf("projection registry: canonical entry %q: ReplayConsumerRef is required (PR-2)", p.Name)
		}
		// V6 — IntegrationTestRef required (PR-3).
		if p.IntegrationTestRef == "" {
			return fmt.Errorf("projection registry: canonical entry %q: IntegrationTestRef is required (PR-3)", p.Name)
		}
		// V7 — ObservabilitySurface cannot be None for Canonical (PR-4).
		if p.ObservabilitySurface.Kind == SurfaceNone {
			return fmt.Errorf("projection registry: canonical entry %q: ObservabilitySurface cannot be None (PR-4)", p.Name)
		}
		// V16 — SourceEvents non-empty for Canonical.
		if len(p.SourceEvents) == 0 {
			return fmt.Errorf("projection registry: canonical entry %q: SourceEvents must be non-empty", p.Name)
		}
		// V11 — StateProbe required unless AllowIdleWithJustification is set.
		if !p.AllowIdleWithJustification && p.StateProbe == nil {
			return fmt.Errorf("projection registry: canonical entry %q: StateProbe is required unless AllowIdleWithJustification is set (PR-5)", p.Name)
		}
	} else {
		// Advisory-specific:
		// V8 — Advisory with SurfaceNone requires Justification.
		if p.ObservabilitySurface.Kind == SurfaceNone && p.ObservabilitySurface.Justification == "" {
			return fmt.Errorf("projection registry: advisory entry %q: ObservabilitySurface=None requires Justification (PR-4)", p.Name)
		}
	}

	return nil
}

// checkIdempotentOrMismatch is called when an entry with the same Name is
// re-registered. Returns nil on an exact match on non-function fields;
// returns an error naming the first mismatched field otherwise. StateProbe
// is intentionally excluded — Go does not support function equality.
//
// Field comparison order is the order declared in CanonicalProjection with
// the exception that Name is pre-matched by the caller.
func checkIdempotentOrMismatch(existing, incoming CanonicalProjection) error {
	name := existing.Name
	mismatch := func(field string, a, b interface{}) error {
		return fmt.Errorf(
			"projection registry: entry %q: Register called with different %s: existing %v, new %v",
			name, field, a, b,
		)
	}
	if existing.Package != incoming.Package {
		return mismatch("Package", existing.Package, incoming.Package)
	}
	if existing.StoreType != incoming.StoreType {
		return mismatch("StoreType", existing.StoreType, incoming.StoreType)
	}
	if existing.Classification != incoming.Classification {
		return mismatch("Classification", existing.Classification, incoming.Classification)
	}
	if !reflect.DeepEqual(existing.SourceEvents, incoming.SourceEvents) {
		return mismatch("SourceEvents", existing.SourceEvents, incoming.SourceEvents)
	}
	if existing.LiveConsumerRef != incoming.LiveConsumerRef {
		return mismatch("LiveConsumerRef", existing.LiveConsumerRef, incoming.LiveConsumerRef)
	}
	if existing.ReplayConsumerRef != incoming.ReplayConsumerRef {
		return mismatch("ReplayConsumerRef", existing.ReplayConsumerRef, incoming.ReplayConsumerRef)
	}
	if existing.ObservabilitySurface != incoming.ObservabilitySurface {
		return mismatch("ObservabilitySurface", existing.ObservabilitySurface, incoming.ObservabilitySurface)
	}
	if existing.IntegrationTestRef != incoming.IntegrationTestRef {
		return mismatch("IntegrationTestRef", existing.IntegrationTestRef, incoming.IntegrationTestRef)
	}
	if existing.Owner != incoming.Owner {
		return mismatch("Owner", existing.Owner, incoming.Owner)
	}
	if existing.CreatedAt != incoming.CreatedAt {
		return mismatch("CreatedAt", existing.CreatedAt, incoming.CreatedAt)
	}
	if existing.AllowIdleWithJustification != incoming.AllowIdleWithJustification {
		return mismatch("AllowIdleWithJustification",
			existing.AllowIdleWithJustification, incoming.AllowIdleWithJustification)
	}
	if existing.IdleJustification != incoming.IdleJustification {
		return mismatch("IdleJustification", existing.IdleJustification, incoming.IdleJustification)
	}
	if existing.Subcategory != incoming.Subcategory {
		return mismatch("Subcategory", existing.Subcategory, incoming.Subcategory)
	}
	// StateProbe intentionally excluded: Go does not support function equality.
	// First-registered probe is retained.
	return nil
}
