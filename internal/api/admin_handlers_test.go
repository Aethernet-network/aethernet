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
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/ledger"
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

// TestAdminLedgerSnapshot_IdenticalStateProducesIdenticalBytes verifies
// the load-bearing F5 5B testnet criterion 12 invariant: two servers
// observing identical canonical state MUST produce identical snapshot
// payloads (modulo NodeID, which encodes per-server identity).
//
// Two servers share the same TransferLedger + Escrow instance (the
// canonical state); their snapshot endpoints emit byte-identical
// AgentBalances + EscrowResiduals + Treasury + TotalSupply values.
func TestAdminLedgerSnapshot_IdenticalStateProducesIdenticalBytes(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketTreasury), 100_000); err != nil {
		t.Fatalf("fund treasury: %v", err)
	}
	if err := tl.FundAgent(crypto.AgentID("agent-a"), 50_000); err != nil {
		t.Fatalf("fund agent-a: %v", err)
	}
	em := escrow.New(tl)

	s1 := &Server{transfer: tl, escrowMgr: em, agentID: "node-1", enableAdminAPI: true}
	s2 := &Server{transfer: tl, escrowMgr: em, agentID: "node-2", enableAdminAPI: true}

	snap1 := fetchSnapshot(t, s1)
	snap2 := fetchSnapshot(t, s2)

	if snap1.NodeID == snap2.NodeID {
		t.Fatalf("NodeID should differ: %q == %q", snap1.NodeID, snap2.NodeID)
	}
	if snap1.Treasury != snap2.Treasury {
		t.Errorf("Treasury divergence: %d vs %d", snap1.Treasury, snap2.Treasury)
	}
	if snap1.TotalSupply != snap2.TotalSupply {
		t.Errorf("TotalSupply divergence: %d vs %d", snap1.TotalSupply, snap2.TotalSupply)
	}
	if !mapsEqualUint64(snap1.AgentBalances, snap2.AgentBalances) {
		t.Errorf("AgentBalances divergence:\n  s1=%v\n  s2=%v", snap1.AgentBalances, snap2.AgentBalances)
	}
	if !mapsEqualUint64(snap1.EscrowResiduals, snap2.EscrowResiduals) {
		t.Errorf("EscrowResiduals divergence:\n  s1=%v\n  s2=%v", snap1.EscrowResiduals, snap2.EscrowResiduals)
	}
}

// TestAdminLedgerSnapshot_DivergedStateProducesDifferentBytes verifies
// the negative case: two servers with diverged ledger state produce
// snapshots whose AgentBalances diverge.
func TestAdminLedgerSnapshot_DivergedStateProducesDifferentBytes(t *testing.T) {
	tl1 := ledger.NewTransferLedger()
	tl2 := ledger.NewTransferLedger()
	if err := tl1.FundAgent(crypto.AgentID("agent-a"), 50_000); err != nil {
		t.Fatalf("fund tl1: %v", err)
	}
	if err := tl2.FundAgent(crypto.AgentID("agent-a"), 75_000); err != nil {
		t.Fatalf("fund tl2: %v", err)
	}

	s1 := &Server{transfer: tl1, agentID: "node-1", enableAdminAPI: true}
	s2 := &Server{transfer: tl2, agentID: "node-2", enableAdminAPI: true}

	snap1 := fetchSnapshot(t, s1)
	snap2 := fetchSnapshot(t, s2)

	if snap1.AgentBalances["agent-a"] == snap2.AgentBalances["agent-a"] {
		t.Fatalf("agent-a balances should differ; both = %d", snap1.AgentBalances["agent-a"])
	}
	if snap1.TotalSupply == snap2.TotalSupply {
		t.Fatalf("TotalSupply should differ; both = %d", snap1.TotalSupply)
	}
}

// TestAdminLedgerSnapshot_TotalSupplyConservation verifies TotalSupply
// equals the sum of AgentBalances + EscrowResiduals at any moment. This
// is the conservation invariant that the cross-node monitor uses to
// flag protocol-level conservation violations (sum mismatch on a single
// node's snapshot indicates internal accounting bug, not just cross-
// node divergence).
func TestAdminLedgerSnapshot_TotalSupplyConservation(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID("agent-a"), 30_000); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := tl.FundAgent(crypto.AgentID("agent-b"), 20_000); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketTreasury), 50_000); err != nil {
		t.Fatalf("fund treasury: %v", err)
	}
	em := escrow.New(tl)

	s := &Server{transfer: tl, escrowMgr: em, agentID: "node", enableAdminAPI: true}
	snap := fetchSnapshot(t, s)

	var sumBalances, sumResiduals uint64
	for _, v := range snap.AgentBalances {
		sumBalances += v
	}
	for _, v := range snap.EscrowResiduals {
		sumResiduals += v
	}
	want := sumBalances + sumResiduals
	if snap.TotalSupply != want {
		t.Fatalf("TotalSupply=%d, want sum(balances)=%d + sum(residuals)=%d = %d",
			snap.TotalSupply, sumBalances, sumResiduals, want)
	}
}

func fetchSnapshot(t *testing.T, s *Server) adminLedgerSnapshotResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/ledger-snapshot", nil)
	w := httptest.NewRecorder()
	s.handleAdminLedgerSnapshot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", w.Code, w.Body.String())
	}
	var resp adminLedgerSnapshotResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func mapsEqualUint64(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
