// Package reputation tracks per-category performance records for AetherNet agents.
//
// An agent's reputation is broken down by task category (e.g. "writing", "code",
// "ml") so that hiring agents can make precision decisions: an agent with 200
// verified summarisation completions at 0.9 avg score is fundamentally different
// from one with 5 random completions.
package reputation

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// CategoryRecord tracks an agent's performance in one task category.
type CategoryRecord struct {
	Category         string  `json:"category"`
	TasksCompleted   uint64  `json:"tasks_completed"`
	TasksFailed      uint64  `json:"tasks_failed"`
	TotalValueEarned uint64  `json:"total_value_earned"`
	AvgScore         float64 `json:"avg_score"`         // average verification score
	AvgDeliveryTime  float64 `json:"avg_delivery_secs"` // average seconds from claim to completion
	LastActive       int64   `json:"last_active"`       // unix timestamp
}

// CompletionRate returns the success rate for this category.
func (cr *CategoryRecord) CompletionRate() float64 {
	total := cr.TasksCompleted + cr.TasksFailed
	if total == 0 {
		return 0
	}
	return float64(cr.TasksCompleted) / float64(total)
}

// AgentReputation holds the full reputation profile for an agent.
type AgentReputation struct {
	AgentID        crypto.AgentID             `json:"agent_id"`
	OverallScore   float64                    `json:"overall_score"`   // 0-100 weighted score
	TotalCompleted uint64                     `json:"total_completed"`
	TotalFailed    uint64                     `json:"total_failed"`
	TotalEarned    uint64                     `json:"total_earned"`
	Categories     map[string]*CategoryRecord `json:"categories"`
	TopCategory    string                     `json:"top_category"` // best category by completion count
	MemberSince    int64                      `json:"member_since"`
}

// reputationStore is the subset of store.Store used by ReputationManager.
// Defining a local interface avoids an import cycle: store → reputation → store.
type reputationStore interface {
	PutReputation(agentID string, data []byte) error
	GetReputation(agentID string) ([]byte, error)
	AllReputations() (map[string][]byte, error)
}

// ReputationManager tracks category-specific reputation for all agents.
// It is safe for concurrent use by multiple goroutines.
type ReputationManager struct {
	mu          sync.RWMutex
	reputations map[crypto.AgentID]*AgentReputation
	store       reputationStore
}

// NewReputationManager creates a new empty ReputationManager.
func NewReputationManager() *ReputationManager {
	return &ReputationManager{
		reputations: make(map[crypto.AgentID]*AgentReputation),
	}
}

// SetStore attaches a persistence backend. After this call all mutations
// write through to the store on every change.
func (rm *ReputationManager) SetStore(s reputationStore) {
	rm.store = s
}

// RecordCompletion records a successful task completion in the given category.
// verificationScore is the evidence quality score (0–1); deliveryTimeSecs is the
// time in seconds from task claim to completion.
func (rm *ReputationManager) RecordCompletion(agentID crypto.AgentID, category string, valueEarned uint64, verificationScore float64, deliveryTimeSecs float64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rep := rm.getOrCreate(agentID)
	cat := rm.getOrCreateCategory(rep, category)

	cat.TasksCompleted++
	cat.TotalValueEarned += valueEarned
	// Rolling average for verification score.
	cat.AvgScore = (cat.AvgScore*float64(cat.TasksCompleted-1) + verificationScore) / float64(cat.TasksCompleted)
	// Rolling average for delivery time.
	cat.AvgDeliveryTime = (cat.AvgDeliveryTime*float64(cat.TasksCompleted-1) + deliveryTimeSecs) / float64(cat.TasksCompleted)
	cat.LastActive = time.Now().Unix()

	rep.TotalCompleted++
	rep.TotalEarned += valueEarned
	rm.updateOverallScore(rep)
	rm.persist(agentID, rep)
}

// RecordFailure records a failed or disputed task in the given category.
func (rm *ReputationManager) RecordFailure(agentID crypto.AgentID, category string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rep := rm.getOrCreate(agentID)
	cat := rm.getOrCreateCategory(rep, category)

	cat.TasksFailed++
	cat.LastActive = time.Now().Unix()

	rep.TotalFailed++
	rm.updateOverallScore(rep)
	rm.persist(agentID, rep)
}

// GetReputation returns the full reputation profile for an agent.
// Returns a zero-value profile when the agent is not yet known.
func (rm *ReputationManager) GetReputation(agentID crypto.AgentID) *AgentReputation {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	rep, ok := rm.reputations[agentID]
	if !ok {
		return &AgentReputation{
			AgentID:    agentID,
			Categories: make(map[string]*CategoryRecord),
		}
	}
	return rep
}

// GetCategoryRecord returns the performance record in a specific category.
// Returns a zero-value record when the agent or category is not yet known.
func (rm *ReputationManager) GetCategoryRecord(agentID crypto.AgentID, category string) *CategoryRecord {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	rep, ok := rm.reputations[agentID]
	if !ok {
		return &CategoryRecord{Category: category}
	}
	cat, ok := rep.Categories[category]
	if !ok {
		return &CategoryRecord{Category: category}
	}
	return cat
}

// RankByCategory returns agents ranked by performance in a category.
// Sorted descending by: TasksCompleted × AvgScore (rewards both volume and quality).
// limit=0 returns all candidates.
func (rm *ReputationManager) RankByCategory(category string, limit int) []*AgentReputation {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	type ranked struct {
		rep   *AgentReputation
		score float64
	}
	var candidates []ranked

	for _, rep := range rm.reputations {
		cat, ok := rep.Categories[category]
		if !ok || cat.TasksCompleted == 0 {
			continue
		}
		rankScore := float64(cat.TasksCompleted) * cat.AvgScore
		candidates = append(candidates, ranked{rep: rep, score: rankScore})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	var results []*AgentReputation
	for i, c := range candidates {
		if limit > 0 && i >= limit {
			break
		}
		results = append(results, c.rep)
	}
	return results
}

// LoadFromStore reconstructs all reputations from a persisted store.
func (rm *ReputationManager) LoadFromStore() error {
	if rm.store == nil {
		return nil
	}
	all, err := rm.store.AllReputations()
	if err != nil {
		return err
	}
	for _, data := range all {
		var rep AgentReputation
		if err := json.Unmarshal(data, &rep); err != nil {
			continue
		}
		rm.reputations[rep.AgentID] = &rep
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers (must be called with rm.mu held)
// ---------------------------------------------------------------------------

func (rm *ReputationManager) getOrCreate(agentID crypto.AgentID) *AgentReputation {
	rep, ok := rm.reputations[agentID]
	if !ok {
		rep = &AgentReputation{
			AgentID:     agentID,
			Categories:  make(map[string]*CategoryRecord),
			MemberSince: time.Now().Unix(),
		}
		rm.reputations[agentID] = rep
	}
	return rep
}

func (rm *ReputationManager) getOrCreateCategory(rep *AgentReputation, category string) *CategoryRecord {
	cat, ok := rep.Categories[category]
	if !ok {
		cat = &CategoryRecord{Category: category}
		rep.Categories[category] = cat
	}
	return cat
}

func (rm *ReputationManager) updateOverallScore(rep *AgentReputation) {
	total := rep.TotalCompleted + rep.TotalFailed
	if total == 0 {
		rep.OverallScore = 0
		rep.TopCategory = ""
		return
	}
	completionRate := float64(rep.TotalCompleted) / float64(total)

	// Weight by volume: more tasks = more confident score. Caps at 100.
	volumeFactor := float64(rep.TotalCompleted)
	if volumeFactor > 100 {
		volumeFactor = 100
	}
	volumeWeight := volumeFactor / 100 // 0 to 1

	rep.OverallScore = completionRate * volumeWeight * 100

	// Find top category (most completions).
	maxTasks := uint64(0)
	for _, cat := range rep.Categories {
		if cat.TasksCompleted > maxTasks {
			maxTasks = cat.TasksCompleted
			rep.TopCategory = cat.Category
		}
	}
}

func (rm *ReputationManager) persist(agentID crypto.AgentID, rep *AgentReputation) {
	if rm.store == nil {
		return
	}
	data, _ := json.Marshal(rep)
	_ = rm.store.PutReputation(string(agentID), data)
}
