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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/eventbus"
	"github.com/aethernet/core/internal/fees"
	"github.com/aethernet/core/internal/genesis"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/network"
	"github.com/aethernet/core/internal/ocs"
	"github.com/aethernet/core/internal/ratelimit"
	svcregistry "github.com/aethernet/core/internal/registry"
	"github.com/aethernet/core/internal/staking"
	"github.com/aethernet/core/internal/wallet"
)

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

	// Event bus — optional; set via SetEventBus after construction.
	// When non-nil, handlers publish events for real-time WebSocket streaming.
	eventBus *eventbus.Bus

	// Rate limiters — optional; set via SetRateLimiters after construction.
	// When nil, no rate limiting is applied (safe for tests).
	writeLimiter *ratelimit.Limiter // POST/DELETE/PUT/PATCH endpoints
	readLimiter  *ratelimit.Limiter // GET endpoints

	// Onboarding tracking. Protected by onboardingMu.
	onboardingMu        sync.Mutex
	onboardingAllocated uint64

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
	}

	mux := http.NewServeMux()
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

	// WebSocket endpoint for real-time event streaming.
	mux.Handle("GET /v1/ws", s.wsHandler())

	// Serve the web explorer from ./explorer (dev) or the Docker install path.
	explorerDir := explorerPath()
	if explorerDir != "" {
		mux.Handle("GET /explorer/", http.StripPrefix("/explorer/", http.FileServer(http.Dir(explorerDir))))
	}

	s.mux = mux
	s.srv = &http.Server{Addr: listenAddr, Handler: s}

	return s
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

// ServeHTTP implements http.Handler. Applies per-IP rate limiting (when
// configured) before dispatching to the route mux. Tests that construct the
// Server via httptest.NewServer(s) pass through here without rate limiting
// because SetRateLimiters is not called in tests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodDelete, http.MethodPut, http.MethodPatch:
		if s.writeLimiter != nil && !s.writeLimiter.Allow(ratelimit.ExtractIP(r)) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
	default:
		if s.readLimiter != nil && !s.readLimiter.Allow(ratelimit.ExtractIP(r)) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
	}
	s.mux.ServeHTTP(w, r)
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
}

type registerAgentResponse struct {
	AgentID              string `json:"agent_id"`
	FingerprintHash      string `json:"fingerprint_hash"`
	DepositAddress       string `json:"deposit_address,omitempty"`
	OnboardingAllocation uint64 `json:"onboarding_allocation,omitempty"`
	TrustLimit           uint64 `json:"trust_limit,omitempty"`
}

type transferRequest struct {
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
	TotalSupply         uint64 `json:"total_supply"`
	CirculatingSupply   uint64 `json:"circulating_supply"`
	OnboardingPoolTotal uint64 `json:"onboarding_pool_total"`
	OnboardingMaxAgents uint64 `json:"onboarding_max_agents"`
	OnboardingAllocated uint64 `json:"onboarding_allocated"`
	TotalCollected      uint64 `json:"total_collected"`
	TotalBurned         uint64 `json:"total_burned"`
	TreasuryAccrued     uint64 `json:"treasury_accrued"`
	FeeBasisPoints      uint64 `json:"fee_basis_points"`
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
}

type dagStatsResponse struct {
	TotalEvents  int            `json:"total_events"`
	EventsByType map[string]int `json:"events_by_type"`
	TipsCount    int            `json:"tips_count"`
	MaxDepth     uint64         `json:"max_depth"`
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
// Helpers
// ---------------------------------------------------------------------------

// explorerPath returns the directory to serve the web explorer from, or ""
// if no explorer directory is found. It checks two locations in order:
//  1. ./explorer  — the development / source-tree path
//  2. /usr/local/share/aethernet/explorer — the Docker install path
func explorerPath() string {
	for _, p := range []string{"./explorer", "/usr/local/share/aethernet/explorer"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
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
func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Snapshot agent count before this registration for the onboarding curve.
	agentCountBefore := uint64(len(s.registry.All(0, 0)))

	fp, err := identity.NewFingerprint(s.agentID, s.kp.PublicKey, req.Capabilities)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create fingerprint: "+err.Error())
		return
	}

	if err := s.registry.Register(fp); err != nil {
		if errors.Is(err, identity.ErrAgentAlreadyExists) {
			existing, _ := s.registry.Get(s.agentID)
			resp := registerAgentResponse{
				AgentID:         string(existing.AgentID),
				FingerprintHash: existing.FingerprintHash,
			}
			if s.walletMgr != nil {
				if addr, ok := s.walletMgr.AddressOf(s.agentID); ok {
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
		addr := s.walletMgr.Register(s.agentID, s.kp.PublicKey)
		resp.DepositAddress = string(addr)
	}

	// Onboarding allocation: grant from ecosystem pool, auto-stake.
	s.onboardingMu.Lock()
	allocation := genesis.OnboardingAllocation(agentCountBefore)
	if allocation > 0 && s.onboardingAllocated+allocation <= genesis.OnboardingPoolTotal {
		s.onboardingAllocated += allocation
		s.onboardingMu.Unlock()

		_ = s.transfer.FundAgent(s.agentID, allocation)
		resp.OnboardingAllocation = allocation

		if s.stakeManager != nil {
			s.stakeManager.Stake(s.agentID, allocation)
			since := s.stakeManager.StakedSince(s.agentID)
			resp.TrustLimit = staking.TrustLimit(allocation, 0, since, time.Now().Unix())
		}
	} else {
		s.onboardingMu.Unlock()
	}

	writeJSON(w, http.StatusCreated, resp)

	if s.eventBus != nil {
		s.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTypeNewAgent,
			Timestamp: time.Now(),
			Data:      map[string]any{"agent_id": string(fp.AgentID)},
		})
	}
}

// handleListAgents returns all registered agent fingerprints.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.All(0, 0)
	writeJSON(w, http.StatusOK, agents)
}

// handleGetAgent returns the capability fingerprint for agent_id.
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	fp, err := s.registry.Get(crypto.AgentID(agentID))
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
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
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.ToAgent == "" {
		writeError(w, http.StatusBadRequest, "to_agent is required")
		return
	}
	if req.Currency == "" {
		req.Currency = "AET"
	}

	// Trust limit enforcement: amount must not exceed the sender's trust limit.
	if s.stakeManager != nil {
		var tasksCompleted uint64
		if fp, err := s.registry.Get(s.agentID); err == nil {
			tasksCompleted = fp.TasksCompleted
		}
		staked := s.stakeManager.StakedAmount(s.agentID)
		since := s.stakeManager.StakedSince(s.agentID)
		lastAct := s.stakeManager.LastActivity(s.agentID)
		trustLimit := staking.TrustLimitFull(staked, tasksCompleted, since, lastAct, time.Now().Unix())
		if req.Amount > trustLimit {
			writeError(w, http.StatusForbidden, "amount exceeds trust limit")
			return
		}
	}

	payload := event.TransferPayload{
		FromAgent: string(s.agentID),
		ToAgent:   req.ToAgent,
		Amount:    req.Amount,
		Currency:  req.Currency,
		Memo:      req.Memo,
	}

	refs, priorTS := s.buildCausalRefs(req.CausalRefs)
	e, err := event.New(event.EventTypeTransfer, refs, payload, string(s.agentID), priorTS, req.StakeAmount)
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

	refs, priorTS := s.buildCausalRefs(req.CausalRefs)
	e, err := event.New(event.EventTypeGeneration, refs, payload, string(s.agentID), priorTS, req.StakeAmount)
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
			writeError(w, http.StatusForbidden, err.Error())
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

	writeJSON(w, http.StatusOK, economicsResponse{
		TotalSupply:         genesis.TotalSupply,
		CirculatingSupply:   circulating,
		OnboardingPoolTotal: genesis.OnboardingPoolTotal,
		OnboardingMaxAgents: genesis.OnboardingMaxAgents,
		OnboardingAllocated: allocated,
		TotalCollected:      collected,
		TotalBurned:         burned,
		TreasuryAccrued:     treasury,
		FeeBasisPoints:      fees.FeeBasisPoints,
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
		items[i] = recentEventItem{
			ID:              string(e.ID),
			Type:            string(e.Type),
			AgentID:         e.AgentID,
			CausalTimestamp: e.CausalTimestamp,
			StakeAmount:     e.StakeAmount,
			SettlementState: string(e.SettlementState),
		}
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
		entries = append(entries, leaderboardEntry{
			AgentID:         string(fp.AgentID),
			ReputationScore: fp.ReputationScore,
			TasksCompleted:  fp.TasksCompleted,
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
