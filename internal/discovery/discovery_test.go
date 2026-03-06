package discovery_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
)

// newTestEngine creates a discovery engine pre-populated with listings and
// optional completion records. completions maps agentID → []category for
// successful task completions (each adds one 100%-score record).
func newTestEngine(
	listings []*registry.ServiceListing,
	completions map[crypto.AgentID][]string,
) *discovery.Engine {
	reg := registry.New()
	for _, l := range listings {
		reg.Register(l)
	}
	rm := reputation.NewReputationManager()
	for agentID, cats := range completions {
		for _, cat := range cats {
			rm.RecordCompletion(agentID, cat, 1000, 1.0, 10.0)
		}
	}
	return discovery.NewEngine(reg, rm)
}

func TestFindAgents_RelevanceRanking(t *testing.T) {
	agent1 := crypto.AgentID("agent-nlp")
	agent2 := crypto.AgentID("agent-image")
	listings := []*registry.ServiceListing{
		{
			AgentID:     agent1,
			Name:        "NLP text analysis",
			Description: "machine learning natural language processing text",
			Category:    "ml",
			PriceAET:    100,
			Active:      true,
		},
		{
			AgentID:     agent2,
			Name:        "Image classifier",
			Description: "computer vision image recognition processing",
			Category:    "ml",
			PriceAET:    100,
			Active:      true,
		},
	}
	eng := newTestEngine(listings, nil)

	matches := eng.FindAgents("machine learning natural language text", "", 0, 0, 0)

	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	// agent1 contains all query words; agent2 contains none — should rank first.
	if matches[0].AgentID != agent1 {
		t.Errorf("expected agent1 to rank first (higher relevance), got %s", matches[0].AgentID)
	}
	if matches[0].RelevanceScore <= matches[1].RelevanceScore {
		t.Errorf("expected agent1 relevance %.3f > agent2 relevance %.3f",
			matches[0].RelevanceScore, matches[1].RelevanceScore)
	}
}

func TestFindAgents_BudgetFilter(t *testing.T) {
	cheap := crypto.AgentID("agent-cheap")
	expensive := crypto.AgentID("agent-expensive")
	listings := []*registry.ServiceListing{
		{AgentID: cheap, Name: "Budget writer", Category: "writing", PriceAET: 100, Active: true},
		{AgentID: expensive, Name: "Premium writer", Category: "writing", PriceAET: 500, Active: true},
	}
	eng := newTestEngine(listings, nil)

	matches := eng.FindAgents("", "writing", 200, 0, 0)

	if len(matches) != 1 {
		t.Fatalf("expected 1 match (budget filter), got %d", len(matches))
	}
	if matches[0].AgentID != cheap {
		t.Errorf("expected cheap agent, got %s", matches[0].AgentID)
	}
}

func TestFindAgents_ReputationFilter(t *testing.T) {
	veteran := crypto.AgentID("agent-veteran")
	newbie := crypto.AgentID("agent-newbie")
	listings := []*registry.ServiceListing{
		{AgentID: veteran, Name: "Veteran coder", Category: "code", PriceAET: 100, Active: true},
		{AgentID: newbie, Name: "Newbie coder", Category: "code", PriceAET: 100, Active: true},
	}
	// Give veteran 10 completions → OverallScore = 1.0 × (10/100) × 100 = 10.0.
	completions := map[crypto.AgentID][]string{
		veteran: {"code", "code", "code", "code", "code", "code", "code", "code", "code", "code"},
	}
	eng := newTestEngine(listings, completions)

	// minReputation = 5.0 — veteran (10.0) passes, newbie (0.0) is filtered out.
	matches := eng.FindAgents("", "code", 0, 5.0, 0)

	if len(matches) != 1 {
		t.Fatalf("expected 1 match (reputation filter), got %d", len(matches))
	}
	if matches[0].AgentID != veteran {
		t.Errorf("expected veteran agent, got %s", matches[0].AgentID)
	}
}

func TestFindAgents_CategoryFilter(t *testing.T) {
	writer := crypto.AgentID("agent-writer")
	coder := crypto.AgentID("agent-coder")
	listings := []*registry.ServiceListing{
		{AgentID: writer, Name: "Essay writer", Category: "writing", PriceAET: 50, Active: true},
		{AgentID: coder, Name: "Go developer", Category: "code", PriceAET: 200, Active: true},
	}
	eng := newTestEngine(listings, nil)

	matches := eng.FindAgents("", "writing", 0, 0, 0)

	if len(matches) != 1 {
		t.Fatalf("expected 1 match (category filter), got %d", len(matches))
	}
	if matches[0].AgentID != writer {
		t.Errorf("expected writer agent, got %s", matches[0].AgentID)
	}
}

func TestFindAgents_EmptyQuery(t *testing.T) {
	listings := []*registry.ServiceListing{
		{AgentID: "agent-a", Name: "Service A", Category: "ml", PriceAET: 10, Active: true},
		{AgentID: "agent-b", Name: "Service B", Category: "ml", PriceAET: 20, Active: true},
		{AgentID: "agent-c", Name: "Service C", Category: "ml", PriceAET: 30, Active: true},
	}
	eng := newTestEngine(listings, nil)

	matches := eng.FindAgents("", "", 0, 0, 0)

	if len(matches) != 3 {
		t.Fatalf("expected 3 matches for empty query, got %d", len(matches))
	}
	// All matches should have neutral relevance (0.5).
	for _, m := range matches {
		if m.RelevanceScore != 0.5 {
			t.Errorf("expected relevance 0.5 for empty query, got %.3f for %s", m.RelevanceScore, m.AgentID)
		}
	}
}

func TestFindAgents_Limit(t *testing.T) {
	listings := []*registry.ServiceListing{
		{AgentID: "a1", Name: "Alpha", Category: "ml", PriceAET: 10, Active: true},
		{AgentID: "a2", Name: "Beta", Category: "ml", PriceAET: 20, Active: true},
		{AgentID: "a3", Name: "Gamma", Category: "ml", PriceAET: 30, Active: true},
		{AgentID: "a4", Name: "Delta", Category: "ml", PriceAET: 40, Active: true},
		{AgentID: "a5", Name: "Epsilon", Category: "ml", PriceAET: 50, Active: true},
	}
	eng := newTestEngine(listings, nil)

	matches := eng.FindAgents("", "", 0, 0, 2)

	if len(matches) != 2 {
		t.Fatalf("expected 2 matches (limit=2), got %d", len(matches))
	}
}

func TestComputeRelevance(t *testing.T) {
	eng := newTestEngine([]*registry.ServiceListing{
		{
			AgentID:     "target",
			Name:        "Go code review",
			Description: "expert golang code review security audit",
			Tags:        []string{"golang", "security", "audit"},
			Category:    "code",
			PriceAET:    100,
			Active:      true,
		},
	}, nil)

	// Query matching all major words — should return high relevance.
	highMatches := eng.FindAgents("golang code review security audit", "code", 0, 0, 0)
	if len(highMatches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(highMatches))
	}
	if highMatches[0].RelevanceScore < 0.8 {
		t.Errorf("expected high relevance (>=0.8), got %.3f", highMatches[0].RelevanceScore)
	}

	// Query with no matching words — should return zero relevance.
	noMatches := eng.FindAgents("python data science pandas", "code", 0, 0, 0)
	if len(noMatches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(noMatches))
	}
	if noMatches[0].RelevanceScore != 0 {
		t.Errorf("expected zero relevance for unrelated query, got %.3f", noMatches[0].RelevanceScore)
	}
}
