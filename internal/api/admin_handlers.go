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

	"github.com/Aethernet-network/aethernet/internal/event"
)

// registerAdminRoutes wires admin-only routes onto the provided mux. Called
// from rebuildMux only when enableAdminAPI is true.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/admin/integer-migration/activate", s.handleAdminActivateIntegerMigration)
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
