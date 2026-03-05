// Package registry implements the AetherNet agent service discovery layer.
//
// Any agent can publish a ServiceListing describing what it offers, at what
// price, and how to reach it. Consumers search by keyword, category, or tag to
// find the best agent for a task before negotiating and paying via AetherNet events.
//
// Concurrency: a single sync.RWMutex protects all state. Reads run concurrently;
// writes serialise. All returned listings are copies — callers may mutate them freely.
package registry

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// registryStore is the subset of store.Store used for persistence.
// *store.Store from the store package satisfies this interface.
type registryStore interface {
	PutListing(agentID string, data []byte) error
	GetListing(agentID string) ([]byte, error)
	AllListings() (map[string][]byte, error)
}

// ServiceListing describes what an agent offers.
type ServiceListing struct {
	AgentID     crypto.AgentID `json:"agent_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`   // e.g. "writing", "research", "code-review"
	PriceAET    uint64         `json:"price_aet"`  // price per task in micro-AET
	Endpoint    string         `json:"endpoint,omitempty"` // optional direct-call URL
	Tags        []string       `json:"tags,omitempty"`
	CreatedAt   int64          `json:"created_at"` // unix nanoseconds
	UpdatedAt   int64          `json:"updated_at"` // unix nanoseconds
	Active      bool           `json:"active"`
}

// Registry manages service listings for agents in the local node.
type Registry struct {
	mu       sync.RWMutex
	listings map[crypto.AgentID]*ServiceListing
	store    registryStore
}

// New creates an empty Registry ready to accept service listings.
func New() *Registry {
	return &Registry{
		listings: make(map[crypto.AgentID]*ServiceListing),
	}
}

// SetStore attaches a persistence backend. After this call Register and
// Deactivate write through to the store.
func (r *Registry) SetStore(s registryStore) {
	r.store = s
}

// LoadFromStore restores all previously-persisted listings. Call once after
// SetStore and before serving requests.
func (r *Registry) LoadFromStore() error {
	if r.store == nil {
		return nil
	}
	all, err := r.store.AllListings()
	if err != nil {
		return fmt.Errorf("registry: load from store: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, data := range all {
		var l ServiceListing
		if err := json.Unmarshal(data, &l); err != nil {
			return fmt.Errorf("registry: unmarshal listing: %w", err)
		}
		r.listings[l.AgentID] = &l
	}
	return nil
}

// Register adds or updates a service listing. If a listing already exists for
// the agent, CreatedAt is preserved and only UpdatedAt is refreshed.
func (r *Registry) Register(listing *ServiceListing) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixNano()
	listing.UpdatedAt = now
	if existing, ok := r.listings[listing.AgentID]; ok {
		listing.CreatedAt = existing.CreatedAt
	} else {
		listing.CreatedAt = now
	}
	cp := *listing
	r.listings[listing.AgentID] = &cp
	if r.store != nil {
		if data, err := json.Marshal(&cp); err == nil {
			_ = r.store.PutListing(string(listing.AgentID), data)
		}
	}
}

// Deactivate marks a listing as inactive and returns true if the listing existed.
func (r *Registry) Deactivate(agentID crypto.AgentID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.listings[agentID]
	if !ok {
		return false
	}
	l.Active = false
	l.UpdatedAt = time.Now().UnixNano()
	if r.store != nil {
		if data, err := json.Marshal(l); err == nil {
			_ = r.store.PutListing(string(agentID), data)
		}
	}
	return true
}

// Get returns a copy of the listing for agentID, or false if not found.
func (r *Registry) Get(agentID crypto.AgentID) (*ServiceListing, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.listings[agentID]
	if !ok {
		return nil, false
	}
	cp := *l
	return &cp, true
}

// Search finds active listings matching query and category. The query is matched
// case-insensitively against Name, Description, and Tags. category is matched
// case-insensitively against Category. maxResults = 0 means no limit.
func (r *Registry) Search(query string, category string, maxResults int) []*ServiceListing {
	r.mu.RLock()
	defer r.mu.RUnlock()

	q := strings.ToLower(query)
	var results []*ServiceListing

	for _, l := range r.listings {
		if !l.Active {
			continue
		}
		if category != "" && !strings.EqualFold(l.Category, category) {
			continue
		}
		if q != "" {
			match := strings.Contains(strings.ToLower(l.Name), q) ||
				strings.Contains(strings.ToLower(l.Description), q)
			if !match {
				for _, tag := range l.Tags {
					if strings.Contains(strings.ToLower(tag), q) {
						match = true
						break
					}
				}
			}
			if !match {
				continue
			}
		}
		cp := *l
		results = append(results, &cp)
		if maxResults > 0 && len(results) >= maxResults {
			break
		}
	}
	return results
}

// Categories returns a map of category name → active listing count.
func (r *Registry) Categories() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cats := make(map[string]int)
	for _, l := range r.listings {
		if l.Active {
			cats[l.Category]++
		}
	}
	return cats
}

// All returns copies of all active listings in unspecified order.
func (r *Registry) All() []*ServiceListing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []*ServiceListing
	for _, l := range r.listings {
		if l.Active {
			cp := *l
			all = append(all, &cp)
		}
	}
	return all
}

// Count returns the number of active listings.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, l := range r.listings {
		if l.Active {
			count++
		}
	}
	return count
}
