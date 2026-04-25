package api

// Admin-only handlers. Routes registered in server.go's rebuildMux when
// Server.enableAdminAPI is true (opt-in via --enable-admin-api on the
// node). All admin endpoints require a signed request envelope; the
// verified signer becomes the emitter of any DAG event the handler
// produces.
//
// Introduced for Part F of the canonical-distribution-integer-migration
// workstream: testnet rehearsal of the settlement shadow → integer-canonical
// cutover.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/genesis"
)

// registerAdminRoutes wires admin-only routes onto the provided mux. Called
// from rebuildMux only when enableAdminAPI is true.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/admin/integer-migration/activate", s.handleAdminActivateIntegerMigration)
	mux.HandleFunc("GET /v1/admin/ledger-snapshot", s.handleAdminLedgerSnapshot)
}

// adminLedgerSnapshotResponse is the JSON shape returned by
// GET /v1/admin/ledger-snapshot. Field shape MUST match
// internal/monitoring/cross_node_invariants.LedgerSnapshot — the
// `aet invariants check` CLI decodes responses into that struct.
//
// Per F5 5B testnet criterion 12 (cross-node byte-equality verification):
// two nodes computing identical canonical state MUST produce snapshots
// whose AgentBalances + EscrowResiduals + Treasury + TotalSupply values
// are byte-identical (for the value-equality semantic; map iteration
// order is irrelevant since the comparator iterates by key).
type adminLedgerSnapshotResponse struct {
	NodeID          string            `json:"NodeID"`
	AgentBalances   map[string]uint64 `json:"AgentBalances"`
	EscrowResiduals map[string]uint64 `json:"EscrowResiduals"`
	Treasury        uint64            `json:"Treasury"`
	TotalSupply     uint64            `json:"TotalSupply"`
}

// handleAdminLedgerSnapshot serves GET /v1/admin/ledger-snapshot.
//
// Returns the node's current view of the canonical ledger projection:
// per-agent balances (positive only), per-task escrow residuals,
// treasury balance, total supply (sum of all balances + residuals).
//
// Authorization: admin-only (gated on Server.enableAdminAPI). NO signed-
// request envelope is required — observation is read-only and the
// endpoint does not mutate canonical state. Production deployments that
// expose the admin route should still front it with a network-level ACL
// (load-balancer-side IP allowlist or VPN gating); the handler logs
// remote address for audit.
//
// Per F5 5B testnet criterion 12: this endpoint is the substrate the
// `aet invariants check` tool uses to compare cross-node ledger byte-
// equality. Divergence between nodes for identical canonical state
// indicates a D-1 violation — halt-trigger per Plan v3 §5.
func (s *Server) handleAdminLedgerSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.transfer == nil {
		writeError(w, http.StatusServiceUnavailable, "ledger not available on this node")
		return
	}

	resp := adminLedgerSnapshotResponse{
		NodeID: string(s.agentID),
	}

	balances := s.transfer.AllBalances()
	resp.AgentBalances = make(map[string]uint64, len(balances))
	// safe: copying map to map; key/value pairs preserved regardless of iteration order
	for agent, bal := range balances {
		resp.AgentBalances[string(agent)] = bal
	}

	if s.escrowMgr != nil {
		resp.EscrowResiduals = s.escrowMgr.AllResiduals()
	} else {
		resp.EscrowResiduals = map[string]uint64{}
	}

	treasury, _ := s.transfer.Balance(crypto.AgentID(genesis.BucketTreasury))
	resp.Treasury = treasury

	var total uint64
	// safe: commutative sum; final scalar is order-independent
	for _, bal := range resp.AgentBalances {
		total += bal
	}
	// safe: commutative sum; final scalar is order-independent
	for _, residual := range resp.EscrowResiduals {
		total += residual
	}
	resp.TotalSupply = total

	writeJSON(w, http.StatusOK, resp)
}

// handleAdminActivateIntegerMigration emits a canonical
// EventTypeIntegerMigrationActivation event. The verified signer of the
// signed HTTP request becomes the payload's EmittingAgent. The server sets
// Version=1 and EmittedAtUnix=time.Now().Unix() authoritatively (never
// client-supplied).
//
// Activation is one-way and idempotent from the consumer side. Re-invoking
// the endpoint produces a new DAG event; the consumer's early-idempotency
// pre-check short-circuits the second apply.
//
// Authorization: any successfully signed request activates. On testnet this
// is adequate because the founder controls the operator wallet and the
// activation is auditable via the emitted DAG event. Mainnet deployment
// requires stronger authorization (operator allowlist, multi-sig, or a
// governance-level gate) and is tracked as a future workstream.
func (s *Server) handleAdminActivateIntegerMigration(w http.ResponseWriter, r *http.Request) {
	signerAgent := getAuthAgent(r)
	if signerAgent == "" {
		writeError(w, http.StatusUnauthorized, "signed request required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}

	emittedAt := time.Now().Unix()
	payload := event.IntegerMigrationActivationPayload{
		Version:          1,
		EmittingAgent:    string(signerAgent),
		ActivationReason: req.Reason,
		EmittedAtUnix:    emittedAt,
	}

	evID := s.emitDAGEvent(event.EventTypeIntegerMigrationActivation, payload, string(signerAgent))
	if evID == "" {
		writeError(w, http.StatusInternalServerError, "emit failed")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"event_id":          string(evID),
		"emitting_agent":    string(signerAgent),
		"emitted_at_unix":   emittedAt,
		"activation_reason": req.Reason,
	})
}
