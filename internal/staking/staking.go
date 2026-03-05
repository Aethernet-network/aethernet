// Package staking manages agent stake positions and computes trust multipliers.
//
// Staking serves two purposes in AetherNet:
//  1. Skin-in-the-game: every OCS event requires a minimum stake, ensuring agents
//     have economic exposure to their claims.
//  2. Trust amplification: a higher task history combined with sufficient time staked
//     multiplies the trust limit an agent can operate under, rewarding reliable and
//     committed participants.
//
// Trust multipliers are time-gated: reaching the next level requires BOTH a minimum
// number of completed tasks AND a minimum number of days staked. This prevents
// colluding agents from rapidly inflating their trust by exchanging fake tasks.
//
// Slashing reduces an agent's staked amount as a penalty for failed verifications.
// The slashed amount is redistributed to the protocol treasury by the OCS engine.
// SlashDefault removes the entire stake for agents that default on transactions.
//
// Reputation decay: agents that are inactive for 30+ days lose DecayTasksPerPeriod
// effective tasks per inactive period, which may reduce their trust multiplier.
package staking

import (
	"sync"
	"time"

	"github.com/aethernet/core/internal/crypto"
)

// Decay constants.
const (
	// DecayDays is the inactivity period after which an agent loses one tier
	// worth of effective tasks.
	DecayDays = 30

	// DecayTasksPerPeriod is the effective-task reduction applied per inactive
	// DecayDays period.
	DecayTasksPerPeriod uint64 = 25
)

// TrustLevel represents a single trust tier with its dual requirements.
type TrustLevel struct {
	Multiplier    uint64
	MinTasks      uint64
	MinDaysStaked uint64 // minimum days since first stake
}

// TrustLevels defines the trust progression. Each level requires meeting BOTH
// the task count AND the days-staked threshold simultaneously.
var TrustLevels = []TrustLevel{
	{Multiplier: 1, MinTasks: 0, MinDaysStaked: 0},    // immediate
	{Multiplier: 2, MinTasks: 25, MinDaysStaked: 30},   // 1 month
	{Multiplier: 3, MinTasks: 50, MinDaysStaked: 60},   // 2 months
	{Multiplier: 4, MinTasks: 75, MinDaysStaked: 90},   // 3 months
	{Multiplier: 5, MinTasks: 100, MinDaysStaked: 120}, // 4 months
}

// EffectiveTasks applies inactivity decay to the raw completed-task count.
//
// An agent that has not transacted in DecayDays days loses DecayTasksPerPeriod
// effective tasks per inactive period. This can reduce their trust multiplier
// until they resume activity.
//
// Returns tasksCompleted unchanged when lastActivityUnix is zero (never recorded)
// or when the agent has been active within the current decay period.
func EffectiveTasks(tasksCompleted uint64, lastActivityUnix int64, now int64) uint64 {
	if lastActivityUnix <= 0 || now <= lastActivityUnix {
		return tasksCompleted
	}
	inactiveDays := uint64((now - lastActivityUnix) / 86400)
	decayPeriods := inactiveDays / DecayDays
	if decayPeriods == 0 {
		return tasksCompleted
	}
	reduction := decayPeriods * DecayTasksPerPeriod
	if reduction >= tasksCompleted {
		return 0
	}
	return tasksCompleted - reduction
}

// TrustMultiplier calculates the trust multiplier for an agent based on BOTH
// completed tasks AND time elapsed since first staking.
//
// Returns 1 when stakedSince is zero or non-positive (not yet staked).
// Returns 1 when now <= stakedSince (clock anomaly guard).
func TrustMultiplier(tasksCompleted uint64, stakedSince int64, now int64) uint64 {
	if stakedSince <= 0 || now <= stakedSince {
		return 1
	}
	daysSinceStake := uint64((now - stakedSince) / 86400)

	achieved := uint64(1)
	for _, level := range TrustLevels {
		if tasksCompleted >= level.MinTasks && daysSinceStake >= level.MinDaysStaked {
			achieved = level.Multiplier
		}
	}
	return achieved
}

// TrustLimit returns the maximum optimistic value an agent may transact under,
// computed as stakedAmount × TrustMultiplier(tasksCompleted, stakedSince, now).
//
// Overflow is handled conservatively: if the product would exceed uint64 max,
// the maximum uint64 value is returned.
func TrustLimit(stakedAmount uint64, tasksCompleted uint64, stakedSince int64, now int64) uint64 {
	multiplier := TrustMultiplier(tasksCompleted, stakedSince, now)
	if stakedAmount > 0 && multiplier > (^uint64(0))/stakedAmount {
		return ^uint64(0) // saturate on overflow
	}
	return stakedAmount * multiplier
}

// TrustLimitFull computes the trust limit with reputation decay applied.
// It first reduces tasksCompleted via EffectiveTasks, then calls TrustLimit.
func TrustLimitFull(stakedAmount uint64, tasksCompleted uint64, stakedSince int64, lastActivity int64, now int64) uint64 {
	effective := EffectiveTasks(tasksCompleted, lastActivity, now)
	return TrustLimit(stakedAmount, effective, stakedSince, now)
}

// ---------------------------------------------------------------------------
// Persistence interface
// ---------------------------------------------------------------------------

// stakeStore is the subset of store.Store used by StakeManager for write-through
// persistence of staking metadata (timestamps). *store.Store satisfies this.
type stakeStore interface {
	PutStakeMeta(agentID crypto.AgentID, stakedSince int64, lastActivity int64) error
	GetStakeMeta(agentID crypto.AgentID) (stakedSince int64, lastActivity int64, err error)
	AllStakeMeta() (map[crypto.AgentID][2]int64, error)
}

// ---------------------------------------------------------------------------
// StakeManager
// ---------------------------------------------------------------------------

// StakeManager tracks staked amounts per agent and provides slash mechanics.
// It is safe for concurrent use by multiple goroutines.
type StakeManager struct {
	mu           sync.RWMutex
	stakes       map[crypto.AgentID]uint64
	stakedSince  map[crypto.AgentID]int64 // Unix timestamp of first stake
	lastActivity map[crypto.AgentID]int64 // Unix timestamp of last transaction
	store        stakeStore               // optional persistence; nil = in-memory only
}

// NewStakeManager returns an empty StakeManager.
func NewStakeManager() *StakeManager {
	return &StakeManager{
		stakes:       make(map[crypto.AgentID]uint64),
		stakedSince:  make(map[crypto.AgentID]int64),
		lastActivity: make(map[crypto.AgentID]int64),
	}
}

// SetStore attaches a persistence backend. After this call Stake, RecordActivity,
// and SlashDefault write through to the store.
func (sm *StakeManager) SetStore(s stakeStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.store = s
}

// LoadFromStore restores stakedSince and lastActivity timestamps from the store.
// Call before using the StakeManager after a node restart.
func (sm *StakeManager) LoadFromStore(s stakeStore) error {
	meta, err := s.AllStakeMeta()
	if err != nil {
		return err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for agentID, ts := range meta {
		if ts[0] != 0 {
			sm.stakedSince[agentID] = ts[0]
		}
		if ts[1] != 0 {
			sm.lastActivity[agentID] = ts[1]
		}
	}
	return nil
}

// Stake adds amount to agentID's staked balance.
// Records the first-stake Unix timestamp on the initial call.
func (sm *StakeManager) Stake(agentID crypto.AgentID, amount uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, exists := sm.stakedSince[agentID]; !exists {
		sm.stakedSince[agentID] = time.Now().Unix()
	}
	sm.stakes[agentID] += amount
	if sm.store != nil {
		_ = sm.store.PutStakeMeta(agentID, sm.stakedSince[agentID], sm.lastActivity[agentID])
	}
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

// StakedSince returns the Unix timestamp when agentID first staked. Returns 0
// for agents that have never staked or after SlashDefault.
func (sm *StakeManager) StakedSince(agentID crypto.AgentID) int64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.stakedSince[agentID]
}

// LastActivity returns the Unix timestamp of agentID's last recorded transaction.
// Returns 0 if no activity has been recorded.
func (sm *StakeManager) LastActivity(agentID crypto.AgentID) int64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastActivity[agentID]
}

// RecordActivity updates the last-activity timestamp for agentID to the current
// wall-clock time. Call on successful transaction settlement.
func (sm *StakeManager) RecordActivity(agentID crypto.AgentID) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastActivity[agentID] = time.Now().Unix()
	if sm.store != nil {
		_ = sm.store.PutStakeMeta(agentID, sm.stakedSince[agentID], sm.lastActivity[agentID])
	}
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

// SlashDefault removes the ENTIRE stake for an agent that defaulted on a
// transaction (e.g., transfer verification rejected). Returns the full slashed
// amount. Also resets the staking timestamp so the agent must restart trust
// accumulation from zero.
func (sm *StakeManager) SlashDefault(agentID crypto.AgentID) uint64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	slashed := sm.stakes[agentID]
	delete(sm.stakes, agentID)
	delete(sm.stakedSince, agentID)  // reset: agent re-earns trust from scratch
	delete(sm.lastActivity, agentID)
	if sm.store != nil {
		_ = sm.store.PutStakeMeta(agentID, 0, 0)
	}
	return slashed
}
