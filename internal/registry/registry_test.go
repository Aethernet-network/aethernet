package registry_test

import (
	"testing"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/registry"
)

// makeID generates a fresh AgentID backed by a real Ed25519 keypair.
func makeID(t *testing.T) crypto.AgentID {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return kp.AgentID()
}

// makeListing builds a ServiceListing for tests.
func makeListing(agentID crypto.AgentID, name, category string, tags []string) *registry.ServiceListing {
	return &registry.ServiceListing{
		AgentID:     agentID,
		Name:        name,
		Description: "A service for " + name,
		Category:    category,
		PriceAET:    1000,
		Tags:        tags,
		Active:      true,
	}
}

func TestRegister_New(t *testing.T) {
	r := registry.New()
	id := makeID(t)
	r.Register(makeListing(id, "Test Service", "research", nil))

	got, ok := r.Get(id)
	if !ok {
		t.Fatal("listing not found after register")
	}
	if got.Name != "Test Service" {
		t.Errorf("name: got %q, want %q", got.Name, "Test Service")
	}
	if got.CreatedAt == 0 {
		t.Error("CreatedAt must be set")
	}
	if got.UpdatedAt == 0 {
		t.Error("UpdatedAt must be set")
	}
	if !got.Active {
		t.Error("listing should be active")
	}
}

func TestRegister_Update(t *testing.T) {
	r := registry.New()
	id := makeID(t)

	r.Register(makeListing(id, "Original", "writing", nil))
	orig, _ := r.Get(id)

	// Second registration updates the listing.
	r.Register(makeListing(id, "Updated", "writing", nil))
	updated, _ := r.Get(id)

	if updated.Name != "Updated" {
		t.Errorf("name: got %q, want Updated", updated.Name)
	}
	// CreatedAt must be preserved from the first registration.
	if updated.CreatedAt != orig.CreatedAt {
		t.Errorf("CreatedAt changed: orig=%d updated=%d", orig.CreatedAt, updated.CreatedAt)
	}
	// UpdatedAt must be >= CreatedAt (monotonically non-decreasing).
	if updated.UpdatedAt < orig.UpdatedAt {
		t.Errorf("UpdatedAt went backwards: %d < %d", updated.UpdatedAt, orig.UpdatedAt)
	}
}

func TestDeactivate(t *testing.T) {
	r := registry.New()
	id := makeID(t)
	r.Register(makeListing(id, "Active Service", "code-review", nil))

	if ok := r.Deactivate(id); !ok {
		t.Fatal("Deactivate must return true for existing listing")
	}

	// Deactivated listing must not appear in search.
	results := r.Search("", "code-review", 0)
	for _, l := range results {
		if l.AgentID == id {
			t.Error("deactivated listing appeared in search results")
		}
	}

	// Get still returns the listing (it exists, just inactive).
	got, ok := r.Get(id)
	if !ok {
		t.Fatal("Get must still return deactivated listing")
	}
	if got.Active {
		t.Error("listing should be inactive after Deactivate")
	}

	// Deactivating non-existent agent returns false.
	if r.Deactivate(makeID(t)) {
		t.Error("Deactivate must return false for unknown agent")
	}
}

func TestSearch_ByQuery(t *testing.T) {
	r := registry.New()
	for _, name := range []string{"PDF Extractor", "Image Analyzer", "Text Summarizer"} {
		r.Register(makeListing(makeID(t), name, "research", nil))
	}

	results := r.Search("summar", "", 0)
	if len(results) != 1 {
		t.Fatalf("want 1 result for 'summar', got %d", len(results))
	}
	if results[0].Name != "Text Summarizer" {
		t.Errorf("wrong result: %q", results[0].Name)
	}
}

func TestSearch_ByCategory(t *testing.T) {
	r := registry.New()
	r.Register(makeListing(makeID(t), "Writer",     "writing",     nil))
	r.Register(makeListing(makeID(t), "Coder",      "code-review", nil))
	r.Register(makeListing(makeID(t), "Researcher", "research",    nil))

	results := r.Search("", "writing", 0)
	if len(results) != 1 {
		t.Fatalf("want 1 result for category=writing, got %d", len(results))
	}
	if results[0].Name != "Writer" {
		t.Errorf("wrong result: %q", results[0].Name)
	}
}

func TestSearch_ByTag(t *testing.T) {
	r := registry.New()
	id := makeID(t)
	r.Register(makeListing(id, "ML Agent", "research",
		[]string{"machine-learning", "llm", "classification"}))

	results := r.Search("classification", "", 0)
	if len(results) != 1 {
		t.Fatalf("want 1 result for tag 'classification', got %d", len(results))
	}
	if results[0].AgentID != id {
		t.Error("wrong agent in results")
	}
}

func TestSearch_OnlyActive(t *testing.T) {
	r := registry.New()
	id := makeID(t)
	r.Register(makeListing(id, "Inactive Service", "writing", nil))
	r.Deactivate(id)

	results := r.Search("", "writing", 0)
	if len(results) != 0 {
		t.Errorf("want 0 active results after deactivate, got %d", len(results))
	}
}

func TestCategories(t *testing.T) {
	r := registry.New()
	r.Register(makeListing(makeID(t), "A", "writing",  nil))
	r.Register(makeListing(makeID(t), "B", "writing",  nil))
	r.Register(makeListing(makeID(t), "C", "research", nil))

	cats := r.Categories()
	if cats["writing"] != 2 {
		t.Errorf("writing: got %d, want 2", cats["writing"])
	}
	if cats["research"] != 1 {
		t.Errorf("research: got %d, want 1", cats["research"])
	}
}

func TestSearch_MaxResults(t *testing.T) {
	r := registry.New()
	for i := 0; i < 10; i++ {
		r.Register(makeListing(makeID(t), "Service", "writing", nil))
	}

	results := r.Search("", "writing", 3)
	if len(results) != 3 {
		t.Errorf("want 3 results (maxResults=3), got %d", len(results))
	}
}
