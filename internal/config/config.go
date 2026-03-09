// Package config provides centralized, runtime-configurable parameters for
// the AetherNet protocol. All constants that govern economic, consensus, and
// network behavior can be overridden here without recompiling the binary.
//
// Precedence (highest wins): env vars > config file > DefaultConfig().
//
// Usage:
//
//	cfg, err := config.LoadFromFile(path)  // path="" → pure defaults
//	config.LoadFromEnv(cfg)                // apply AETHERNET_* overrides
//	// then pass cfg to buildStack / startStack
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Duration wraps time.Duration with JSON string marshaling.
// Values use Go duration notation: "10m", "30s", "1h".
// An extended "Nd" suffix is also accepted: "7d" = 7 × 24 h.
type Duration struct {
	time.Duration
}

// MarshalJSON serialises the duration as a quoted Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON parses a quoted duration string, accepting the "d" extension.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := parseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// parseDuration extends time.ParseDuration with an "Nd" suffix (1d = 24h).
func parseDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		days, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("config: invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// FeesConfig controls protocol fee distribution.
type FeesConfig struct {
	// FeeBasisPoints is the settlement fee expressed in basis points (10 = 0.1%).
	FeeBasisPoints uint64 `json:"fee_basis_points"`
	// FeeValidatorShare is the percentage of each fee credited to the validating agent.
	FeeValidatorShare uint64 `json:"fee_validator_share"`
	// FeeTreasuryShare is the percentage credited to the protocol treasury.
	FeeTreasuryShare uint64 `json:"fee_treasury_share"`
}

// StakingConfig controls stake-decay and trust-level progression.
type StakingConfig struct {
	// DecayPeriodDays is the inactivity period (days) after which decay applies.
	DecayPeriodDays int `json:"decay_period_days"`
	// DecayTasksPenalty is the effective-task reduction applied per inactive period.
	DecayTasksPenalty uint64 `json:"decay_tasks_penalty"`
}

// TasksConfig controls the task-marketplace lifecycle.
type TasksConfig struct {
	// MinTaskBudget is the minimum budget (micro-AET) for tasks posted via the API.
	MinTaskBudget uint64 `json:"min_task_budget"`
	// DefaultClaimDeadline is how long a claimer has to submit work before the
	// task is released and their reputation is penalised.
	DefaultClaimDeadline Duration `json:"default_claim_deadline"`
	// MaxCompletedAge is how long completed or cancelled tasks stay in memory
	// before being archived (the store always retains them).
	MaxCompletedAge Duration `json:"max_completed_age"`
}

// EvidenceConfig controls evidence-verification pass thresholds.
type EvidenceConfig struct {
	// PassThreshold is the global minimum score for auto-settlement (0.0–1.0).
	PassThreshold float64 `json:"pass_threshold"`
	// CodePassThreshold is the pass threshold for code / technical tasks.
	CodePassThreshold float64 `json:"code_pass_threshold"`
	// DataPassThreshold is the pass threshold for data / research tasks.
	DataPassThreshold float64 `json:"data_pass_threshold"`
	// ContentPassThreshold is the pass threshold for writing / documentation.
	ContentPassThreshold float64 `json:"content_pass_threshold"`
}

// RouterConfig controls the autonomous task-routing engine.
type RouterConfig struct {
	// NewcomerThreshold is the per-category task count before an agent
	// graduates from the newcomer pool.
	NewcomerThreshold uint64 `json:"newcomer_threshold"`
	// NewcomerAllocation is the target fraction (0–1) of routes reserved for
	// newcomer agents.
	NewcomerAllocation float64 `json:"newcomer_allocation"`
	// MaxNewcomerBudget is the maximum task budget (micro-AET) routable via
	// the newcomer slot. High-value tasks always go to established agents.
	MaxNewcomerBudget uint64 `json:"max_newcomer_budget"`
	// WebhookTimeout is the HTTP client timeout for agent webhook deliveries.
	WebhookTimeout Duration `json:"webhook_timeout"`
	// RoutingInterval controls how often the routing loop runs.
	RoutingInterval Duration `json:"routing_interval"`
}

// OCSConfig controls the Optimistic Capability Settlement engine.
type OCSConfig struct {
	// MaxPendingItems caps the number of simultaneous unresolved OCS events.
	MaxPendingItems int `json:"max_pending_items"`
	// MinStakeRequired is the minimum stake (micro-AET) an event must carry.
	MinStakeRequired uint64 `json:"min_stake_required"`
	// SettlementTimeout is the maximum time an event may remain in Optimistic
	// state before being treated as a failed verification.
	SettlementTimeout Duration `json:"settlement_timeout"`
	// CheckInterval is how often the engine sweeps for expired pending events.
	CheckInterval Duration `json:"check_interval"`
}

// RateLimitConfig controls per-IP rate limiting on the HTTP API.
type RateLimitConfig struct {
	// WriteRatePerSec is the token replenishment rate for write (POST/PUT/DELETE) operations.
	WriteRatePerSec float64 `json:"write_rate_per_sec"`
	// WriteBurst is the burst capacity for write operations.
	WriteBurst int `json:"write_burst"`
	// ReadRatePerSec is the token replenishment rate for read (GET) operations.
	ReadRatePerSec float64 `json:"read_rate_per_sec"`
	// ReadBurst is the burst capacity for read operations.
	ReadBurst int `json:"read_burst"`
	// RegistrationPerHour is the maximum agent registrations per hour per IP
	// (sybil-resistance gate).
	RegistrationPerHour int `json:"registration_per_hour"`
}

// NetworkConfig controls the P2P networking layer.
type NetworkConfig struct {
	// MaxPeers is the maximum number of simultaneous peer connections.
	MaxPeers int `json:"max_peers"`
	// P2PMaxMessageBytes is the per-message read limit on the P2P decoder.
	// Messages larger than this cause the decoder to error and close the conn.
	P2PMaxMessageBytes int64 `json:"p2p_max_message_bytes"`
	// HandshakeTimeout is the deadline for completing the peer challenge-response
	// handshake. Peers that do not finish in time are disconnected.
	HandshakeTimeout Duration `json:"handshake_timeout"`
	// SyncInterval controls how often the node sends MsgRequestSync to all peers.
	SyncInterval Duration `json:"sync_interval"`
	// VoteMaxAge is the maximum age (seconds) of a P2P vote before it is
	// rejected as a potential replay.
	VoteMaxAge int64 `json:"vote_max_age"`
}

// ArchivalConfig controls in-memory ledger eviction.
type ArchivalConfig struct {
	// ArchiveThreshold is the minimum age of a Settled/Adjusted entry before
	// it may be evicted from the in-memory cache.
	ArchiveThreshold Duration `json:"archive_threshold"`
	// ArchiveInterval is how often the archival goroutine runs.
	ArchiveInterval Duration `json:"archive_interval"`
}

// ProtocolConfig is the top-level configuration for an AetherNet node.
// Use DefaultConfig to obtain a config pre-populated with all production defaults.
type ProtocolConfig struct {
	Fees      FeesConfig      `json:"fees"`
	Staking   StakingConfig   `json:"staking"`
	Tasks     TasksConfig     `json:"tasks"`
	Evidence  EvidenceConfig  `json:"evidence"`
	Router    RouterConfig    `json:"router"`
	OCS       OCSConfig       `json:"ocs"`
	RateLimit RateLimitConfig `json:"rate_limit"`
	Network   NetworkConfig   `json:"network"`
	Archival  ArchivalConfig  `json:"archival"`
}

// DefaultConfig returns a ProtocolConfig pre-populated with all current
// production defaults. All values exactly match the hardcoded constants in
// their respective packages, so the protocol behaves identically whether
// this config is wired in or not.
func DefaultConfig() *ProtocolConfig {
	return &ProtocolConfig{
		Fees: FeesConfig{
			FeeBasisPoints:    10,
			FeeValidatorShare: 80,
			FeeTreasuryShare:  20,
		},
		Staking: StakingConfig{
			DecayPeriodDays:   30,
			DecayTasksPenalty: 25,
		},
		Tasks: TasksConfig{
			MinTaskBudget:        100_000,
			DefaultClaimDeadline: Duration{10 * time.Minute},
			MaxCompletedAge:      Duration{7 * 24 * time.Hour},
		},
		Evidence: EvidenceConfig{
			PassThreshold:        0.6,
			CodePassThreshold:    0.5,
			DataPassThreshold:    0.5,
			ContentPassThreshold: 0.5,
		},
		Router: RouterConfig{
			NewcomerThreshold:  10,
			NewcomerAllocation: 0.20,
			MaxNewcomerBudget:  5_000_000,
			WebhookTimeout:     Duration{5 * time.Second},
			RoutingInterval:    Duration{5 * time.Second},
		},
		OCS: OCSConfig{
			MaxPendingItems:   10000,
			MinStakeRequired:  1000,
			SettlementTimeout: Duration{30 * time.Second},
			CheckInterval:     Duration{5 * time.Second},
		},
		RateLimit: RateLimitConfig{
			WriteRatePerSec:     10,
			WriteBurst:          50,
			ReadRatePerSec:      30,
			ReadBurst:           100,
			RegistrationPerHour: 5,
		},
		Network: NetworkConfig{
			MaxPeers:           50,
			P2PMaxMessageBytes: 4 * 1024 * 1024,
			HandshakeTimeout:   Duration{30 * time.Second},
			SyncInterval:       Duration{10 * time.Second},
			VoteMaxAge:         60,
		},
		Archival: ArchivalConfig{
			ArchiveThreshold: Duration{7 * 24 * time.Hour},
			ArchiveInterval:  Duration{time.Hour},
		},
	}
}

// LoadFromFile reads a ProtocolConfig from a JSON file at path.
// Fields missing from the file retain their DefaultConfig() values.
// Returns DefaultConfig() when path is empty (no file configured).
func LoadFromFile(path string) (*ProtocolConfig, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// LoadFromEnv applies AETHERNET_* environment variable overrides to cfg.
// Only variables that are explicitly set (non-empty) override the corresponding
// field; unset variables leave the field unchanged.
//
// Supported variables:
//
//	AETHERNET_FEE_BASIS_POINTS        → Fees.FeeBasisPoints
//	AETHERNET_FEE_VALIDATOR_SHARE     → Fees.FeeValidatorShare
//	AETHERNET_FEE_TREASURY_SHARE      → Fees.FeeTreasuryShare
//	AETHERNET_DECAY_PERIOD_DAYS       → Staking.DecayPeriodDays
//	AETHERNET_DECAY_TASKS_PENALTY     → Staking.DecayTasksPenalty
//	AETHERNET_MIN_TASK_BUDGET         → Tasks.MinTaskBudget
//	AETHERNET_CLAIM_DEADLINE          → Tasks.DefaultClaimDeadline
//	AETHERNET_MAX_COMPLETED_AGE       → Tasks.MaxCompletedAge
//	AETHERNET_EVIDENCE_PASS_THRESHOLD → Evidence.PassThreshold
//	AETHERNET_CODE_PASS_THRESHOLD     → Evidence.CodePassThreshold
//	AETHERNET_DATA_PASS_THRESHOLD     → Evidence.DataPassThreshold
//	AETHERNET_CONTENT_PASS_THRESHOLD  → Evidence.ContentPassThreshold
//	AETHERNET_NEWCOMER_THRESHOLD      → Router.NewcomerThreshold
//	AETHERNET_NEWCOMER_ALLOCATION     → Router.NewcomerAllocation
//	AETHERNET_MAX_NEWCOMER_BUDGET     → Router.MaxNewcomerBudget
//	AETHERNET_WEBHOOK_TIMEOUT         → Router.WebhookTimeout
//	AETHERNET_ROUTING_INTERVAL        → Router.RoutingInterval
//	AETHERNET_OCS_MAX_PENDING         → OCS.MaxPendingItems
//	AETHERNET_OCS_MIN_STAKE           → OCS.MinStakeRequired
//	AETHERNET_OCS_SETTLEMENT_TIMEOUT  → OCS.SettlementTimeout
//	AETHERNET_OCS_CHECK_INTERVAL      → OCS.CheckInterval
//	AETHERNET_WRITE_RATE              → RateLimit.WriteRatePerSec
//	AETHERNET_WRITE_BURST             → RateLimit.WriteBurst
//	AETHERNET_READ_RATE               → RateLimit.ReadRatePerSec
//	AETHERNET_READ_BURST              → RateLimit.ReadBurst
//	AETHERNET_REG_PER_HOUR            → RateLimit.RegistrationPerHour
//	AETHERNET_MAX_PEERS               → Network.MaxPeers
//	AETHERNET_MAX_MSG_BYTES           → Network.P2PMaxMessageBytes
//	AETHERNET_HANDSHAKE_TIMEOUT       → Network.HandshakeTimeout
//	AETHERNET_SYNC_INTERVAL           → Network.SyncInterval
//	AETHERNET_VOTE_MAX_AGE            → Network.VoteMaxAge
//	AETHERNET_ARCHIVE_THRESHOLD       → Archival.ArchiveThreshold
//	AETHERNET_ARCHIVE_INTERVAL        → Archival.ArchiveInterval
func LoadFromEnv(cfg *ProtocolConfig) {
	setUint64 := func(key string, dest *uint64) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				*dest = n
			}
		}
	}
	setInt := func(key string, dest *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dest = n
			}
		}
	}
	setInt64 := func(key string, dest *int64) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dest = n
			}
		}
	}
	setFloat64 := func(key string, dest *float64) {
		if v := os.Getenv(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				*dest = f
			}
		}
	}
	setDuration := func(key string, dest *Duration) {
		if v := os.Getenv(key); v != "" {
			if d, err := parseDuration(v); err == nil {
				dest.Duration = d
			}
		}
	}

	// Fees
	setUint64("AETHERNET_FEE_BASIS_POINTS", &cfg.Fees.FeeBasisPoints)
	setUint64("AETHERNET_FEE_VALIDATOR_SHARE", &cfg.Fees.FeeValidatorShare)
	setUint64("AETHERNET_FEE_TREASURY_SHARE", &cfg.Fees.FeeTreasuryShare)

	// Staking
	setInt("AETHERNET_DECAY_PERIOD_DAYS", &cfg.Staking.DecayPeriodDays)
	setUint64("AETHERNET_DECAY_TASKS_PENALTY", &cfg.Staking.DecayTasksPenalty)

	// Tasks
	setUint64("AETHERNET_MIN_TASK_BUDGET", &cfg.Tasks.MinTaskBudget)
	setDuration("AETHERNET_CLAIM_DEADLINE", &cfg.Tasks.DefaultClaimDeadline)
	setDuration("AETHERNET_MAX_COMPLETED_AGE", &cfg.Tasks.MaxCompletedAge)

	// Evidence
	setFloat64("AETHERNET_EVIDENCE_PASS_THRESHOLD", &cfg.Evidence.PassThreshold)
	setFloat64("AETHERNET_CODE_PASS_THRESHOLD", &cfg.Evidence.CodePassThreshold)
	setFloat64("AETHERNET_DATA_PASS_THRESHOLD", &cfg.Evidence.DataPassThreshold)
	setFloat64("AETHERNET_CONTENT_PASS_THRESHOLD", &cfg.Evidence.ContentPassThreshold)

	// Router
	setUint64("AETHERNET_NEWCOMER_THRESHOLD", &cfg.Router.NewcomerThreshold)
	setFloat64("AETHERNET_NEWCOMER_ALLOCATION", &cfg.Router.NewcomerAllocation)
	setUint64("AETHERNET_MAX_NEWCOMER_BUDGET", &cfg.Router.MaxNewcomerBudget)
	setDuration("AETHERNET_WEBHOOK_TIMEOUT", &cfg.Router.WebhookTimeout)
	setDuration("AETHERNET_ROUTING_INTERVAL", &cfg.Router.RoutingInterval)

	// OCS
	setInt("AETHERNET_OCS_MAX_PENDING", &cfg.OCS.MaxPendingItems)
	setUint64("AETHERNET_OCS_MIN_STAKE", &cfg.OCS.MinStakeRequired)
	setDuration("AETHERNET_OCS_SETTLEMENT_TIMEOUT", &cfg.OCS.SettlementTimeout)
	setDuration("AETHERNET_OCS_CHECK_INTERVAL", &cfg.OCS.CheckInterval)

	// RateLimit
	setFloat64("AETHERNET_WRITE_RATE", &cfg.RateLimit.WriteRatePerSec)
	setInt("AETHERNET_WRITE_BURST", &cfg.RateLimit.WriteBurst)
	setFloat64("AETHERNET_READ_RATE", &cfg.RateLimit.ReadRatePerSec)
	setInt("AETHERNET_READ_BURST", &cfg.RateLimit.ReadBurst)
	setInt("AETHERNET_REG_PER_HOUR", &cfg.RateLimit.RegistrationPerHour)

	// Network
	setInt("AETHERNET_MAX_PEERS", &cfg.Network.MaxPeers)
	setInt64("AETHERNET_MAX_MSG_BYTES", &cfg.Network.P2PMaxMessageBytes)
	setDuration("AETHERNET_HANDSHAKE_TIMEOUT", &cfg.Network.HandshakeTimeout)
	setDuration("AETHERNET_SYNC_INTERVAL", &cfg.Network.SyncInterval)
	setInt64("AETHERNET_VOTE_MAX_AGE", &cfg.Network.VoteMaxAge)

	// Archival
	setDuration("AETHERNET_ARCHIVE_THRESHOLD", &cfg.Archival.ArchiveThreshold)
	setDuration("AETHERNET_ARCHIVE_INTERVAL", &cfg.Archival.ArchiveInterval)
}
