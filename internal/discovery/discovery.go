// Package discovery implements capability-aware agent matching for AetherNet.
//
// The Engine combines service registry listings with category-specific
// reputation data to rank agents against a natural-language task description.
// Composite ranking uses four equally-weighted signals:
//
//   - Relevance (30%)   — fraction of query words found in listing text/tags
//   - Reputation (30%)  — agent's overall reputation score (0–100), normalised
//   - Completion rate (20%) — category-specific success rate for the agent
//   - Price efficiency (20%) — how far below maxBudget the agent's price is
//
// When maxBudget is zero the price-efficiency component is 1.0 (neutral).
// When the query is empty, relevance defaults to 0.5 (neutral).
package discovery

import (
	"sort"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
)

// Match is a ranked discovery result pairing a service listing with its
// computed scores and overall rank.
type Match struct {
	AgentID         crypto.AgentID `json:"agent_id"`
	ServiceName     string         `json:"service_name"`
	Category        string         `json:"category"`
	Price           uint64         `json:"price_aet"`
	RelevanceScore  float64        `json:"relevance_score"`
	ReputationScore float64        `json:"reputation_score"`
	CompletionRate  float64        `json:"completion_rate"`
	TasksCompleted  uint64         `json:"tasks_completed"`
	AvgDelivery     float64        `json:"avg_delivery_secs"`
	OverallRank     float64        `json:"overall_rank"`
}

// Engine matches task requirements to agent capabilities using the service
// registry and reputation data.
type Engine struct {
	serviceRegistry *registry.Registry
	reputationMgr   *reputation.ReputationManager
}

// NewEngine creates a discovery Engine backed by the given service registry
// and reputation manager.
func NewEngine(sr *registry.Registry, rm *reputation.ReputationManager) *Engine {
	return &Engine{serviceRegistry: sr, reputationMgr: rm}
}

// FindAgents matches a natural-language query against all active service
// listings, filters by maxBudget and minReputation, then ranks results by
// composite score. Returns up to limit matches (0 = no limit).
func (e *Engine) FindAgents(query string, category string, maxBudget uint64, minReputation float64, limit int) []*Match {
	listings := e.serviceRegistry.Search("", category, 0)
	queryWords := extractSignificantWords(query)
	var matches []*Match
	for _, listing := range listings {
		if maxBudget > 0 && listing.PriceAET > maxBudget {
			continue
		}
		rep := e.reputationMgr.GetReputation(listing.AgentID)
		if minReputation > 0 && rep.OverallScore < minReputation {
			continue
		}
		relevance := computeRelevance(queryWords, listing)
		catRep := e.reputationMgr.GetCategoryRecord(listing.AgentID, listing.Category)
		priceEfficiency := 1.0
		if maxBudget > 0 && listing.PriceAET > 0 {
			priceEfficiency = 1.0 - float64(listing.PriceAET)/float64(maxBudget)
			if priceEfficiency < 0 {
				priceEfficiency = 0
			}
		}
		match := &Match{
			AgentID:         listing.AgentID,
			ServiceName:     listing.Name,
			Category:        listing.Category,
			Price:           listing.PriceAET,
			RelevanceScore:  relevance,
			ReputationScore: rep.OverallScore,
			CompletionRate:  catRep.CompletionRate(),
			TasksCompleted:  catRep.TasksCompleted,
			AvgDelivery:     catRep.AvgDeliveryTime,
			OverallRank:     relevance*0.3 + (rep.OverallScore/100)*0.3 + catRep.CompletionRate()*0.2 + priceEfficiency*0.2,
		}
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].OverallRank > matches[j].OverallRank
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// computeRelevance scores a listing against a set of significant query words.
// Returns 0.5 for empty queries (neutral relevance), or the fraction of query
// words found in the listing's name, description, and tags.
func computeRelevance(queryWords []string, listing *registry.ServiceListing) float64 {
	if len(queryWords) == 0 {
		return 0.5
	}
	searchText := strings.ToLower(listing.Name + " " + listing.Description + " " + strings.Join(listing.Tags, " "))
	matched := 0
	for _, word := range queryWords {
		if strings.Contains(searchText, word) {
			matched++
		}
	}
	return float64(matched) / float64(len(queryWords))
}

// extractSignificantWords tokenises text and filters out stop words and short
// tokens. Returns lowercase words of length > 2 that are not common English
// stop words, suitable for word-overlap relevance scoring.
func extractSignificantWords(text string) []string {
	stopWords := map[string]bool{
		"i": true, "need": true, "want": true, "a": true, "an": true, "the": true,
		"to": true, "for": true, "and": true, "or": true, "of": true, "in": true,
		"on": true, "is": true, "it": true, "my": true, "me": true, "do": true,
		"can": true, "this": true, "that": true, "with": true, "from": true,
		"some": true, "find": true, "get": true, "help": true,
	}
	words := strings.Fields(strings.ToLower(text))
	var significant []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) > 2 && !stopWords[w] {
			significant = append(significant, w)
		}
	}
	return significant
}
