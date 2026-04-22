package api

// Internal tests for the admin API handlers. Uses same-package construction
// (not api_test) so the tests can inject the auth context directly and
// exercise the handler without routing through the signed-request
// middleware, which is covered separately by internal/auth tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/auth"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// captureTestPublisher records events passed to Publish for assertion.
// Satisfies localEventPublisher.
type captureTestPublisher struct {
	mu     sync.Mutex
	events []*event.Event
}

func (p *captureTestPublisher) Publish(ev *event.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func (p *captureTestPublisher) List() []*event.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*event.Event, len(p.events))
	copy(out, p.events)
	return out
}

// newAdminTestServer constructs the minimal Server state required to
// exercise the admin handler. emitDAGEvent needs dag, kp, agentID, and
// publisher.
func newAdminTestServer(t *testing.T) (*Server, *captureTestPublisher) {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pub := &captureTestPublisher{}
	s := &Server{
		dag:            dag.New(),
		kp:             kp,
		agentID:        kp.AgentID(),
		publisher:      pub,
		enableAdminAPI: true,
	}
	s.rebuildMux()
	return s, pub
}

// signerContext returns a request context marked as authenticated by the
// given agent ID via the deprecated AETHERNET-REQUEST-V1 envelope (which
// the server accepts at handler scope via getAuthAgent).
func signerContext(ctx context.Context, signer crypto.AgentID) context.Context {
	return context.WithValue(ctx, authContextKey, &auth.RequestAuth{AgentID: signer})
}

func TestAdminActivateIntegerMigration_Unauthenticated_Returns401(t *testing.T) {
	s, _ := newAdminTestServer(t)

	body, _ := json.Marshal(map[string]any{"reason": "test"})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleAdminActivateIntegerMigration(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d want 401", w.Code)
	}
}

func TestAdminActivateIntegerMigration_MissingReason_Returns400(t *testing.T) {
	s, _ := newAdminTestServer(t)

	signer := crypto.AgentID("signer-agent-1")
	body, _ := json.Marshal(map[string]any{"reason": ""})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	req = req.WithContext(signerContext(req.Context(), signer))
	w := httptest.NewRecorder()

	s.handleAdminActivateIntegerMigration(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "reason") {
		t.Errorf("expected error to mention reason; got %q", w.Body.String())
	}
}

func TestAdminActivateIntegerMigration_WhitespaceReason_Returns400(t *testing.T) {
	s, _ := newAdminTestServer(t)

	signer := crypto.AgentID("signer-agent-1")
	body, _ := json.Marshal(map[string]any{"reason": "   \t  "})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	req = req.WithContext(signerContext(req.Context(), signer))
	w := httptest.NewRecorder()

	s.handleAdminActivateIntegerMigration(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("whitespace reason should 400; got %d", w.Code)
	}
}

func TestAdminActivateIntegerMigration_Valid_Emits201AndCanonicalEvent(t *testing.T) {
	s, pub := newAdminTestServer(t)

	signer := crypto.AgentID("operator-xyz")
	const reason = "shadow-observation-complete"
	beforeUnix := time.Now().Unix()

	body, _ := json.Marshal(map[string]any{"reason": reason})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	req = req.WithContext(signerContext(req.Context(), signer))
	w := httptest.NewRecorder()

	s.handleAdminActivateIntegerMigration(w, req)

	afterUnix := time.Now().Unix()

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d want 201, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		EventID          string `json:"event_id"`
		EmittingAgent    string `json:"emitting_agent"`
		EmittedAtUnix    int64  `json:"emitted_at_unix"`
		ActivationReason string `json:"activation_reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if resp.EventID == "" {
		t.Error("event_id empty")
	}
	if resp.EmittingAgent != string(signer) {
		t.Errorf("emitting_agent = %q want %q", resp.EmittingAgent, signer)
	}
	if resp.ActivationReason != reason {
		t.Errorf("activation_reason = %q want %q", resp.ActivationReason, reason)
	}
	if resp.EmittedAtUnix < beforeUnix || resp.EmittedAtUnix > afterUnix {
		t.Errorf("emitted_at_unix=%d outside [%d, %d]", resp.EmittedAtUnix, beforeUnix, afterUnix)
	}

	events := pub.List()
	if len(events) != 1 {
		t.Fatalf("publisher saw %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != event.EventTypeIntegerMigrationActivation {
		t.Errorf("event type = %v want EventTypeIntegerMigrationActivation", ev.Type)
	}
	if string(ev.ID) != resp.EventID {
		t.Errorf("published event ID %q != response event_id %q", ev.ID, resp.EventID)
	}
	payload, err := event.GetPayload[event.IntegerMigrationActivationPayload](ev)
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Version != 1 {
		t.Errorf("payload.Version = %d want 1", payload.Version)
	}
	if payload.EmittingAgent != string(signer) {
		t.Errorf("payload.EmittingAgent = %q want %q", payload.EmittingAgent, signer)
	}
	if payload.ActivationReason != reason {
		t.Errorf("payload.ActivationReason = %q want %q", payload.ActivationReason, reason)
	}
	if payload.EmittedAtUnix != resp.EmittedAtUnix {
		t.Errorf("payload.EmittedAtUnix=%d != response.emitted_at_unix=%d", payload.EmittedAtUnix, resp.EmittedAtUnix)
	}
	// Activation has no semantic parent: consumer's Prerequisites returns nil
	// and the root-like event is causally self-contained. event.New replaces
	// nil with []EventID{}, so the slice is non-nil but empty.
	if len(ev.CausalRefs) != 0 {
		t.Errorf("expected 0 causal refs, got %d", len(ev.CausalRefs))
	}
}

func TestAdminAPI_DisabledByDefault_Returns404(t *testing.T) {
	// Default-constructed Server without SetAdminAPI(true) should not route
	// /v1/admin/integer-migration/activate to the handler — the mux omits
	// the admin group entirely.
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	s := &Server{
		dag:       dag.New(),
		kp:        kp,
		agentID:   kp.AgentID(),
		publisher: &captureTestPublisher{},
		// enableAdminAPI intentionally left false.
	}
	s.rebuildMux()

	body, _ := json.Marshal(map[string]any{"reason": "test"})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Go's ServeMux returns 405 Method Not Allowed when a pattern exists for
	// the path but not the method, and 404 when no pattern matches. Either
	// outcome proves the admin handler did not fire.
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("disabled admin API should 404 or 405; got %d body=%s", w.Code, w.Body.String())
	}
	// Under no circumstances should the handler have run and returned 201.
	if w.Code == http.StatusCreated {
		t.Errorf("admin handler fired while admin API was disabled")
	}
}

func TestAdminAPI_EnabledRoutes_Register(t *testing.T) {
	s, _ := newAdminTestServer(t)

	signer := crypto.AgentID("ops-1")
	body, _ := json.Marshal(map[string]any{"reason": "routing check"})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/integer-migration/activate", bytes.NewReader(body))
	req = req.WithContext(signerContext(req.Context(), signer))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("enabled admin API should serve handler; got %d body=%s", w.Code, w.Body.String())
	}
}
