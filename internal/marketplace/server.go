// Package marketplace implements the AetherNet marketplace HTTP server.
//
// The marketplace layer sits above the protocol layer and manages task
// lifecycle, escrow, autonomous routing, service discovery, and the web
// explorer. It can run embedded in the combined node binary (--marketplace flag)
// or as a standalone cmd/marketplace binary that connects to the protocol node
// via its public API.
//
// Endpoints served:
//
//	POST /v1/tasks                          create a new task
//	GET  /v1/tasks                          list tasks (filters: status, category, limit)
//	GET  /v1/tasks/stats                    marketplace statistics
//	GET  /v1/tasks/agent/{agent_id}         tasks for a specific agent
//	GET  /v1/tasks/{id}                     get task by ID
//	POST /v1/tasks/{id}/claim               claim an open task
//	POST /v1/tasks/{id}/submit              submit result
//	POST /v1/tasks/{id}/approve             approve submitted result
//	POST /v1/tasks/{id}/dispute             dispute submitted result
//	POST /v1/tasks/{id}/cancel              cancel an open task
//	POST /v1/tasks/{id}/subtask             create a child task
//	GET  /v1/tasks/subtasks/{id}            list subtasks for a task
//	POST /v1/router/register                register agent capability profile
//	DELETE /v1/router/register/{agent_id}   unregister agent
//	PUT  /v1/router/availability/{agent_id} toggle agent availability
//	GET  /v1/router/agents                  list registered capabilities
//	GET  /v1/router/routes                  recent routing decisions
//	GET  /v1/router/stats                   router statistics
//	POST /v1/registry                       register service listing
//	GET  /v1/registry/search                search listings
//	GET  /v1/registry/categories            list service categories
//	DELETE /v1/registry/{agent_id}          deactivate listing
//	GET  /v1/registry/{agent_id}            get listing
//	GET  /v1/discover                       capability-aware agent matching
//	GET  /v1/agents/{id}/reputation         agent reputation profile
//	GET  /v1/reputation/rankings            category leaderboard
//	GET  /explorer/                         web dashboard
package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/ratelimit"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// taskRouterInterface is the subset of *router.Router used by the marketplace.
type taskRouterInterface interface {
	RegisterCapability(cap router.AgentCapability)
	UnregisterCapability(agentID crypto.AgentID)
	SetAvailability(agentID crypto.AgentID, available bool) bool
	RegisteredAgents() []*router.AgentCapability
	RecentRoutes(limit int) []*router.RouteResult
	Stats() map[string]any
}

// Server is the AetherNet marketplace HTTP server.
//
// It serves marketplace-specific endpoints (tasks, routing, discovery,
// service registry) and optionally the web explorer. All components are
// optional — endpoints return 501 when the corresponding component is nil.
type Server struct {
	listenAddr string

	// Marketplace components.
	taskMgr       *tasks.TaskManager
	escrowMgr     *escrow.Escrow
	svcRegistry   *registry.Registry
	reputationMgr *reputation.ReputationManager
	discoveryEng  *discovery.Engine
	taskRouter    taskRouterInterface

	// explorerDir is the directory to serve the web dashboard from.
	explorerDir string

	// Rate limiters applied in ServeHTTP (NEW-4).
	writeLimiter *ratelimit.Limiter // POST/PUT/DELETE endpoints
	readLimiter  *ratelimit.Limiter // GET endpoints

	// requireAuth gates mutating registry operations (NEW-9).
	requireAuth bool

	mux *http.ServeMux
	srv *http.Server
}

// New creates a marketplace Server bound to listenAddr.
func New(listenAddr string) *Server {
	s := &Server{
		listenAddr:   listenAddr,
		writeLimiter: ratelimit.New(ratelimit.DefaultConfig()),
		readLimiter:  ratelimit.New(ratelimit.ReadOnlyConfig()),
	}
	s.mux = s.buildMux()
	s.srv = &http.Server{Addr: listenAddr, Handler: s}
	return s
}

// SetRequireAuth enables mandatory ownership checks on mutating registry
// operations. Off by default (testnet compatibility). Enable for production
// deployments where callers must authenticate before modifying listings (NEW-9).
func (s *Server) SetRequireAuth(v bool) {
	s.requireAuth = v
}

// SetTaskManager wires the task manager and escrow system.
func (s *Server) SetTaskManager(tm *tasks.TaskManager, e *escrow.Escrow) {
	s.taskMgr = tm
	s.escrowMgr = e
}

// SetServiceRegistry wires the service registry.
func (s *Server) SetServiceRegistry(r *registry.Registry) {
	s.svcRegistry = r
}

// SetReputationManager wires reputation tracking.
func (s *Server) SetReputationManager(rm *reputation.ReputationManager) {
	s.reputationMgr = rm
}

// SetDiscoveryEngine wires capability-aware discovery.
func (s *Server) SetDiscoveryEngine(e *discovery.Engine) {
	s.discoveryEng = e
}

// SetTaskRouter wires the autonomous task router.
func (s *Server) SetTaskRouter(r taskRouterInterface) {
	s.taskRouter = r
}

// SetExplorerDir sets the directory from which to serve the web explorer.
// Call before Start.
func (s *Server) SetExplorerDir(dir string) {
	s.explorerDir = dir
	s.mux = s.buildMux()
}

// Start binds the TCP listener and begins serving marketplace requests.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("marketplace: listen %s: %w", s.listenAddr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("marketplace server error", "err", err)
		}
	}()
	slog.Info("marketplace server started", "addr", s.listenAddr)
	return nil
}

// Stop gracefully shuts down the marketplace server (5 s timeout).
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	if s.writeLimiter != nil {
		s.writeLimiter.Stop()
	}
	if s.readLimiter != nil {
		s.readLimiter.Stop()
	}
}

// ServeHTTP implements http.Handler, adding CORS headers and rate limiting to
// every response (NEW-4). Write operations (POST/PUT/DELETE) use the tighter
// writeLimiter; GETs use the more permissive readLimiter.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ip := ratelimit.ExtractIP(r)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if s.readLimiter != nil && !s.readLimiter.Allow(ip) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
	default:
		if s.writeLimiter != nil && !s.writeLimiter.Allow(ip) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
	}

	s.mux.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Mux construction
// ---------------------------------------------------------------------------

func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Root info.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":    "AetherNet Marketplace",
			"version": "0.1.0",
			"docs":    "https://github.com/Aethernet-network/aethernet",
			"endpoints": map[string]string{
				"tasks":    "/v1/tasks",
				"router":   "/v1/router",
				"discover": "/v1/discover",
				"registry": "/v1/registry",
				"explorer": "/explorer/",
			},
		})
	})

	// Task marketplace — literal sub-paths beat wildcards in Go 1.22+ routing.
	mux.HandleFunc("POST /v1/tasks", s.handlePostTask)
	mux.HandleFunc("GET /v1/tasks/stats", s.handleTaskStats)
	mux.HandleFunc("GET /v1/tasks/agent/{agent_id}", s.handleAgentTasks)
	mux.HandleFunc("GET /v1/tasks/subtasks/{id}", s.handleGetSubtasks)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/tasks/result/{id}", s.handleGetTaskResult)
	mux.HandleFunc("POST /v1/tasks/{id}/claim", s.handleClaimTask)
	mux.HandleFunc("POST /v1/tasks/{id}/submit", s.handleSubmitTask)
	mux.HandleFunc("POST /v1/tasks/{id}/approve", s.handleApproveTask)
	mux.HandleFunc("POST /v1/tasks/{id}/dispute", s.handleDisputeTask)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleCancelTask)
	mux.HandleFunc("POST /v1/tasks/{id}/subtask", s.handleCreateSubtask)

	// Autonomous task router.
	mux.HandleFunc("POST /v1/router/register", s.handleRouterRegister)
	mux.HandleFunc("DELETE /v1/router/register/{agent_id}", s.handleRouterUnregister)
	mux.HandleFunc("PUT /v1/router/availability/{agent_id}", s.handleRouterAvailability)
	mux.HandleFunc("GET /v1/router/agents", s.handleRouterAgents)
	mux.HandleFunc("GET /v1/router/routes", s.handleRouterRoutes)
	mux.HandleFunc("GET /v1/router/stats", s.handleRouterStats)

	// Service registry.
	mux.HandleFunc("POST /v1/registry", s.handlePostRegistry)
	mux.HandleFunc("GET /v1/registry/search", s.handleSearchRegistry)
	mux.HandleFunc("GET /v1/registry/categories", s.handleRegistryCategories)
	mux.HandleFunc("DELETE /v1/registry/{agent_id}", s.handleDeleteRegistry)
	mux.HandleFunc("GET /v1/registry/{agent_id}", s.handleGetRegistryListing)

	// Discovery.
	mux.HandleFunc("GET /v1/discover", s.handleDiscover)

	// Reputation.
	mux.HandleFunc("GET /v1/agents/{id}/reputation", s.handleGetReputation)
	mux.HandleFunc("GET /v1/reputation/rankings", s.handleGetReputationRankings)

	// Explorer static files.
	if s.explorerDir != "" {
		mux.Handle("GET /explorer/", http.StripPrefix("/explorer/", http.FileServer(http.Dir(s.explorerDir))))
	}

	return mux
}

// ---------------------------------------------------------------------------
// Task handlers
// ---------------------------------------------------------------------------

func (s *Server) handlePostTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		PosterID       string   `json:"poster_id"`
		Title          string   `json:"title"`
		Description    string   `json:"description"`
		Category       string   `json:"category"`
		Budget         uint64   `json:"budget"`
		Tags           []string `json:"tags,omitempty"`
		DeliveryMethod string   `json:"delivery_method,omitempty"`
		// AcceptanceContract fields — all optional; defaults applied in PostTask.
		SuccessCriteria     []string `json:"success_criteria,omitempty"`
		RequiredChecks      []string `json:"required_checks,omitempty"`
		PolicyVersion       string   `json:"policy_version,omitempty"`
		ChallengeWindowSecs int64    `json:"challenge_window_secs,omitempty"`
		GenerationEligible  *bool    `json:"generation_eligible,omitempty"`
		MaxDeliveryTimeSecs int64    `json:"max_delivery_time_secs,omitempty"`
		// AssuranceLane selects the service-guarantee tier. "" = unassured.
		AssuranceLane string `json:"assurance_lane,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	task, err := s.taskMgr.PostTask(req.PosterID, req.Title, req.Description, req.Category, req.Budget, tasks.PostTaskOpts{
		DeliveryMethod:      req.DeliveryMethod,
		SuccessCriteria:     req.SuccessCriteria,
		RequiredChecks:      req.RequiredChecks,
		PolicyVersion:       req.PolicyVersion,
		ChallengeWindowSecs: req.ChallengeWindowSecs,
		GenerationEligible:  req.GenerationEligible,
		MaxDeliveryTimeSecs: req.MaxDeliveryTimeSecs,
		AssuranceLane:       req.AssuranceLane,
	})
	if err != nil {
		var sfe *tasks.SecurityFloorError
		if errors.As(err, &sfe) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":          "insufficient_category_security",
				"requested_lane": sfe.RequestedLane,
				"offered_lane":   sfe.OfferedLane,
				"message":        err.Error(),
			})
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Budget > 0 && req.PosterID != "" {
		if err := s.escrowMgr.Hold(task.ID, crypto.AgentID(req.PosterID), req.Budget); err != nil {
			slog.Warn("marketplace: escrow hold failed", "task_id", task.ID, "err", err)
		}
	}

	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	category := q.Get("category")
	limit := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	all := s.taskMgr.Search(tasks.TaskStatus(status), category, limit)
	if all == nil {
		all = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, all)
}

func (s *Server) handleTaskStats(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.taskMgr.Stats())
}

func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	agentID := r.PathValue("agent_id")
	all := s.taskMgr.Search("", "", 0)
	result := make([]*tasks.Task, 0)
	for _, t := range all {
		if t.PosterID == agentID || t.ClaimerID == agentID {
			result = append(result, t)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	task, err := s.taskMgr.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id := r.PathValue("id")
	var req struct {
		ClaimerID string `json:"claimer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := s.taskMgr.ClaimTask(id, crypto.AgentID(req.ClaimerID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, _ := s.taskMgr.Get(id)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id := r.PathValue("id")
	var req struct {
		ClaimerID       string             `json:"claimer_id"`
		ResultHash      string             `json:"result_hash"`
		ResultNote      string             `json:"result_note,omitempty"`
		ResultURI       string             `json:"result_uri,omitempty"`
		Evidence        *evidence.Evidence `json:"evidence,omitempty"`
		ResultContent   string             `json:"result_content,omitempty"`
		ResultEncrypted bool               `json:"result_encrypted,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

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

	if err := s.taskMgr.SubmitResult(id, crypto.AgentID(req.ClaimerID), resultHash, resultNote, resultURI, req.Evidence); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.ResultContent != "" {
		if err := s.taskMgr.SetResultContent(id, req.ResultContent, req.ResultEncrypted); err != nil {
			slog.Warn("marketplace: could not store result content", "task_id", id, "err", err)
		}
	}

	if req.Evidence != nil {
		if t2, err2 := s.taskMgr.Get(id); err2 == nil {
			verifier := evidence.NewVerifier()
			if score, ok := verifier.Verify(req.Evidence, t2.Title, t2.Description, t2.Budget); ok {
				_ = s.taskMgr.SetVerificationScore(id, score)
			}
		}
	}

	task, _ := s.taskMgr.Get(id)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleGetTaskResult(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	taskID := r.PathValue("id")
	task, err := s.taskMgr.Get(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":          task.ID,
		"status":           task.Status,
		"delivery_method":  task.DeliveryMethod,
		"result_content":   task.ResultContent,
		"result_encrypted": task.ResultEncrypted,
	})
}

func (s *Server) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id := r.PathValue("id")
	var req struct {
		ApproverID string `json:"approver_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	taskBefore, err := s.taskMgr.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := s.taskMgr.ApproveTask(id, crypto.AgentID(req.ApproverID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if taskBefore.ClaimerID != "" {
		if err := s.escrowMgr.Release(id, crypto.AgentID(taskBefore.ClaimerID)); err != nil {
			slog.Error("marketplace: handleApproveTask: escrow release failed",
				"task_id", id, "claimer", taskBefore.ClaimerID, "err", err)
			writeError(w, http.StatusInternalServerError, "escrow release failed")
			return
		}
	}

	task, _ := s.taskMgr.Get(id)
	if s.reputationMgr != nil && taskBefore.ClaimerID != "" {
		verScore := 0.5
		if task != nil && task.VerificationScore != nil {
			verScore = task.VerificationScore.Overall
		}
		deliverySecs := 60.0
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

func (s *Server) handleDisputeTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id := r.PathValue("id")
	var req struct {
		PosterID string `json:"poster_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	taskBefore, _ := s.taskMgr.Get(id)

	if err := s.taskMgr.DisputeTask(id, crypto.AgentID(req.PosterID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if s.reputationMgr != nil && taskBefore != nil && taskBefore.ClaimerID != "" {
		s.reputationMgr.RecordFailure(crypto.AgentID(taskBefore.ClaimerID), taskBefore.Category)
	}

	task, _ := s.taskMgr.Get(id)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil || s.escrowMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id := r.PathValue("id")
	var req struct {
		PosterID string `json:"poster_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := s.taskMgr.CancelTask(id, crypto.AgentID(req.PosterID)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.escrowMgr.Refund(id); err != nil {
		slog.Error("marketplace: handleCancelTask: escrow refund failed",
			"task_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "escrow refund failed")
		return
	}

	task, _ := s.taskMgr.Get(id)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCreateSubtask(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	parentID := r.PathValue("id")
	var req struct {
		ClaimerID   string `json:"claimer_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Budget      uint64 `json:"budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	subtask, err := s.taskMgr.CreateSubtask(parentID, crypto.AgentID(req.ClaimerID), req.Title, req.Description, req.Category, req.Budget)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, subtask)
}

func (s *Server) handleGetSubtasks(w http.ResponseWriter, r *http.Request) {
	if s.taskMgr == nil {
		writeError(w, http.StatusNotImplemented, "task marketplace not enabled")
		return
	}
	parentID := r.PathValue("id")
	parent, err := s.taskMgr.Get(parentID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	subtasks := make([]*tasks.Task, 0)
	for _, subID := range parent.SubtaskIDs {
		if sub, err := s.taskMgr.Get(subID); err == nil {
			subtasks = append(subtasks, sub)
		}
	}
	writeJSON(w, http.StatusOK, subtasks)
}

// ---------------------------------------------------------------------------
// Router handlers
// ---------------------------------------------------------------------------

func (s *Server) handleRouterRegister(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var cap router.AgentCapability
	if err := json.NewDecoder(r.Body).Decode(&cap); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if cap.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	cap.Available = true
	s.taskRouter.RegisterCapability(cap)
	writeJSON(w, http.StatusCreated, cap)
}

func (s *Server) handleRouterUnregister(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	s.taskRouter.UnregisterCapability(crypto.AgentID(r.PathValue("agent_id")))
	writeJSON(w, http.StatusOK, map[string]string{"status": "unregistered"})
}

func (s *Server) handleRouterAvailability(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))
	if ok := s.taskRouter.SetAvailability(agentID, req.Available); !ok {
		writeError(w, http.StatusNotFound, "agent not registered")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": req.Available})
}

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

func (s *Server) handleRouterRoutes(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	routes := s.taskRouter.RecentRoutes(limit)
	if routes == nil {
		routes = []*router.RouteResult{}
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Server) handleRouterStats(w http.ResponseWriter, r *http.Request) {
	if s.taskRouter == nil {
		writeError(w, http.StatusNotImplemented, "task router not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.taskRouter.Stats())
}

// ---------------------------------------------------------------------------
// Service registry handlers
// ---------------------------------------------------------------------------

func (s *Server) handlePostRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var listing registry.ServiceListing
	if err := json.NewDecoder(r.Body).Decode(&listing); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s.svcRegistry.Register(&listing)
	updated, _ := s.svcRegistry.Get(crypto.AgentID(listing.AgentID))
	writeJSON(w, http.StatusCreated, updated)
}

func (s *Server) handleSearchRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	q := r.URL.Query()
	limit := 20
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	results := s.svcRegistry.Search(q.Get("q"), q.Get("category"), limit)
	if results == nil {
		results = []*registry.ServiceListing{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleRegistryCategories(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	writeJSON(w, http.StatusOK, s.svcRegistry.Categories())
}

func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	if s.svcRegistry == nil {
		writeError(w, http.StatusNotImplemented, "service registry not enabled")
		return
	}
	agentID := crypto.AgentID(r.PathValue("agent_id"))
	// Ownership check: when requireAuth is enabled the caller must prove they
	// own the listing by supplying a matching X-Agent-ID header. This prevents
	// arbitrary agents from deactivating other agents' listings (NEW-9).
	if s.requireAuth {
		callerID := crypto.AgentID(r.Header.Get("X-Agent-ID"))
		if callerID != agentID {
			writeError(w, http.StatusUnauthorized, "X-Agent-ID header must match the listing agent_id")
			return
		}
	}
	if ok := s.svcRegistry.Deactivate(agentID); !ok {
		writeError(w, http.StatusNotFound, "listing not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}

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

// ---------------------------------------------------------------------------
// Discovery handler
// ---------------------------------------------------------------------------

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if s.discoveryEng == nil {
		writeError(w, http.StatusNotImplemented, "discovery engine not enabled")
		return
	}
	q := r.URL.Query()

	maxBudget := uint64(0)
	if mb := q.Get("max_budget"); mb != "" {
		if n, err := strconv.ParseUint(mb, 10, 64); err == nil {
			maxBudget = n
		}
	}
	minRep := 0.0
	if mr := q.Get("min_reputation"); mr != "" {
		if f, err := strconv.ParseFloat(mr, 64); err == nil {
			minRep = f
		}
	}
	limit := 10
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	matches := s.discoveryEng.FindAgents(q.Get("q"), q.Get("category"), maxBudget, minRep, limit)
	if matches == nil {
		matches = []*discovery.Match{}
	}
	writeJSON(w, http.StatusOK, matches)
}

// ---------------------------------------------------------------------------
// Reputation handlers
// ---------------------------------------------------------------------------

func (s *Server) handleGetReputation(w http.ResponseWriter, r *http.Request) {
	if s.reputationMgr == nil {
		writeError(w, http.StatusNotImplemented, "reputation tracking not enabled")
		return
	}
	rep := s.reputationMgr.GetReputation(crypto.AgentID(r.PathValue("id")))
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleGetReputationRankings(w http.ResponseWriter, r *http.Request) {
	if s.reputationMgr == nil {
		writeError(w, http.StatusNotImplemented, "reputation tracking not enabled")
		return
	}
	q := r.URL.Query()
	category := q.Get("category")
	if category == "" {
		writeError(w, http.StatusBadRequest, "category parameter is required")
		return
	}
	limit := 10
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	rankings := s.reputationMgr.RankByCategory(category, limit)
	if rankings == nil {
		rankings = []*reputation.AgentReputation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"category": category,
		"rankings": rankings,
	})
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
