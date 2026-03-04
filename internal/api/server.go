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
//	POST /v1/agents                  register the node's own agent
//	GET  /v1/agents                  list all known agents
//	GET  /v1/agents/{agent_id}        get an agent's capability fingerprint
//	GET  /v1/agents/{agent_id}/balance get an agent's spendable balance
//	POST /v1/transfer                 submit a Transfer event
//	POST /v1/generation               submit a Generation event
//	POST /v1/verify                   submit a verification verdict for a pending event
//	GET  /v1/pending                  list all pending OCS items
//	GET  /v1/events/{event_id}        get a DAG event by ID
//	GET  /v1/dag/tips                 list current DAG tip event IDs
//	GET  /v1/status                   node health snapshot
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/network"
	"github.com/aethernet/core/internal/ocs"
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
	mux.HandleFunc("GET /v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /v1/agents/{agent_id}/balance", s.handleGetBalance)
	mux.HandleFunc("GET /v1/agents/{agent_id}", s.handleGetAgent)
	mux.HandleFunc("POST /v1/transfer", s.handleTransfer)
	mux.HandleFunc("POST /v1/generation", s.handleGeneration)
	mux.HandleFunc("POST /v1/verify", s.handleVerify)
	mux.HandleFunc("GET /v1/pending", s.handlePending)
	mux.HandleFunc("GET /v1/events/{event_id}", s.handleGetEvent)
	mux.HandleFunc("GET /v1/dag/tips", s.handleDAGTips)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux = mux
	s.srv = &http.Server{Addr: listenAddr, Handler: mux}

	return s
}

// ServeHTTP implements http.Handler. This allows the Server to be mounted
// directly in httptest.NewServer for tests without binding a real TCP port.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	AgentID         string `json:"agent_id"`
	FingerprintHash string `json:"fingerprint_hash"`
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
func (s *Server) handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req registerAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	fp, err := identity.NewFingerprint(s.agentID, s.kp.PublicKey, req.Capabilities)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create fingerprint: "+err.Error())
		return
	}

	if err := s.registry.Register(fp); err != nil {
		if errors.Is(err, identity.ErrAgentAlreadyExists) {
			existing, _ := s.registry.Get(s.agentID)
			writeJSON(w, http.StatusOK, registerAgentResponse{
				AgentID:         string(existing.AgentID),
				FingerprintHash: existing.FingerprintHash,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, registerAgentResponse{
		AgentID:         string(fp.AgentID),
		FingerprintHash: fp.FingerprintHash,
	})
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
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	var req generationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
