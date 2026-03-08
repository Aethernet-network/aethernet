// Package api implements the HTTP REST API for an AetherNet node.
//
// All endpoints use JSON over HTTP; the path prefix is /v1. Responses always
// include a JSON body; errors are returned as {"error": "..."} with an
// appropriate HTTP status code.
//
// The server binds on :8338 by default. It requires a fully-wired node stack —
// DAG, ledgers, identity registry, OCS engine, supply manager, and the node's
// keypair — so that it can construct and sign events on behalf of the node
// operator.
//
// Endpoints:
//
//	POST /v1/agents                      register the node's own agent
//	GET  /v1/agents                      list all known agents
//	GET  /v1/agents/{agent_id}            get an agent's capability fingerprint
//	GET  /v1/agents/{agent_id}/balance    get an agent's spendable balance
//	GET  /v1/agents/{agent_id}/address    get an agent's deposit address
//	GET  /v1/agents/{agent_id}/stake      get an agent's staking info
//	POST /v1/transfer                     submit a Transfer event
//	POST /v1/generation                   submit a Generation event
//	POST /v1/verify                       submit a verification verdict for a pending event
//	GET  /v1/pending                      list all pending OCS items
//	GET  /v1/events/{event_id}            get a DAG event by ID
//	GET  /v1/events/recent?limit=N        most recent N events ordered by timestamp desc
//	GET  /v1/dag/tips                     list current DAG tip event IDs
//	GET  /v1/dag/stats                    DAG statistics for visualization
//	GET  /v1/status                       node health snapshot
//	GET  /v1/economics                    network economics overview
//	GET  /v1/address/{address}            resolve a deposit address to an agent ID
//	POST /v1/stake                        stake tokens for an agent
//	POST /v1/unstake                      unstake tokens for an agent
//	GET  /v1/agents/leaderboard?sort=S    top agents sorted by reputation/balance/tasks
//	GET  /v1/network/activity?hours=N     hourly transaction volume for last N hours
package api

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/metrics"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/platform"
	"github.com/Aethernet-network/aethernet/internal/ratelimit"
	svcregistry "github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/wallet"
)

// onboardingStore is the subset of *store.Store used by the API server to
// persist the onboarding allocation counter across restarts.
type onboardingStore interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

// taskRouterInterface is the subset of *router.Router used by the API server.
// Using a local interface keeps the server testable without a real router.
type taskRouterInterface interface {
	RegisterCapability(cap router.AgentCapability)
	UnregisterCapability(agentID crypto.AgentID)
	SetAvailability(agentID crypto.AgentID, available bool) bool
	RegisteredAgents() []*router.AgentCapability
	RecentRoutes(limit int) []*router.RouteResult
	Stats() map[string]any
}

// Server is the HTTP REST API for an AetherNet node.
//
// It wraps all core node components and exposes them over a JSON API.
// Server implements http.Handler so it can be mounted in httptest.NewServer
// for tests without binding a real TCP port.
type Server struct {
	dag        *dag.DAG
	transfer   *ledger.TransferLedger
	generation *ledger.GenerationLedger
	registry   *identity.Registry
	engine     *ocs.Engine
	supply     *ledger.SupplyManager
	node       *network.Node // may be nil in tests
	kp         *crypto.KeyPair
	agentID    crypto.AgentID

	// Economics — all optional; set via SetEconomics after construction.
	walletMgr    *wallet.Wallet
	stakeManager *staking.StakeManager
	feeCollector *fees.Collector

	// Service registry — optional; set via SetServiceRegistry after construction.
	svcRegistry *svcregistry.Registry

	// Task marketplace — optional; set via SetTaskManager after construction.
	// When nil, the /v1/tasks endpoints return 501.
	taskMgr   *tasks.TaskManager
	escrowMgr *escrow.Escrow

	// Reputation tracking — optional; set via SetReputationManager after construction.
	// When nil, reputation endpoints return 501 and completion recording is skipped.
	reputationMgr *reputation.ReputationManager

	// Discovery engine — optional; set via SetDiscoveryEngine after construction.
	// When nil, GET /v1/discover returns 501.
	discoveryEngine *discovery.Engine

	// Autonomous task router — optional; set via SetTaskRouter after construction.
	// When nil, /v1/router/* endpoints return 501.
	taskRouter taskRouterInterface

	// Event bus — optional; set via SetEventBus after construction.
	// When non-nil, handlers publish events for real-time WebSocket streaming.
	eventBus *eventbus.Bus

	// Rate limiters — optional; set via SetRateLimiters after construction.
	// When nil, no rate limiting is applied (safe for tests).
	writeLimiter        *ratelimit.Limiter // POST/DELETE/PUT/PATCH endpoints
	readLimiter         *ratelimit.Limiter // GET endpoints
	registrationLimiter *ratelimit.Limiter // agent registration: max 5/hour per IP (sybil resistance)

	// Platform API key manager — optional; set via SetPlatformKeys after construction.
	// When non-nil, X-API-Key headers are validated; requests with invalid keys
	// are rejected with 401. Keyless requests are still accepted (backward compat).
	platformKeys *platform.KeyManager

	// Metrics — optional; set via SetMetrics after construction.
	// When nil, no metrics are recorded (safe for tests).
	metricsReg  *metrics.Registry
	nodeMetrics *metrics.AetherNetMetrics

	startTime time.Time // used by /health for uptime reporting

	// Onboarding tracking. Protected by onboardingMu.
	onboardingMu        sync.Mutex
	onboardingAllocated uint64

	// Persistence store — optional; set via SetStore after construction.
	// When set, the onboarding counter is persisted so it survives restarts.
	store onboardingStore

	mux        *http.ServeMux
	srv        *http.Server
	listenAddr string
}

// NewServer constructs an API Server backed by the provided node components.
// listenAddr is the TCP address to bind when Start is called (e.g. ":8338").
// node may be nil; peer-count and broadcast operations are skipped when nil.
func NewServer(
	listenAddr string,
	d *dag.DAG,
	tl *ledger.TransferLedger,
	gl *ledger.GenerationLedger,
	reg *identity.Registry,
	eng *ocs.Engine,
	sm *ledger.SupplyManager,
	node *network.Node,
	kp *crypto.KeyPair,
) *Server {
	s := &Server{
		dag:        d,
		transfer:   tl,
		generation: gl,
		registry:   reg,
		engine:     eng,
		supply:     sm,
		node:       node,
		kp:         kp,
		agentID:    kp.AgentID(),
		listenAddr: listenAddr,
		startTime:  time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": "AetherNet Testnet", "version": "0.1.0", "docs": "https://github.com/Aethernet-network/aethernet", "endpoints": map[string]string{"status": "/v1/status", "agents": "/v1/agents", "economics": "/v1/economics", "explorer": "/explorer/"}})
	})
	mux.HandleFunc("POST /v1/agents", s.handleRegisterAgent)
	mux.HandleFunc("GET /v1/agents/leaderboard", s.handleLeaderboard)
	mux.HandleFunc("GET /v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /v1/agents/{agent_id}/balance", s.handleGetBalance)
	mux.HandleFunc("GET /v1/agents/{agent_id}/address", s.handleGetAgentAddress)
	mux.HandleFunc("GET /v1/agents/{agent_id}/stake", s.handleGetStake)
	mux.HandleFunc("GET /v1/agents/{agent_id}", s.handleGetAgent)
	mux.HandleFunc("POST /v1/transfer", s.handleTransfer)
	mux.HandleFunc("POST /v1/generation", s.handleGeneration)
	mux.HandleFunc("POST /v1/verify", s.handleVerify)
	mux.HandleFunc("GET /v1/pending", s.handlePending)
	mux.HandleFunc("GET /v1/events/recent", s.handleRecentEvents)
	mux.HandleFunc("GET /v1/events/{event_id}", s.handleGetEvent)
	mux.HandleFunc("GET /v1/dag/tips", s.handleDAGTips)
	mux.HandleFunc("GET /v1/dag/stats", s.handleDAGStats)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/economics", s.handleEconomics)
	mux.HandleFunc("GET /v1/address/{address}", s.handleResolveAddress)
	mux.HandleFunc("POST /v1/stake", s.handleStake)
	mux.HandleFunc("POST /v1/unstake", s.handleUnstake)
	mux.HandleFunc("GET /v1/network/activity", s.handleNetworkActivity)
	mux.HandleFunc("POST /v1/registry", s.handlePostRegistry)
	mux.HandleFunc("GET /v1/registry/search", s.handleSearchRegistry)
	mux.HandleFunc("GET /v1/registry/categories", s.handleRegistryCategories)
	mux.HandleFunc("DELETE /v1/registry/{agent_id}", s.handleDeleteRegistry)
	mux.HandleFunc("GET /v1/registry/{agent_id}", s.handleGetRegistryListing)

	// Task marketplace endpoints.
	// Literal paths (/stats, /agent/{id}) beat wildcards in Go 1.22 routing.
	mux.HandleFunc("POST /v1/tasks", s.handlePostTask)
	mux.HandleFunc("GET /v1/tasks/stats", s.handleTaskStats)
	mux.HandleFunc("GET /v1/tasks/agent/{agent_id}", s.handleAgentTasks)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /v1/tasks/{id}/claim", s.handleClaimTask)
	mux.HandleFunc("POST /v1/tasks/{id}/submit", s.handleSubmitTask)
	mux.HandleFunc("POST /v1/tasks/{id}/approve", s.handleApproveTask)
	mux.HandleFunc("POST /v1/tasks/{id}/dispute", s.handleDisputeTask)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleCancelTask)
	mux.HandleFunc("POST /v1/tasks/{id}/subtask", s.handleCreateSubtask)
	// GET /v1/tasks/subtasks/{id} avoids a routing conflict with
	// GET /v1/tasks/agent/{agent_id} that would arise from {id}/subtasks.
	mux.HandleFunc("GET /v1/tasks/subtasks/{id}", s.handleGetSubtasks)

	// Discovery endpoint — capability-aware agent matching.
	mux.HandleFunc("GET /v1/discover", s.handleDiscover)

	// Reputation endpoints.
	mux.HandleFunc("GET /v1/agents/{id}/reputation", s.handleGetReputation)
	mux.HandleFunc("GET /v1/reputation/rankings", s.handleGetReputationRankings)

	// Developer platform endpoints — API key management and usage stats.
	mux.HandleFunc("POST /v1/platform/keys", s.handleGenerateKey)
	mux.HandleFunc("GET /v1/platform/keys/{key}", s.handleGetKey)
	mux.HandleFunc("DELETE /v1/platform/keys/{key}", s.handleRevokeKey)
	mux.HandleFunc("GET /v1/platform/stats", s.handlePlatformStats)

	// Autonomous task router endpoints.
	mux.HandleFunc("POST /v1/router/register", s.handleRouterRegister)
	mux.HandleFunc("DELETE /v1/router/register/{agent_id}", s.handleRouterUnregister)
	mux.HandleFunc("PUT /v1/router/availability/{agent_id}", s.handleRouterAvailability)
	mux.HandleFunc("GET /v1/router/agents", s.handleRouterAgents)
	mux.HandleFunc("GET /v1/router/routes", s.handleRouterRoutes)
	mux.HandleFunc("GET /v1/router/stats", s.handleRouterStats)

	// WebSocket endpoint for real-time event streaming.
	mux.Handle("GET /v1/ws", s.wsHandler())

	// Monitoring endpoints (no /v1 prefix — standard convention).
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /health", s.handleHealth)

	// Serve the web explorer from ./explorer (dev) or the Docker install path.
	explorerDir := explorerPath()
	if explorerDir != "" {
		mux.Handle("GET /explorer/", http.StripPrefix("/explorer/", http.FileServer(http.Dir(explorerDir))))
	}

	s.mux = mux
	s.srv = &http.Server{Addr: listenAddr, Handler: s}

	return s
}

// SetStore attaches a persistence backend for the onboarding counter. When set,
// the counter is loaded on startup and written through on every allocation so
// the declining-curve state survives node restarts.
// s must satisfy onboardingStore; *store.Store from the store package does so.
func (s *Server) SetStore(st onboardingStore) {
	s.store = st
	if st == nil {
		return
	}
	// Load persisted onboarding counter.
	if data, err := st.GetMeta("onboarding_allocated"); err == nil && len(data) == 8 {
		s.onboardingMu.Lock()
		s.onboardingAllocated = binary.BigEndian.Uint64(data)
		s.onboardingMu.Unlock()
	}
}

// SetEconomics wires optional economics components (wallet, staking, fee collection)
// into the server. Call before Start. All parameters may be nil independently;
// the corresponding endpoints return 501 when the component is absent.
func (s *Server) SetEconomics(w *wallet.Wallet, sm *staking.StakeManager, fc *fees.Collector) {
	s.walletMgr = w
	s.stakeManager = sm
	s.feeCollector = fc
}

// SetServiceRegistry wires the agent service discovery registry into the server.
// Call before Start. When nil, the /v1/registry endpoints return 501.
func (s *Server) SetServiceRegistry(r *svcregistry.Registry) {
	s.svcRegistry = r
}

// SetTaskManager wires the task marketplace manager and escrow system into the
// server. Call before Start. When nil, the /v1/tasks endpoints return 501.
func (s *Server) SetTaskManager(tm *tasks.TaskManager, e *escrow.Escrow) {
	s.taskMgr = tm
	s.escrowMgr = e
}

// SetReputationManager wires reputation tracking into the server. Call before
// Start. When nil, GET /v1/agents/{id}/reputation and /v1/reputation/rankings
// return 501, and task approval/dispute does not record reputation events.
func (s *Server) SetReputationManager(rm *reputation.ReputationManager) {
	s.reputationMgr = rm
}

// SetDiscoveryEngine wires the capability-aware discovery engine into the
// server. Call before Start. When nil, GET /v1/discover returns 501.
func (s *Server) SetDiscoveryEngine(e *discovery.Engine) {
	s.discoveryEngine = e
}

// SetTaskRouter wires the autonomous task routing engine into the server.
// Call before Start. When nil, the /v1/router/* endpoints return 501.
func (s *Server) SetTaskRouter(r taskRouterInterface) {
	s.taskRouter = r
}

// SetEventBus wires the event bus into the server. Call before Start.
// When non-nil, handlers publish typed events for real-time WebSocket streaming
// at GET /v1/ws. When nil, the WebSocket endpoint closes immediately.
func (s *Server) SetEventBus(b *eventbus.Bus) {
	s.eventBus = b
}

// SetRateLimiters wires per-IP rate limiters into the server. Call before Start.
// writeLimiter is applied to POST/DELETE/PUT/PATCH requests; readLimiter to GET
// requests. Either may be nil to disable rate limiting for that class. When not
// called (e.g. in tests), both limiters are nil and no rate limiting is applied.
func (s *Server) SetRateLimiters(writeLimiter, readLimiter *ratelimit.Limiter) {
	s.writeLimiter = writeLimiter
	s.readLimiter = readLimiter
}

// SetRegistrationLimiter wires the agent-registration rate limiter.
// This is a separate, stricter limiter (5 registrations per hour per IP) used
// as a first line of sybil resistance on POST /v1/agents.
// When nil (default, safe for tests), no registration rate limit is applied.
func (s *Server) SetRegistrationLimiter(l *ratelimit.Limiter) {
	s.registrationLimiter = l
}

// SetPlatformKeys wires the developer API key manager into the server.
// Call before Start. When non-nil, requests that include an X-API-Key header
// have that key validated; an invalid or revoked key results in a 401 response.
// Requests without X-API-Key continue to be served normally (backward-compatible).
func (s *Server) SetPlatformKeys(km *platform.KeyManager) {
	s.platformKeys = km
}

// SetMetrics wires a metrics registry and metric set into the server.
// Call before Start. When not called (e.g. in tests), metrics are nil and no
// instrumentation is applied. The /metrics endpoint returns an empty response
// when metricsReg is nil.
func (s *Server) SetMetrics(reg *metrics.Registry, m *metrics.AetherNetMetrics) {
	s.metricsReg = reg
	s.nodeMetrics = m
}

// ServeHTTP implements http.Handler. Applies CORS headers, handles OPTIONS
// preflight, applies per-IP rate limiting (when configured), then dispatches
// to the route mux. Tests that construct the Server via httptest.NewServer(s)
// pass through here without rate limiting because SetRateLimiters is not called
// in tests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS — required for browser-based clients (explorer, developer frontends).
	corsHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// API key validation — backward-compatible: keyless requests are accepted.
	// If an X-API-Key header is present and the key manager is wired in, the
	// key must be known and active; invalid keys are rejected with 401.
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" && s.platformKeys != nil {
		if _, ok := s.platformKeys.Validate(apiKey); !ok {
			writeCodedError(w, http.StatusUnauthorized, "invalid_api_key",
				"invalid or revoked API key", "")
			return
		}
	}

	switch r.Method {
	case http.MethodPost, http.MethodDelete, http.MethodPut, http.MethodPatch:
		if s.writeLimiter != nil && !s.writeLimiter.Allow(ratelimit.ExtractIP(r)) {
			if s.nodeMetrics != nil {
				s.nodeMetrics.RateLimitRejects.Inc()
			}
			w.Header().Set("Retry-After", "1")
			writeCodedError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "")
			return
		}
	default:
		if s.readLimiter != nil && !s.readLimiter.Allow(ratelimit.ExtractIP(r)) {
			if s.nodeMetrics != nil {
				s.nodeMetrics.RateLimitRejects.Inc()
			}
			w.Header().Set("Retry-After", "1")
			writeCodedError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "")
			return
		}
	}

	// Metrics middleware — wraps dispatch to capture latency and error counts.
	if s.nodeMetrics != nil {
		s.nodeMetrics.APIRequests.Inc()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		s.mux.ServeHTTP(sw, r)
		s.nodeMetrics.APILatency.Observe(float64(time.Since(start).Milliseconds()))
		if sw.status >= 400 {
			s.nodeMetrics.APIErrors.Inc()
		}
		return
	}
	s.mux.ServeHTTP(w, r)
}

// statusWriter wraps http.ResponseWriter to capture the HTTP status code written
// by handlers, enabling post-request error counting in the metrics middleware.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// Start binds the TCP listener and serves requests in a background goroutine.
// Returns immediately; use Stop to shut down.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("api: listen %s: %w", s.listenAddr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api: server error", "err", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the HTTP server with a 5-second deadline.
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

type registerAgentRequest struct {
	Capabilities []identity.Capability `json:"capabilities,omitempty"`

	// AgentID is the optional caller-supplied identity: a 64-char hex-encoded
	// Ed25519 public key. When provided, the server registers that agent rather
	// than the node's own identity. Must equal hex(decoded PublicKeyB64) when
	// both fields are present; when PublicKeyB64 is absent the public key is
	// recovered directly from this field (hex → bytes). Omitting both fields
	// falls back to the node's own keypair (backward-compatible default).
	AgentID string `json:"agent_id,omitempty"`

	// PublicKeyB64 is the caller's Ed25519 public key as standard base64
	// (32 raw bytes → ~44 chars). Required when AgentID alone does not
	// already encode the full public key (i.e. when AgentID is not a valid
	// 64-char hex string). Optional when AgentID is provided as hex(pubkey).
	PublicKeyB64 string `json:"public_key_b64,omitempty"`
}

type registerAgentResponse struct {
	AgentID              string `json:"agent_id"`
	FingerprintHash      string `json:"fingerprint_hash"`
	DepositAddress       string `json:"deposit_address,omitempty"`
	OnboardingAllocation uint64 `json:"onboarding_allocation,omitempty"`
	TrustLimit           uint64 `json:"trust_limit,omitempty"`
}

type transferRequest struct {
	// FromAgent is the economic identity of the sender. When provided it is
	// used for balance and trust-limit checks, and as TransferPayload.FromAgent.
	// When absent the node's own identity (s.agentID) is used.
	FromAgent   string          `json:"from_agent,omitempty"`
	ToAgent     string          `json:"to_agent"`
	Amount      uint64          `json:"amount"`
	Currency    string          `json:"currency,omitempty"`
	Memo        string          `json:"memo,omitempty"`
	StakeAmount uint64          `json:"stake_amount"`
	CausalRefs  []event.EventID `json:"causal_refs,omitempty"`
}

type generationRequest struct {
	BeneficiaryAgent string          `json:"beneficiary_agent,omitempty"`
	ClaimedValue     uint64          `json:"claimed_value"`
	EvidenceHash     string          `json:"evidence_hash"`
	TaskDescription  string          `json:"task_description,omitempty"`
	StakeAmount      uint64          `json:"stake_amount"`
	CausalRefs       []event.EventID `json:"causal_refs,omitempty"`
}

type eventIDResponse struct {
	EventID string `json:"event_id"`
}

type balanceResponse struct {
	AgentID  string `json:"agent_id"`
	Balance  uint64 `json:"balance"`
	Currency string `json:"currency"`
}

type statusResponse struct {
	AgentID     string  `json:"agent_id"`
	Version     string  `json:"version"`
	Peers       int     `json:"peers"`
	DAGSize     int     `json:"dag_size"`
	OCSPending  int     `json:"ocs_pending"`
	SupplyRatio float64 `json:"supply_ratio"`
}

type tipsResponse struct {
	Tips []event.EventID `json:"tips"`
}

type verifyRequest struct {
	EventID       event.EventID `json:"event_id"`
	Verdict       bool          `json:"verdict"`
	VerifiedValue uint64        `json:"verified_value,omitempty"`
}

type verifyResponse struct {
	EventID string `json:"event_id"`
	Verdict bool   `json:"verdict"`
	Status  string `json:"status"` // "settled" or "adjusted"
}

type agentAddressResponse struct {
	AgentID string `json:"agent_id"`
	Address string `json:"address"`
}

type stakeInfoResponse struct {
	AgentID         string `json:"agent_id"`
	StakedAmount    uint64 `json:"staked_amount"`
	StakedSince     int64  `json:"staked_since"`
	TrustMultiplier uint64 `json:"trust_multiplier"`
	TrustLimit      uint64 `json:"trust_limit"`
	TasksCompleted  uint64 `json:"tasks_completed"`
	EffectiveTasks  uint64 `json:"effective_tasks"`
	LastActivity    int64  `json:"last_activity"`
	DaysStaked      uint64 `json:"days_staked"`
}

type economicsResponse struct {
	TotalSupply          uint64 `json:"total_supply"`
	CirculatingSupply    uint64 `json:"circulating_supply"`
	OnboardingPoolTotal  uint64 `json:"onboarding_pool_total"`
	OnboardingMaxAgents  uint64 `json:"onboarding_max_agents"`
	OnboardingAllocated  uint64 `json:"onboarding_allocated"`
	TotalCollected       uint64 `json:"total_collected"`
	TotalBurned          uint64 `json:"total_burned"`
	TreasuryAccrued      uint64 `json:"treasury_accrued"`
	TreasuryBalance      uint64 `json:"treasury_balance"`
	FeeBasisPoints       uint64 `json:"fee_basis_points"`
	TotalGeneratedValue  uint64 `json:"total_generated_value"`  // cumulative verified AI output (micro-AET)
}

type resolveAddressResponse struct {
	Address string `json:"address"`
	AgentID string `json:"agent_id"`
}

type recentEventItem struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	AgentID         string `json:"agent_id"`
	CausalTimestamp uint64 `json:"causal_timestamp"`
	StakeAmount     uint64 `json:"stake_amount"`
	SettlementState string `json:"settlement_state"`
	// Enriched fields populated for Transfer events.
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Amount uint64 `json:"amount,omitempty"`
	Memo   string `json:"memo,omitempty"`
}

type dagStatsResponse struct {
	TotalEvents  int            `json:"total_events"`
	EventsByType map[string]int `json:"events_by_type"`
	TipsCount    int            `json:"tips_count"`
	MaxDepth     uint64         `json:"max_depth"`
}

// agentSummaryEntry is returned by GET /v1/agents. It enriches the registry
// fingerprint with live balance and stake data so callers do not need follow-up
// calls to /balance and /stake for each agent.
type agentSummaryEntry struct {
	AgentID         string `json:"agent_id"`
	ReputationScore uint64 `json:"reputation_score"`
	TasksCompleted  uint64 `json:"tasks_completed"`
	Balance         uint64 `json:"balance"`
	StakedAmount    uint64 `json:"staked_amount"`
	TrustLimit      uint64 `json:"trust_limit"`
}

type leaderboardEntry struct {
	Rank            int    `json:"rank"`
	AgentID         string `json:"agent_id"`
	ReputationScore uint64 `json:"reputation_score"`
	TasksCompleted  uint64 `json:"tasks_completed"`
	Balance         uint64 `json:"balance"`
	TrustLimit      uint64 `json:"trust_limit"`
	StakedAmount    uint64 `json:"staked_amount"`
}

type activityBucket struct {
	Hour   string `json:"hour"`
	Volume uint64 `json:"volume"`
	Count  int    `json:"count"`
}

type networkActivityResponse struct {
	Hours   int              `json:"hours"`
	Buckets []activityBucket `json:"buckets"`
}

type registryListingRequest struct {
	AgentID     string   `json:"agent_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	PriceAET    uint64   `json:"price_aet"`
	Endpoint    string   `json:"endpoint,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Active      bool     `json:"active"`
}

type deactivateResponse struct {
	AgentID string `json:"agent_id"`
	Active  bool   `json:"active"`
}

type stakeRequest struct {
	AgentID string `json:"agent_id"`
	Amount  uint64 `json:"amount"`
}

type stakeOpResponse struct {
	AgentID      string `json:"agent_id"`
	StakedAmount uint64 `json:"staked_amount"`
	TrustLimit   uint64 `json:"trust_limit"`
}

type unstakeOpResponse struct {
	AgentID      string `json:"agent_id"`
	StakedAmount uint64 `json:"staked_amount"`
	TrustLimit   uint64 `json:"trust_limit"`
	Success      bool   `json:"success"`
}

// ---------------------------------------------------------------------------
// Task marketplace request / response types
// ---------------------------------------------------------------------------

type postTaskRequest struct {
	PosterID    string `json:"poster_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Budget      uint64 `json:"budget"`
}

type claimTaskRequest struct {
	AgentID   string `json:"agent_id,omitempty"`   // preferred: explicit claimer identity
	ClaimerID string `json:"claimer_id,omitempty"` // legacy alias accepted for compatibility
}

type submitTaskRequest struct {
	ClaimerID  string             `json:"claimer_id,omitempty"`
	ResultHash string             `json:"result_hash"`
	ResultNote string             `json:"result_note,omitempty"`
	ResultURI  string             `json:"result_uri,omitempty"`
	Evidence   *evidence.Evidence `json:"evidence,omitempty"`
}

type approveTaskRequest struct {
	ApproverID string `json:"approver_id,omitempty"`
}

type disputeTaskRequest struct {
	PosterID string `json:"poster_id,omitempty"`
}

type cancelTaskRequest struct {
	PosterID string `json:"poster_id,omitempty"`
}

// ---------------------------------------------------------------------------
// Task marketplace handlers
// ---------------------------------------------------------------------------

// handlePostTask handles POST /v1/tasks. It creates a new task and escrows
// the budget from the poster's balance. Returns 501 when task manager is nil.
func (s *Server) handlePostTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req postTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	posterID := req.PosterID
	if posterID == "" {
		posterID = string(s.agentID)
	}

	// Validate poster has sufficient balance before creating the task.
	if err := s.transfer.BalanceCheck(crypto.AgentID(posterID), req.Budget); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, err := s.taskMgr.PostTask(posterID, req.Title, req.Description, req.Category, req.Budget)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Escrow the budget. Balance was validated above so this should succeed,
	// but TOCTOU is possible; on failure we log and continue (testnet behaviour).
	if err := s.escrowMgr.Hold(task.ID, crypto.AgentID(posterID), req.Budget); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, task)
}

// handleListTasks handles GET /v1/tasks. Supports ?status=&category=&limit= query params.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	status := tasks.TaskStatus(r.URL.Query().Get("status"))
	category := r.URL.Query().Get("category")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	result := s.taskMgr.Search(status, category, limit)
	if result == nil {
		result = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleTaskStats handles GET /v1/tasks/stats.
func (s *Server) handleTaskStats(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.taskMgr.Stats())
}

// handleAgentTasks handles GET /v1/tasks/agent/{agent_id}.
func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	agentID := r.PathValue("agent_id")
	result := s.taskMgr.AgentTasks(agentID)
	if result == nil {
		result = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleGetTask handles GET /v1/tasks/{id}.
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	taskID := r.PathValue("id")
	task, err := s.taskMgr.Get(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleClaimTask handles POST /v1/tasks/{id}/claim.
func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req claimTaskRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Prefer agent_id (the canonical field); fall back to claimer_id (legacy), then the node's own identity.
	claimerID := req.AgentID
	if claimerID == "" {
		claimerID = req.ClaimerID
	}
	if claimerID == "" {
		claimerID = string(s.agentID)
	}

	taskID := r.PathValue("id")
	if err := s.taskMgr.ClaimTask(taskID, crypto.AgentID(claimerID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, _ := s.taskMgr.Get(taskID)
	writeJSON(w, http.StatusOK, task)
}

// handleSubmitTask handles POST /v1/tasks/{id}/submit.
func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req submitTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	claimerID := req.ClaimerID
	if claimerID == "" {
		claimerID = string(s.agentID)
	}

	taskID := r.PathValue("id")

	// If structured evidence is provided, use its hash; otherwise fall back to
	// the legacy result_hash field.
	resultHash := req.ResultHash
	resultNote := req.ResultNote
	resultURI := req.ResultURI
	if req.Evidence != nil {
		if req.Evidence.Hash != "" {
			resultHash = req.Evidence.Hash
		}
		if req.Evidence.Summary != "" {
			resultNote = req.Evidence.Summary
		}
		if req.Evidence.OutputURL != "" {
			resultURI = req.Evidence.OutputURL
		}
	}

	if err := s.taskMgr.SubmitResult(taskID, crypto.AgentID(claimerID), resultHash, resultNote, resultURI); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, _ := s.taskMgr.Get(taskID)
	writeJSON(w, http.StatusOK, task)
}

// handleApproveTask handles POST /v1/tasks/{id}/approve. It releases the escrowed
// budget to the claimer and records task completion in the identity registry.
func (s *Server) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req approveTaskRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	approverID := req.ApproverID
	if approverID == "" {
		approverID = string(s.agentID)
	}

	taskID := r.PathValue("id")

	// Get task before approving to capture claimer_id.
	taskBefore, err := s.taskMgr.Get(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := s.taskMgr.ApproveTask(taskID, crypto.AgentID(approverID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Release escrow to the claimer minus the protocol fee. Best-effort — the task
	// is already approved. Fees are collected via feeCollector if available.
	if taskBefore.ClaimerID != "" {
		fee := fees.CalculateFee(taskBefore.Budget)
		netAmount := taskBefore.Budget - fee
		if err := s.escrowMgr.ReleaseNet(taskID, crypto.AgentID(taskBefore.ClaimerID), netAmount); err != nil {
			slog.Warn("handleApproveTask: escrow release failed", "task_id", taskID, "err", err)
		} else if s.feeCollector != nil && fee > 0 {
			s.feeCollector.CollectFee(taskBefore.Budget, s.agentID, crypto.AgentID(genesis.BucketTreasury))
		}
		// Record task completion in the identity registry (best-effort).
		_ = s.registry.RecordTaskCompletion(
			crypto.AgentID(taskBefore.ClaimerID),
			taskBefore.Budget,
			taskBefore.Category,
		)
	}

	task, _ := s.taskMgr.Get(taskID)

	// Record reputation completion (best-effort).
	if s.reputationMgr != nil && taskBefore.ClaimerID != "" {
		verScore := 0.5 // neutral default when no verification score available
		if task != nil && task.VerificationScore != nil {
			verScore = task.VerificationScore.Overall
		}
		var deliverySecs float64
		if task != nil && task.CompletedAt > 0 && taskBefore.ClaimedAt > 0 {
			deliverySecs = float64(task.CompletedAt-taskBefore.ClaimedAt) / 1e9
		}
		s.reputationMgr.RecordCompletion(
			crypto.AgentID(taskBefore.ClaimerID),
			taskBefore.Category,
			taskBefore.Budget,
			verScore,
			deliverySecs,
		)
	}

	writeJSON(w, http.StatusOK, task)
}

// handleDisputeTask handles POST /v1/tasks/{id}/dispute.
// Note: reputation failure is NOT pre-recorded here. The auto-validator records
// a failure only after resolving the dispute — if the evidence score is below
// the pass threshold. Pre-recording penalises the claimer before any review.
func (s *Server) handleDisputeTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req disputeTaskRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	posterID := req.PosterID
	if posterID == "" {
		posterID = string(s.agentID)
	}

	taskID := r.PathValue("id")

	if err := s.taskMgr.DisputeTask(taskID, crypto.AgentID(posterID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	task, _ := s.taskMgr.Get(taskID)
	writeJSON(w, http.StatusOK, task)
}

// handleCancelTask handles POST /v1/tasks/{id}/cancel. Refunds the escrowed budget
// to the poster.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req cancelTaskRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	posterID := req.PosterID
	if posterID == "" {
		posterID = string(s.agentID)
	}

	taskID := r.PathValue("id")
	if err := s.taskMgr.CancelTask(taskID, crypto.AgentID(posterID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Refund escrow to poster. Best-effort — the task is already cancelled.
	_ = s.escrowMgr.Refund(taskID)

	task, _ := s.taskMgr.Get(taskID)
	writeJSON(w, http.StatusOK, task)
}

// ---------------------------------------------------------------------------
// Task chain and discovery handlers
// ---------------------------------------------------------------------------

type createSubtaskRequest struct {
	ClaimerID   string `json:"claimer_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Budget      uint64 `json:"budget"`
}

// handleCreateSubtask handles POST /v1/tasks/{id}/subtask. The claimer of the
// parent task partitions part of its budget into a new child task. Returns 201
// on success with the created subtask.
func (s *Server) handleCreateSubtask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	var req createSubtaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	claimerID := req.ClaimerID
	if claimerID == "" {
		claimerID = string(s.agentID)
	}

	parentID := r.PathValue("id")
	subtask, err := s.taskMgr.CreateSubtask(parentID, crypto.AgentID(claimerID), req.Title, req.Description, req.Category, req.Budget)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, subtask)
}

// handleGetSubtasks handles GET /v1/tasks/{id}/subtasks. Returns all child
// tasks of the given parent task, ordered by PostedAt ascending.
func (s *Server) handleGetSubtasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}

	taskID := r.PathValue("id")
	parent, err := s.taskMgr.Get(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var subtasks []*tasks.Task
	for _, subID := range parent.SubtaskIDs {
		sub, err := s.taskMgr.Get(subID)
		if err == nil {
			subtasks = append(subtasks, sub)
		}
	}
	if subtasks == nil {
		subtasks = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, subtasks)
}

// handleDiscover handles GET /v1/discover. Query params:
//   - q           — natural-language task description
//   - category    — optional category filter (e.g. "writing", "code")
//   - max_budget  — optional upper price limit in micro-AET
//   - min_reputation — optional minimum overall reputation score (0–100)
//   - limit       — maximum results to return (0 = no limit)
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if s.discoveryEngine == nil {
		writeError(w, http.StatusNotImplemented, "discovery not enabled")
		return
	}

	q := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")
	maxBudget, _ := strconv.ParseUint(r.URL.Query().Get("max_budget"), 10, 64)
	minReputation, _ := strconv.ParseFloat(r.URL.Query().Get("min_reputation"), 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	matches := s.discoveryEngine.FindAgents(q, category, maxBudget, minReputation, limit)
	if matches == nil {
		matches = []*discovery.Match{}
	}
	writeJSON(w, http.StatusOK, matches)
}

// ---------------------------------------------------------------------------
// Reputation handlers
// ---------------------------------------------------------------------------

// handleGetReputation handles GET /v1/agents/{id}/reputation.
// Returns the full category-level reputation profile for an agent.
func (s *Server) handleGetReputation(w http.ResponseWriter, r *http.Request) {
	if s.reputationMgr == nil {
		writeError(w, http.StatusNotImplemented, "reputation tracking not enabled")
		return
	}
	agentID := r.PathValue("id")
	rep := s.reputationMgr.GetReputation(crypto.AgentID(agentID))
	writeJSON(w, http.StatusOK, rep)
}

// handleGetReputationRankings handles GET /v1/reputation/rankings.
// Query params: category (required), limit (optional, default 10).
func (s *Server) handleGetReputationRankings(w http.ResponseWriter, r *http.Request) {
	if s.reputationMgr == nil {
		writeError(w, http.StatusNotImplemented, "reputation tracking not enabled")
		return
	}
	category := r.URL.Query().Get("category")
	if category == "" {
		writeError(w, http.StatusBadRequest, "category query parameter required")
		return
	}
	limit := 10
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			limit = n
		}
	}
	rankings := s.reputationMgr.RankByCategory(category, limit)
	writeJSON(w, http.StatusOK, rankings)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// explorerPath returns the directory to serve the web explorer from, or ""
// if no explorer directory is found. It checks two locations in order:
//  1. ./explorer  — the development / source-tree path
//  2. /usr/local/share/aethernet/explorer — the Docker install path
// handleMetrics serves Prometheus text exposition format on GET /metrics.
// Returns an empty 200 response when no registry has been wired in (tests).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if s.metricsReg != nil {
		_, _ = w.Write([]byte(s.metricsReg.Render()))
	}
}

// handleHealth serves a simple health-check on GET /health.
// Returns HTTP 200 with status "healthy" when the node is operational.
// Returns HTTP 503 with status "degraded" when the node is wired to the
// network but reports zero connected peers.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	peers := 0
	if s.node != nil {
		peers = s.node.PeerCount()
	}

	status := "healthy"
	code := http.StatusOK
	if s.node != nil && peers == 0 {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		"status":         status,
		"uptime_seconds": int64(time.Since(s.startTime).Seconds()),
		"dag_size":       s.dag.Size(),
		"peers":          peers,
	})
}

// clientIPFromRequest extracts the client IP from an HTTP request.
// It honours X-Forwarded-For (set by load balancers / reverse proxies) and
// falls back to r.RemoteAddr. Only the IP portion is returned (port stripped).
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain a comma-separated list; the first entry
		// is the originating client IP.
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		xff = strings.TrimSpace(xff)
		if xff != "" {
			return xff
		}
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}

func explorerPath() string {
	for _, p := range []string{"./explorer", "/usr/local/share/aethernet/explorer"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

// APIError is the standard error response body for all API error responses.
// Error is always present. Code and Details are present for specific error
// conditions to allow programmatic error handling by clients.
type APIError struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// corsHeaders sets CORS headers required for browser-based clients to call the
// API. Applied unconditionally on every response including errors.
func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response with the standard {"error": msg} body.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, APIError{Error: msg})
}

// writeCodedError writes a structured JSON error response including a machine-
// readable error code and optional details string. Use this for errors that
// clients may want to handle programmatically (e.g. trust_limit_exceeded,
// agent_not_found).
func writeCodedError(w http.ResponseWriter, status int, code, msg, details string) {
	writeJSON(w, status, APIError{Error: msg, Code: code, Details: details})
}

// buildCausalRefs returns the causal ref list (falling back to current DAG tips
// when none are requested) and the priorTimestamps map needed by event.New.
func (s *Server) buildCausalRefs(requested []event.EventID) ([]event.EventID, map[event.EventID]uint64) {
	refs := requested
	if len(refs) == 0 {
		refs = s.dag.Tips()
	}
	priorTimestamps := make(map[event.EventID]uint64, len(refs))
	for _, ref := range refs {
		if e, err := s.dag.Get(ref); err == nil {
			priorTimestamps[ref] = e.CausalTimestamp
		}
	}
	return refs, priorTimestamps
}

// submitAndAdd builds, signs, submits to the OCS engine, and adds to the DAG.
// Broadcast is attempted if s.node is non-nil. Returns the event on success.
func (s *Server) submitAndAdd(e *event.Event) error {
	if err := crypto.SignEvent(e, s.kp); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	if err := s.engine.Submit(e); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	if err := s.dag.Add(e); err != nil {
		// Ledger entry exists but DAG rejected — should not occur with valid events.
		slog.Error("api: dag.Add failed after Submit", "event_id", e.ID, "err", err)
		return fmt.Errorf("dag: %w", err)
	}
	if s.node != nil {
		_ = s.node.Broadcast(e)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleRegisterAgent registers the node's own agent in the identity registry.
// If the agent is already registered the existing fingerprint is returned.
// When economics are wired in (SetEconomics was called), the handler also:
//   - Registers a deposit address in the wallet
//   - Grants an onboarding allocation from the ecosystem pool (declining curve)
//   - Auto-stakes the onboarding allocation
//
// Sybil resistance: when a registrationLimiter is configured, this endpoint
// enforces a maximum of 5 registrations per IP per hour. Requests that exceed
// the limit receive HTTP 429 Too Many Requests. This is a first-line defence
// against bulk agent creation from a single source; the declining onboarding
// curve provides a complementary economic deterrent.
func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	// Sybil resistance: check per-IP registration rate limit before doing anything.
	if s.registrationLimiter != nil {
		clientIP := clientIPFromRequest(r)
		if !s.registrationLimiter.Allow(clientIP) {
			writeCodedError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
				"registration rate limit exceeded: max 5 registrations per hour per IP", "")
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeCodedError(w, http.StatusBadRequest, "invalid_request", "invalid request body: "+err.Error(), "")
		return
	}

	// Resolve the agent identity to register.
	//
	// Priority:
	//   1. Caller supplies public_key_b64 (base64-encoded Ed25519, 32 bytes).
	//      The public key is decoded to bytes. agent_id is used as-is when
	//      provided; if omitted it is derived as hex(pubkey). Any non-empty
	//      agent_id is accepted — it need not equal hex(pubkey), allowing
	//      human-readable names like "researcher-agent".
	//   2. Caller supplies agent_id alone without public_key_b64.
	//      It must be a 64-char hex-encoded Ed25519 public key so the raw
	//      key bytes can be recovered (AgentID = hex(pubkey) invariant).
	//   3. Neither field supplied → fall back to the node's own keypair identity
	//      (backward-compatible default; preserves existing single-node behaviour).
	regAgentID := s.agentID
	regPubKey := []byte(s.kp.PublicKey)

	if req.PublicKeyB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(req.PublicKeyB64)
		if err != nil {
			writeCodedError(w, http.StatusBadRequest, "invalid_request",
				"invalid public_key_b64: "+err.Error(), "")
			return
		}
		if len(decoded) != 32 { // ed25519.PublicKeySize
			writeCodedError(w, http.StatusBadRequest, "invalid_request",
				fmt.Sprintf("public_key_b64 must decode to 32 bytes (Ed25519), got %d", len(decoded)), "")
			return
		}
		// Use the caller-supplied agent_id as-is. When absent, derive it from
		// the public key (hex-encoding preserves the cryptographic identity).
		agentIDStr := req.AgentID
		if agentIDStr == "" {
			agentIDStr = hex.EncodeToString(decoded)
		}
		regAgentID = crypto.AgentID(agentIDStr)
		regPubKey = decoded
	} else if req.AgentID != "" {
		// agent_id without public_key_b64: must be hex(ed25519 pubkey) so the
		// raw key bytes can be recovered. Arbitrary names require public_key_b64.
		decoded, err := hex.DecodeString(req.AgentID)
		if err != nil || len(decoded) != 32 {
			writeCodedError(w, http.StatusBadRequest, "invalid_request",
				"agent_id without public_key_b64 must be a 64-char hex-encoded Ed25519 public key", "")
			return
		}
		regAgentID = crypto.AgentID(req.AgentID)
		regPubKey = decoded
	}

	// Snapshot agent count before this registration for the onboarding curve.
	agentCountBefore := uint64(len(s.registry.All(0, 0)))

	fp, err := identity.NewFingerprint(regAgentID, regPubKey, req.Capabilities)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create fingerprint: "+err.Error())
		return
	}

	// When the caller supplied a human-readable agent_id alongside a public_key_b64,
	// treat the agent_id as a display name and store it separately from the
	// canonical cryptographic identity (AgentID = hex(pubkey) or caller-provided).
	// This allows agents to have friendly names while retaining cryptographic anchoring.
	if req.AgentID != "" && req.PublicKeyB64 != "" {
		fp.DisplayName = req.AgentID
	} else if req.AgentID != "" && req.PublicKeyB64 == "" {
		// agent_id without public_key_b64: the id IS the canonical id, no display name.
	}

	if err := s.registry.Register(fp); err != nil {
		if errors.Is(err, identity.ErrAgentAlreadyExists) {
			existing, _ := s.registry.Get(regAgentID)
			resp := registerAgentResponse{
				AgentID:         string(existing.AgentID),
				FingerprintHash: existing.FingerprintHash,
			}
			if s.walletMgr != nil {
				if addr, ok := s.walletMgr.AddressOf(regAgentID); ok {
					resp.DepositAddress = string(addr)
				}
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := registerAgentResponse{
		AgentID:         string(fp.AgentID),
		FingerprintHash: fp.FingerprintHash,
	}

	// Register deposit address (optional — requires walletMgr).
	if s.walletMgr != nil {
		addr := s.walletMgr.Register(regAgentID, regPubKey)
		resp.DepositAddress = string(addr)
	}

	// Onboarding allocation: grant initial AET and auto-stake.
	// FundAgent mints from the protocol reserve so this always succeeds regardless
	// of whether genesis has been run. The declining-curve cap (OnboardingPoolTotal)
	// prevents unbounded minting across the full agent lifecycle.
	s.onboardingMu.Lock()
	allocation := genesis.OnboardingAllocation(agentCountBefore)
	if allocation > 0 && s.onboardingAllocated+allocation <= genesis.OnboardingPoolTotal {
		s.onboardingAllocated += allocation
		newAllocated := s.onboardingAllocated
		s.onboardingMu.Unlock()

		// Persist the updated counter so it survives restarts.
		if s.store != nil {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, newAllocated)
			_ = s.store.PutMeta("onboarding_allocated", buf)
		}

		if err := s.transfer.FundAgent(regAgentID, allocation); err == nil {
			resp.OnboardingAllocation = allocation
			if s.stakeManager != nil {
				stakeAmount := allocation / 2
				s.stakeManager.Stake(regAgentID, stakeAmount)
				since := s.stakeManager.StakedSince(regAgentID)
				resp.TrustLimit = staking.TrustLimit(stakeAmount, 0, since, time.Now().Unix())
			}
		}
	} else {
		s.onboardingMu.Unlock()
	}

	writeJSON(w, http.StatusCreated, resp)

	if s.nodeMetrics != nil {
		s.nodeMetrics.AgentsRegistered.Inc()
	}
	if s.eventBus != nil {
		s.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeNewAgent,
			Timestamp: time.Now(),
			Data:      map[string]any{"agent_id": string(fp.AgentID)},
		})
	}
}

// handleListAgents returns all registered agents enriched with live balance and
// stake data from the TransferLedger and StakeManager.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	fps := s.registry.All(0, 0)
	now := time.Now().Unix()
	entries := make([]agentSummaryEntry, 0, len(fps))
	for _, fp := range fps {
		bal, _ := s.transfer.Balance(fp.AgentID)
		var staked, trustLimit uint64
		if s.stakeManager != nil {
			staked = s.stakeManager.StakedAmount(fp.AgentID)
			since := s.stakeManager.StakedSince(fp.AgentID)
			lastAct := s.stakeManager.LastActivity(fp.AgentID)
			trustLimit = staking.TrustLimitFull(staked, fp.TasksCompleted, since, lastAct, now)
		}
		// Use live reputation score from the reputation manager when available.
		repScore := fp.ReputationScore
		tasksCompleted := fp.TasksCompleted
		if s.reputationMgr != nil {
			rep := s.reputationMgr.GetReputation(fp.AgentID)
			repScore = uint64(rep.OverallScore)
			tasksCompleted = rep.TotalCompleted
		}
		entries = append(entries, agentSummaryEntry{
			AgentID:         string(fp.AgentID),
			ReputationScore: repScore,
			TasksCompleted:  tasksCompleted,
			Balance:         bal,
			StakedAmount:    staked,
			TrustLimit:      trustLimit,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleGetAgent returns the capability fingerprint for agent_id.
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	fp, err := s.registry.Get(crypto.AgentID(agentID))
	if err != nil {
		writeCodedError(w, http.StatusNotFound, "agent_not_found", "agent not found", "agent_id: "+agentID)
		return
	}
	writeJSON(w, http.StatusOK, fp)
}

// handleGetBalance returns the spendable balance for agent_id.
func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	bal, err := s.transfer.Balance(crypto.AgentID(agentID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{
		AgentID:  agentID,
		Balance:  bal,
		Currency: "AET",
	})
}

// handleTransfer constructs, signs, and submits a Transfer event.
// The from_agent is always the node's own keypair identity.
// When staking is wired in, the transfer amount must not exceed the sender's
// computed trust limit; excess is rejected with HTTP 403.
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeCodedError(w, http.StatusBadRequest, "invalid_request", "invalid request body: "+err.Error(), "")
		return
	}
	if req.ToAgent == "" {
		writeError(w, http.StatusBadRequest, "to_agent is required")
		return
	}
	if req.Currency == "" {
		req.Currency = "AET"
	}

	// Resolve the economic sender identity.
	// fromAgentID is used for balance/trust lookups and as TransferPayload.FromAgent.
	// The DAG event itself is still signed by the node's keypair (s.agentID / s.kp),
	// which is required by crypto.SignEvent.
	fromAgentID := s.agentID
	if req.FromAgent != "" {
		fromAgentID = crypto.AgentID(req.FromAgent)
	}

	// Trust limit enforcement: amount must not exceed the sender's trust limit.
	if s.stakeManager != nil {
		var tasksCompleted uint64
		if fp, err := s.registry.Get(fromAgentID); err == nil {
			tasksCompleted = fp.TasksCompleted
		}
		staked := s.stakeManager.StakedAmount(fromAgentID)
		since := s.stakeManager.StakedSince(fromAgentID)
		lastAct := s.stakeManager.LastActivity(fromAgentID)
		trustLimit := staking.TrustLimitFull(staked, tasksCompleted, since, lastAct, time.Now().Unix())
		if req.Amount > trustLimit {
			writeCodedError(w, http.StatusForbidden, "trust_limit_exceeded",
				"amount exceeds trust limit",
				fmt.Sprintf("trust_limit: %d, requested: %d", trustLimit, req.Amount))
			return
		}
	}

	payload := event.TransferPayload{
		FromAgent: string(fromAgentID),
		ToAgent:   req.ToAgent,
		Amount:    req.Amount,
		Currency:  req.Currency,
		Memo:      req.Memo,
	}

	// Default stake_amount to the engine's minimum when the client omits it.
	// Without this, every request without an explicit stake_amount would be
	// rejected by the OCS engine with ErrInsufficientStake.
	stakeAmount := req.StakeAmount
	if stakeAmount == 0 {
		stakeAmount = s.engine.MinEventStake()
	}

	refs, priorTS := s.buildCausalRefs(req.CausalRefs)
	e, err := event.New(event.EventTypeTransfer, refs, payload, string(s.agentID), priorTS, stakeAmount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build event: "+err.Error())
		return
	}

	if err := s.submitAndAdd(e); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, fmt.Errorf("dag: %w", err)) {
			code = http.StatusInternalServerError
		}
		writeError(w, code, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, eventIDResponse{EventID: string(e.ID)})
}

// handleGeneration constructs, signs, and submits a Generation event.
// The generating_agent is always the node's own keypair identity.
func (s *Server) handleGeneration(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req generationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.EvidenceHash == "" {
		writeError(w, http.StatusBadRequest, "evidence_hash is required")
		return
	}
	if req.BeneficiaryAgent == "" {
		req.BeneficiaryAgent = string(s.agentID)
	}

	payload := event.GenerationPayload{
		GeneratingAgent:  string(s.agentID),
		BeneficiaryAgent: req.BeneficiaryAgent,
		ClaimedValue:     req.ClaimedValue,
		EvidenceHash:     req.EvidenceHash,
		TaskDescription:  req.TaskDescription,
	}

	// Default stake_amount to the engine's minimum when the client omits it.
	stakeAmount := req.StakeAmount
	if stakeAmount == 0 {
		stakeAmount = s.engine.MinEventStake()
	}

	refs, priorTS := s.buildCausalRefs(req.CausalRefs)
	e, err := event.New(event.EventTypeGeneration, refs, payload, string(s.agentID), priorTS, stakeAmount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build event: "+err.Error())
		return
	}

	if err := s.submitAndAdd(e); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, fmt.Errorf("dag: %w", err)) {
			code = http.StatusInternalServerError
		}
		writeError(w, code, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, eventIDResponse{EventID: string(e.ID)})
}

// handleGetEvent returns the event stored at event_id in the local DAG.
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("event_id")
	e, err := s.dag.Get(event.EventID(eventID))
	if err != nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// handleDAGTips returns the current tip event IDs in lexicographic order.
func (s *Server) handleDAGTips(w http.ResponseWriter, r *http.Request) {
	tips := s.dag.Tips()
	sort.Slice(tips, func(i, j int) bool { return tips[i] < tips[j] })
	writeJSON(w, http.StatusOK, tipsResponse{Tips: tips})
}

// handleStatus returns a point-in-time snapshot of the node's health.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	peers := 0
	if s.node != nil {
		peers = s.node.PeerCount()
	}
	ratio, _ := s.supply.SupplyRatio()
	writeJSON(w, http.StatusOK, statusResponse{
		AgentID:     string(s.agentID),
		Version:     "0.1.0-testnet",
		Peers:       peers,
		DAGSize:     s.dag.Size(),
		OCSPending:  s.engine.PendingCount(),
		SupplyRatio: ratio,
	})
}

// handleVerify submits a VerificationResult to the OCS engine.
// Returns 400 when the event is not in the pending map.
// Returns 403 when the validator is a party to the transaction (self-dealing).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.EventID == "" {
		writeError(w, http.StatusBadRequest, "event_id is required")
		return
	}

	result := ocs.VerificationResult{
		EventID:       req.EventID,
		Verdict:       req.Verdict,
		VerifiedValue: req.VerifiedValue,
		VerifierID:    s.agentID,
		Timestamp:     time.Now(),
	}
	if err := s.engine.ProcessResult(result); err != nil {
		if errors.Is(err, ocs.ErrNotPending) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ocs.ErrSelfDealing) {
			writeCodedError(w, http.StatusForbidden, "self_dealing", err.Error(), "")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := "settled"
	if !req.Verdict {
		status = "adjusted"
	}
	writeJSON(w, http.StatusOK, verifyResponse{
		EventID: string(req.EventID),
		Verdict: req.Verdict,
		Status:  status,
	})
}

// handlePending returns all events currently awaiting OCS verification.
func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	items := s.engine.Pending()
	writeJSON(w, http.StatusOK, items)
}

// handleGetAgentAddress returns the deposit address for agent_id.
// Returns 501 when the wallet component is not wired in.
func (s *Server) handleGetAgentAddress(w http.ResponseWriter, r *http.Request) {
	if s.walletMgr == nil {
		writeError(w, http.StatusNotImplemented, "wallet not enabled")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))
	addr, ok := s.walletMgr.AddressOf(agentID)
	if !ok {
		writeError(w, http.StatusNotFound, "no deposit address for agent")
		return
	}
	writeJSON(w, http.StatusOK, agentAddressResponse{
		AgentID: string(agentID),
		Address: string(addr),
	})
}

// handleGetStake returns the full staking info for agent_id, including effective
// tasks (after decay), days staked, and the computed trust limit.
// Returns 501 when the staking component is not wired in.
func (s *Server) handleGetStake(w http.ResponseWriter, r *http.Request) {
	if s.stakeManager == nil {
		writeError(w, http.StatusNotImplemented, "staking not enabled")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))

	var tasksCompleted uint64
	if fp, err := s.registry.Get(agentID); err == nil {
		tasksCompleted = fp.TasksCompleted
	}

	now := time.Now().Unix()
	staked := s.stakeManager.StakedAmount(agentID)
	since := s.stakeManager.StakedSince(agentID)
	lastAct := s.stakeManager.LastActivity(agentID)

	var daysStaked uint64
	if since > 0 && now > since {
		daysStaked = uint64((now - since) / 86400)
	}

	effective := staking.EffectiveTasks(tasksCompleted, lastAct, now)
	multiplier := staking.TrustMultiplier(effective, since, now)
	limit := staking.TrustLimitFull(staked, tasksCompleted, since, lastAct, now)

	writeJSON(w, http.StatusOK, stakeInfoResponse{
		AgentID:         string(agentID),
		StakedAmount:    staked,
		StakedSince:     since,
		TrustMultiplier: multiplier,
		TrustLimit:      limit,
		TasksCompleted:  tasksCompleted,
		EffectiveTasks:  effective,
		LastActivity:    lastAct,
		DaysStaked:      daysStaked,
	})
}

// handleEconomics returns a network economics overview.
func (s *Server) handleEconomics(w http.ResponseWriter, r *http.Request) {
	s.onboardingMu.Lock()
	allocated := s.onboardingAllocated
	s.onboardingMu.Unlock()

	var collected, burned, treasury uint64
	if s.feeCollector != nil {
		collected, burned, treasury = s.feeCollector.Stats()
	}

	var circulating uint64
	if genesis.TotalSupply >= burned {
		circulating = genesis.TotalSupply - burned
	}

	// Include the live treasury account balance (genesis allocation + all fee
	// credits), not just the in-session fee-accrued counter which resets on
	// restart. This lets the explorer show the real treasury at all times.
	treasuryBalance, _ := s.transfer.Balance(crypto.AgentID(genesis.BucketTreasury))

	// Sum all-time verified AI output across the generation ledger.
	var totalGeneratedValue uint64
	if s.generation != nil {
		totalGeneratedValue, _ = s.generation.TotalVerifiedValue(365 * 24 * time.Hour)
	}

	writeJSON(w, http.StatusOK, economicsResponse{
		TotalSupply:         genesis.TotalSupply,
		CirculatingSupply:   circulating,
		OnboardingPoolTotal: genesis.OnboardingPoolTotal,
		OnboardingMaxAgents: genesis.OnboardingMaxAgents,
		OnboardingAllocated: allocated,
		TotalCollected:      collected,
		TotalBurned:         burned,
		TreasuryAccrued:     treasury,
		TreasuryBalance:     treasuryBalance,
		FeeBasisPoints:      fees.FeeBasisPoints,
		TotalGeneratedValue: totalGeneratedValue,
	})
}

// handleResolveAddress resolves a deposit address to an agent ID.
// Returns 501 when the wallet component is not wired in.
func (s *Server) handleResolveAddress(w http.ResponseWriter, r *http.Request) {
	if s.walletMgr == nil {
		writeError(w, http.StatusNotImplemented, "wallet not enabled")
		return
	}
	address := wallet.Address(r.PathValue("address"))
	agentID, ok := s.walletMgr.Resolve(address)
	if !ok {
		writeError(w, http.StatusNotFound, "address not found")
		return
	}
	writeJSON(w, http.StatusOK, resolveAddressResponse{
		Address: string(address),
		AgentID: string(agentID),
	})
}

// handleStake stakes tokens for the given agent.
// Returns 501 when staking is not wired in.
func (s *Server) handleStake(w http.ResponseWriter, r *http.Request) {
	if s.stakeManager == nil {
		writeError(w, http.StatusNotImplemented, "staking not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req stakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if req.Amount == 0 {
		writeError(w, http.StatusBadRequest, "amount must be > 0")
		return
	}

	agentID := crypto.AgentID(req.AgentID)
	s.stakeManager.Stake(agentID, req.Amount)

	var tasksCompleted uint64
	if fp, err := s.registry.Get(agentID); err == nil {
		tasksCompleted = fp.TasksCompleted
	}
	now := time.Now().Unix()
	staked := s.stakeManager.StakedAmount(agentID)
	since := s.stakeManager.StakedSince(agentID)
	lastAct := s.stakeManager.LastActivity(agentID)

	writeJSON(w, http.StatusOK, stakeOpResponse{
		AgentID:      req.AgentID,
		StakedAmount: staked,
		TrustLimit:   staking.TrustLimitFull(staked, tasksCompleted, since, lastAct, now),
	})

	if s.eventBus != nil {
		s.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeStake,
			Timestamp: time.Now(),
			Data:      map[string]any{"agent_id": req.AgentID, "amount": req.Amount},
		})
	}
}

// handleRecentEvents returns the most recent N events from the DAG ordered by
// CausalTimestamp descending. The limit query parameter controls how many are
// returned (default 50, max 200).
func (s *Server) handleRecentEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	all := s.dag.All()
	sort.Slice(all, func(i, j int) bool {
		if all[i].CausalTimestamp != all[j].CausalTimestamp {
			return all[i].CausalTimestamp > all[j].CausalTimestamp
		}
		return all[i].ID > all[j].ID
	})
	if len(all) > limit {
		all = all[:limit]
	}

	items := make([]recentEventItem, len(all))
	for i, e := range all {
		item := recentEventItem{
			ID:              string(e.ID),
			Type:            string(e.Type),
			AgentID:         e.AgentID,
			CausalTimestamp: e.CausalTimestamp,
			StakeAmount:     e.StakeAmount,
			SettlementState: string(e.SettlementState),
		}
		// Issue 4: use live settlement state from the ledger / engine rather
		// than the immutable DAG event field which stays Optimistic forever.
		if e.Type == event.EventTypeTransfer {
			if state, ok := s.transfer.GetSettlement(e.ID); ok {
				item.SettlementState = string(state)
			}
		} else if !s.engine.IsPending(e.ID) && e.SettlementState == event.SettlementOptimistic {
			item.SettlementState = string(event.SettlementSettled)
		}
		// Issue 5: enrich Transfer events with payload fields for the activity feed.
		if e.Type == event.EventTypeTransfer {
			if tp, err := event.GetPayload[event.TransferPayload](e); err == nil {
				item.From = tp.FromAgent
				item.To = tp.ToAgent
				item.Amount = tp.Amount
				item.Memo = tp.Memo
			}
		}
		items[i] = item
	}
	writeJSON(w, http.StatusOK, items)
}

// handleDAGStats returns aggregate statistics about the local DAG useful for
// the explorer visualization.
func (s *Server) handleDAGStats(w http.ResponseWriter, r *http.Request) {
	all := s.dag.All()
	byType := make(map[string]int)
	var maxDepth uint64
	for _, e := range all {
		byType[string(e.Type)]++
		if e.CausalTimestamp > maxDepth {
			maxDepth = e.CausalTimestamp
		}
	}
	writeJSON(w, http.StatusOK, dagStatsResponse{
		TotalEvents:  len(all),
		EventsByType: byType,
		TipsCount:    len(s.dag.Tips()),
		MaxDepth:     maxDepth,
	})
}

// handleLeaderboard returns the top N agents sorted by a given field.
// The sort query parameter accepts "reputation" (default), "balance", or "tasks".
// The limit query parameter controls how many agents are returned (default 20, max 100).
func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "reputation"
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	agents := s.registry.All(0, 0)

	// Fetch balances and stake info for each agent.
	entries := make([]leaderboardEntry, 0, len(agents))
	now := time.Now().Unix()
	for _, fp := range agents {
		bal, _ := s.transfer.Balance(fp.AgentID)
		var staked uint64
		var trustLimit uint64
		if s.stakeManager != nil {
			staked = s.stakeManager.StakedAmount(fp.AgentID)
			since := s.stakeManager.StakedSince(fp.AgentID)
			lastAct := s.stakeManager.LastActivity(fp.AgentID)
			trustLimit = staking.TrustLimitFull(staked, fp.TasksCompleted, since, lastAct, now)
		}
		// Use live reputation score from the reputation manager when available;
		// fall back to the identity fingerprint's cached score otherwise.
		repScore := fp.ReputationScore
		tasksCompleted := fp.TasksCompleted
		if s.reputationMgr != nil {
			rep := s.reputationMgr.GetReputation(fp.AgentID)
			repScore = uint64(rep.OverallScore)
			tasksCompleted = rep.TotalCompleted
		}
		entries = append(entries, leaderboardEntry{
			AgentID:         string(fp.AgentID),
			ReputationScore: repScore,
			TasksCompleted:  tasksCompleted,
			Balance:         bal,
			TrustLimit:      trustLimit,
			StakedAmount:    staked,
		})
	}

	switch sortBy {
	case "balance":
		sort.Slice(entries, func(i, j int) bool { return entries[i].Balance > entries[j].Balance })
	case "tasks":
		sort.Slice(entries, func(i, j int) bool { return entries[i].TasksCompleted > entries[j].TasksCompleted })
	default: // "reputation"
		sort.Slice(entries, func(i, j int) bool { return entries[i].ReputationScore > entries[j].ReputationScore })
	}

	if len(entries) > limit {
		entries = entries[:limit]
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleNetworkActivity returns hourly transaction volume buckets for the last
// N hours. The hours query parameter controls the window (default 24, max 168).
// Since events carry Lamport timestamps rather than wall-clock times, we bucket
// by event submission epoch: events are partitioned into equal Lamport-clock
// ranges mapped to wall-clock hours relative to now.
func (s *Server) handleNetworkActivity(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	if hours > 168 {
		hours = 168
	}

	// Build buckets keyed by hour offset from now (0 = current hour).
	type bucket struct {
		volume uint64
		count  int
	}
	buckets := make([]bucket, hours)

	all := s.dag.All()
	if len(all) > 0 {
		// Find the max Lamport timestamp — maps to the current hour (offset 0).
		var maxTS uint64
		for _, e := range all {
			if e.CausalTimestamp > maxTS {
				maxTS = e.CausalTimestamp
			}
		}

		// Each Lamport unit maps to an approximate wall-clock slot.
		// Treat each bucket as covering maxTS/hours Lamport ticks.
		ticksPerHour := maxTS / uint64(hours)
		if ticksPerHour == 0 {
			ticksPerHour = 1
		}

		for _, e := range all {
			// Offset from the newest event (higher Lamport = more recent).
			age := maxTS - e.CausalTimestamp
			idx := int(age / ticksPerHour)
			if idx >= hours {
				idx = hours - 1
			}
			buckets[idx].count++
			// Add stake as a proxy for economic volume.
			buckets[idx].volume += e.StakeAmount
		}
	}

	now := time.Now().UTC().Truncate(time.Hour)
	result := make([]activityBucket, hours)
	for i := range result {
		t := now.Add(-time.Duration(i) * time.Hour)
		result[i] = activityBucket{
			Hour:   t.Format(time.RFC3339),
			Volume: buckets[i].volume,
			Count:  buckets[i].count,
		}
	}
	writeJSON(w, http.StatusOK, networkActivityResponse{
		Hours:   hours,
		Buckets: result,
	})
}

// handlePostRegistry creates or updates a service listing for the requesting
// agent. Returns 501 when the service registry is not wired in.
func (s *Server) handlePostRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registryListingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Category == "" {
		writeError(w, http.StatusBadRequest, "category is required")
		return
	}

	// Accept any agent_id from the body; fall back to the node's own identity.
	agentID := crypto.AgentID(req.AgentID)
	if agentID == "" {
		agentID = s.agentID
	}

	listing := &svcregistry.ServiceListing{
		AgentID:     agentID,
		Name:        req.Name,
		Description: req.Description,
		Category:    req.Category,
		PriceAET:    req.PriceAET,
		Endpoint:    req.Endpoint,
		Tags:        req.Tags,
		Active:      req.Active,
	}
	s.svcRegistry.Register(listing)
	got, _ := s.svcRegistry.Get(agentID)
	writeJSON(w, http.StatusCreated, got)

	if s.eventBus != nil {
		s.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeRegistration,
			Timestamp: time.Now(),
			Data:      map[string]any{"agent_id": string(agentID), "name": req.Name},
		})
	}
}

// handleSearchRegistry searches for active service listings.
// Query params: q (search term), category, limit (default 20).
func (s *Server) handleSearchRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	q := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	results := s.svcRegistry.Search(q, category, limit)
	if results == nil {
		results = []*svcregistry.ServiceListing{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleGetRegistryListing returns the service listing for a single agent.
func (s *Server) handleGetRegistryListing(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))
	listing, ok := s.svcRegistry.Get(agentID)
	if !ok {
		writeError(w, http.StatusNotFound, "listing not found")
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// handleRegistryCategories returns all categories with active listing counts.
func (s *Server) handleRegistryCategories(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	cats := s.svcRegistry.Categories()
	if cats == nil {
		cats = map[string]int{}
	}
	writeJSON(w, http.StatusOK, cats)
}

// handleDeleteRegistry deactivates the listing owned by the node. Returns 403
// when the requested agent_id does not match the node's own identity, and 404
// when the listing does not exist.
func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))
	if agentID != s.agentID {
		writeError(w, http.StatusForbidden, "can only deactivate your own listing")
		return
	}
	if !s.svcRegistry.Deactivate(agentID) {
		writeError(w, http.StatusNotFound, "listing not found")
		return
	}
	writeJSON(w, http.StatusOK, deactivateResponse{AgentID: string(agentID), Active: false})
}

// handleUnstake unstakes tokens for the given agent.
// Returns 400 when the agent has insufficient staked balance.
// Returns 501 when staking is not wired in.
func (s *Server) handleUnstake(w http.ResponseWriter, r *http.Request) {
	if s.stakeManager == nil {
		writeError(w, http.StatusNotImplemented, "staking not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req stakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if req.Amount == 0 {
		writeError(w, http.StatusBadRequest, "amount must be > 0")
		return
	}

	agentID := crypto.AgentID(req.AgentID)
	ok := s.stakeManager.Unstake(agentID, req.Amount)
	if !ok {
		writeError(w, http.StatusBadRequest, "insufficient staked balance")
		return
	}

	var tasksCompleted uint64
	if fp, err := s.registry.Get(agentID); err == nil {
		tasksCompleted = fp.TasksCompleted
	}
	now := time.Now().Unix()
	staked := s.stakeManager.StakedAmount(agentID)
	since := s.stakeManager.StakedSince(agentID)
	lastAct := s.stakeManager.LastActivity(agentID)

	writeJSON(w, http.StatusOK, unstakeOpResponse{
		AgentID:      req.AgentID,
		StakedAmount: staked,
		TrustLimit:   staking.TrustLimitFull(staked, tasksCompleted, since, lastAct, now),
		Success:      true,
	})

	if s.eventBus != nil {
		s.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeUnstake,
			Timestamp: time.Now(),
			Data:      map[string]any{"agent_id": req.AgentID, "amount": req.Amount},
		})
	}
}

// ---------------------------------------------------------------------------
// Developer Platform Handlers
// ---------------------------------------------------------------------------

// handleGenerateKey handles POST /v1/platform/keys.
// Creates a new API key for a third-party developer application.
func (s *Server) handleGenerateKey(w http.ResponseWriter, r *http.Request) {
	if s.platformKeys == nil {
		writeError(w, http.StatusNotImplemented, "platform API keys not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Name  string         `json:"name"`
		Email string         `json:"email"`
		Tier  platform.Tier  `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" || req.Email == "" {
		writeError(w, http.StatusBadRequest, "name and email are required")
		return
	}
	if req.Tier == "" {
		req.Tier = platform.TierFree
	}
	key := s.platformKeys.GenerateKey(req.Name, req.Email, req.Tier)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":        key.Key,
		"name":       key.Name,
		"tier":       key.Tier,
		"rate_limit": platform.RateLimit(key.Tier),
	})
}

// handleGetKey handles GET /v1/platform/keys/{key}.
// Returns metadata and usage stats for a specific API key.
func (s *Server) handleGetKey(w http.ResponseWriter, r *http.Request) {
	if s.platformKeys == nil {
		writeError(w, http.StatusNotImplemented, "platform API keys not enabled")
		return
	}
	keyStr := r.PathValue("key")
	key, ok := s.platformKeys.GetKey(keyStr)
	if !ok {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, key)
}

// handleRevokeKey handles DELETE /v1/platform/keys/{key}.
// Deactivates an API key so it can no longer be used.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if s.platformKeys == nil {
		writeError(w, http.StatusNotImplemented, "platform API keys not enabled")
		return
	}
	keyStr := r.PathValue("key")
	if !s.platformKeys.Revoke(keyStr) {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handlePlatformStats handles GET /v1/platform/stats.
// Returns aggregate usage statistics across all developer API keys.
func (s *Server) handlePlatformStats(w http.ResponseWriter, r *http.Request) {
	if s.platformKeys == nil {
		writeError(w, http.StatusNotImplemented, "platform API keys not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.platformKeys.Stats())
}

// ---------------------------------------------------------------------------
// Autonomous task router handlers
// ---------------------------------------------------------------------------

// handleRouterRegister handles POST /v1/router/register.
// Registers an agent's capability profile for autonomous task routing.
func (s *Server) handleRouterRegister(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	var req struct {
		AgentID       string               `json:"agent_id"`
		Categories    []string             `json:"categories"`
		Tags          []string             `json:"tags"`
		Description   string               `json:"description"`
		PricePerTask  uint64               `json:"price_per_task"`
		MaxConcurrent int                  `json:"max_concurrent"`
		WebhookURL    string               `json:"webhook_url"`
		WebhookSecret string               `json:"webhook_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id required")
		return
	}
	if len(req.Categories) == 0 {
		writeError(w, http.StatusBadRequest, "categories required")
		return
	}

	cap := router.AgentCapability{
		AgentID:       crypto.AgentID(req.AgentID),
		Categories:    req.Categories,
		Tags:          req.Tags,
		Description:   req.Description,
		PricePerTask:  req.PricePerTask,
		MaxConcurrent: req.MaxConcurrent,
		Available:     true,
	}
	if req.WebhookURL != "" {
		cap.Webhook = &router.WebhookConfig{URL: req.WebhookURL, Secret: req.WebhookSecret}
	}

	s.taskRouter.RegisterCapability(cap)
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent_id":   req.AgentID,
		"categories": req.Categories,
		"status":     "registered",
	})
}

// handleRouterUnregister handles DELETE /v1/router/register/{agent_id}.
// Removes an agent from the routing pool.
func (s *Server) handleRouterUnregister(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id required")
		return
	}
	s.taskRouter.UnregisterCapability(crypto.AgentID(agentID))
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "status": "unregistered"})
}

// handleRouterAvailability handles PUT /v1/router/availability/{agent_id}.
// Toggles whether the router will assign tasks to the agent.
func (s *Server) handleRouterAvailability(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id required")
		return
	}
	var req struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !s.taskRouter.SetAvailability(crypto.AgentID(agentID), req.Available) {
		writeError(w, http.StatusNotFound, "agent not registered for routing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "available": req.Available})
}

// handleRouterAgents handles GET /v1/router/agents.
// Returns all agents currently registered for autonomous routing.
func (s *Server) handleRouterAgents(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	agents := s.taskRouter.RegisteredAgents()
	if agents == nil {
		agents = []*router.AgentCapability{}
	}
	writeJSON(w, http.StatusOK, agents)
}

// handleRouterRoutes handles GET /v1/router/routes?limit=N.
// Returns recent routing decisions.
func (s *Server) handleRouterRoutes(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	routes := s.taskRouter.RecentRoutes(limit)
	if routes == nil {
		routes = []*router.RouteResult{}
	}
	writeJSON(w, http.StatusOK, routes)
}

// handleRouterStats handles GET /v1/router/stats.
// Returns aggregate routing engine statistics.
func (s *Server) handleRouterStats(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.taskRouter.Stats())
}
