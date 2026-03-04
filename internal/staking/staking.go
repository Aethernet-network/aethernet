// Package staking manages agent stake positions and computes trust multipliers.
//
// Staking serves two purposes in AetherNet:
//  1. Skin-in-the-game: every OCS event requires a minimum stake, ensuring agents
//     have economic exposure to their claims.
//  2. Trust amplification: a higher task history multiplies the trust limit an
//     agent can operate under, rewarding reliable participants.
//
// Slashing reduces an agent's staked amount as a penalty for failed verifications.
// The slashed amount is redistributed to the protocol treasury by the OCS engine.
package staking

import (
	"sync"

	"github.com/aethernet/core/internal/crypto"
)

// TrustMultiplier returns the multiplier applied to an agent's staked amount
// to compute their effective trust limit.
//
// Formula: 1 + min(4, tasksCompleted/25), capped at 5×.
//
//	  0 tasks → 1×
//	 25 tasks → 2×
//	 50 tasks → 3×
//	 75 tasks → 4×
//	100+ tasks → 5×
func TrustMultiplier(tasksCompleted uint64) uint64 {
	bonus := tasksCompleted / 25
	if bonus > 4 {
		bonus = 4
	}
	return 1 + bonus
}

// TrustLimit returns the maximum optimistic value an agent may transact under,
// computed as stakedAmount × TrustMultiplier(tasksCompleted).
//
// Overflow is handled conservatively: if the product would exceed uint64 max,
// the maximum uint64 value is returned.
func TrustLimit(stakedAmount uint64, tasksCompleted uint64) uint64 {
	multiplier := TrustMultiplier(tasksCompleted)
	if stakedAmount > 0 && multiplier > (^uint64(0))/stakedAmount {
		return ^uint64(0) // saturate on overflow
	}
	return stakedAmount * multiplier
}

// StakeManager tracks staked amounts per agent and provides slash mechanics.
// It is safe for concurrent use by multiple goroutines.
type StakeManager struct {
	mu     sync.RWMutex
	stakes map[crypto.AgentID]uint64
}

// NewStakeManager returns an empty StakeManager.
func NewStakeManager() *StakeManager {
	return &StakeManager{
		stakes: make(map[crypto.AgentID]uint64),
	}
}

// Stake adds amount to agentID's staked balance.
func (sm *StakeManager) Stake(agentID crypto.AgentID, amount uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stakes[agentID] += amount
}

// Unstake removes amount from agentID's staked balance. Returns false if the
// agent has insufficient stake (no state change occurs in that case).
func (sm *StakeManager) Unstake(agentID crypto.AgentID, amount uint64) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	current := sm.stakes[agentID]
	if current < amount {
		return false
	}
	sm.stakes[agentID] = current - amount
	if sm.stakes[agentID] == 0 {
		delete(sm.stakes, agentID)
	}
	return true
}

// StakedAmount returns the current staked balance for agentID. Returns 0 for
// unknown agents.
func (sm *StakeManager) StakedAmount(agentID crypto.AgentID) uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.stakes[agentID]
}

// Slash removes percentage% of agentID's staked balance as a penalty and returns
// the slashed amount. percentage is clamped to [0, 100].
// If the remaining stake rounds to zero the entry is removed.
func (sm *StakeManager) Slash(agentID crypto.AgentID, percentage uint64) uint64 {
	if percentage > 100 {
		percentage = 100
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	current := sm.stakes[agentID]
	if current == 0 {
		return 0
	}
	slashed := current * percentage / 100
	sm.stakes[agentID] = current - slashed
	if sm.stakes[agentID] == 0 {
		delete(sm.stakes, agentID)
	}
	return slashed
}
