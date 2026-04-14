package projections

import (
	"testing"
)

// T1.1 — Classification zero value is invalid. Ensures validation V3 fires
// if a caller passes a zero-initialized struct. Canonical and Advisory are
// the only legal values.
func TestClassification_ZeroValueIsInvalid(t *testing.T) {
	var zero Classification
	if zero == Canonical {
		t.Fatalf("zero Classification must not equal Canonical")
	}
	if zero == Advisory {
		t.Fatalf("zero Classification must not equal Advisory")
	}
}

// T1.2 — Surface shape: each SurfaceKind must be distinct, and a Surface
// value can carry EndpointPath and Justification.
func TestSurface_Shape(t *testing.T) {
	kinds := map[string]SurfaceKind{
		"None":            SurfaceNone,
		"NodeLocalHTTP":   SurfaceNodeLocalHTTP,
		"CLI":             SurfaceCLI,
		"Health":          SurfaceHealth,
		"PublicAggregate": SurfacePublicAggregate,
	}
	seen := make(map[SurfaceKind]string)
	for name, k := range kinds {
		if prior, ok := seen[k]; ok {
			t.Fatalf("SurfaceKind %s duplicates %s", name, prior)
		}
		seen[k] = name
	}
	s := Surface{
		Kind:          SurfaceNodeLocalHTTP,
		EndpointPath:  "/v1/reputation/self",
		Justification: "",
	}
	if s.Kind != SurfaceNodeLocalHTTP {
		t.Fatalf("Kind not preserved")
	}
	if s.EndpointPath != "/v1/reputation/self" {
		t.Fatalf("EndpointPath not preserved")
	}
	if s.Justification != "" {
		t.Fatalf("Justification not preserved")
	}
}

// T1.3 — EligibilityWindow locked at 3 epochs per plan §16.
func TestEligibilityWindow_Locked(t *testing.T) {
	if EligibilityWindow != 3 {
		t.Fatalf("EligibilityWindow must be 3 per plan §16, got %d", EligibilityWindow)
	}
}
