// scoring.go implements peer quality tracking for the Fast Path.
//
// Scores are used to prefer higher-quality peers for body fetch, repair
// requests, and relay target selection. Scores are purely advisory — they
// do NOT affect consensus, settlement, or ledger semantics.
//
// Score model:
//   - Base score starts at 100.
//   - Positive events increase the score (capped at MaxScore).
//   - Negative events decrease the score (floored at 0).
//   - Peers below MinUsableScore are deprioritized for fetch/repair.
package network

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// Score constants.
const (
	BaseScore      int64 = 100
	MaxScore       int64 = 1000
	MinUsableScore int64 = 20
)

// Score event weights.
const (
	ScoreValidHeader     int64 = 2  // received a useful, novel header
	ScoreValidBody       int64 = 5  // received a valid body matching commitment
	ScoreValidRepair     int64 = 5  // received useful repair response
	ScoreDuplicateHeader int64 = -1 // received a duplicate header (mild spam)
	ScoreInvalidBody     int64 = -20 // body failed commitment verification
	ScoreInvalidSig      int64 = -30 // event failed signature verification
	ScoreDuplicateSpam   int64 = -5  // excessive duplicates in short window
	ScoreTimeout         int64 = -3  // body or repair request timed out
)

// PeerScore tracks quality metrics for a single peer. Safe for concurrent use.
type PeerScore struct {
	score atomic.Int64

	mu              sync.Mutex
	validHeaders    int64
	duplicateHeaders int64
	validBodies     int64
	invalidBodies   int64
	validRepairs    int64
	invalidSigs     int64
	timeouts        int64
}

// NewPeerScore creates a PeerScore initialized to BaseScore.
func NewPeerScore() *PeerScore {
	ps := &PeerScore{}
	ps.score.Store(BaseScore)
	return ps
}

// Score returns the current score value.
func (ps *PeerScore) Score() int64 {
	return ps.score.Load()
}

// adjust modifies the score by delta, clamping to [0, MaxScore].
func (ps *PeerScore) adjust(delta int64) {
	for {
		old := ps.score.Load()
		next := old + delta
		if next > MaxScore {
			next = MaxScore
		}
		if next < 0 {
			next = 0
		}
		if ps.score.CompareAndSwap(old, next) {
			return
		}
	}
}

// RecordValidHeader records delivery of a useful novel header.
func (ps *PeerScore) RecordValidHeader() {
	ps.adjust(ScoreValidHeader)
	ps.mu.Lock()
	ps.validHeaders++
	ps.mu.Unlock()
}

// RecordDuplicateHeader records delivery of a duplicate header.
func (ps *PeerScore) RecordDuplicateHeader() {
	ps.adjust(ScoreDuplicateHeader)
	ps.mu.Lock()
	ps.duplicateHeaders++
	ps.mu.Unlock()
}

// RecordValidBody records delivery of a valid body matching commitment.
func (ps *PeerScore) RecordValidBody() {
	ps.adjust(ScoreValidBody)
	ps.mu.Lock()
	ps.validBodies++
	ps.mu.Unlock()
}

// RecordInvalidBody records delivery of a body that failed commitment check.
func (ps *PeerScore) RecordInvalidBody() {
	ps.adjust(ScoreInvalidBody)
	ps.mu.Lock()
	ps.invalidBodies++
	ps.mu.Unlock()
}

// RecordValidRepair records delivery of useful repair response events.
func (ps *PeerScore) RecordValidRepair() {
	ps.adjust(ScoreValidRepair)
	ps.mu.Lock()
	ps.validRepairs++
	ps.mu.Unlock()
}

// RecordInvalidSignature records an event with bad signature from this peer.
func (ps *PeerScore) RecordInvalidSignature() {
	ps.adjust(ScoreInvalidSig)
	ps.mu.Lock()
	ps.invalidSigs++
	ps.mu.Unlock()
}

// RecordTimeout records a timeout waiting for response from this peer.
func (ps *PeerScore) RecordTimeout() {
	ps.adjust(ScoreTimeout)
	ps.mu.Lock()
	ps.timeouts++
	ps.mu.Unlock()
}

// RecordDuplicateSpam records excessive duplicate traffic.
func (ps *PeerScore) RecordDuplicateSpam() {
	ps.adjust(ScoreDuplicateSpam)
}

// IsUsable returns true if the peer's score is above MinUsableScore.
func (ps *PeerScore) IsUsable() bool {
	return ps.score.Load() >= MinUsableScore
}

// Metrics returns a snapshot of the scoring counters.
func (ps *PeerScore) Metrics() map[string]int64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return map[string]int64{
		"score":             ps.score.Load(),
		"valid_headers":     ps.validHeaders,
		"duplicate_headers": ps.duplicateHeaders,
		"valid_bodies":      ps.validBodies,
		"invalid_bodies":    ps.invalidBodies,
		"valid_repairs":     ps.validRepairs,
		"invalid_sigs":      ps.invalidSigs,
		"timeouts":          ps.timeouts,
	}
}

// ── Peer Selection ───────────────────────────────────────────────────────────

// PeersByScore returns peers sorted by score descending (highest first).
// Only includes peers that are usable (score >= MinUsableScore).
func PeersByScore(peers []*Peer) []*Peer {
	usable := make([]*Peer, 0, len(peers))
	for _, p := range peers {
		if p.PeerScore() != nil && p.PeerScore().IsUsable() {
			usable = append(usable, p)
		}
	}
	sort.Slice(usable, func(i, j int) bool {
		return usable[i].PeerScore().Score() > usable[j].PeerScore().Score()
	})
	return usable
}

// BestPeerFor returns the highest-scored V2 peer excluding the given AgentID.
// Returns nil if no usable V2 peer is available.
func BestPeerFor(peers map[crypto.AgentID]*Peer, exclude crypto.AgentID) *Peer {
	var best *Peer
	var bestScore int64 = -1
	for _, p := range peers {
		if p.AgentID == exclude {
			continue
		}
		if !p.SupportsFastPath() {
			continue
		}
		ps := p.PeerScore()
		if ps == nil || !ps.IsUsable() {
			continue
		}
		if ps.Score() > bestScore {
			best = p
			bestScore = ps.Score()
		}
	}
	return best
}
