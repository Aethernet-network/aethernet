// aet-e2e is a live-cluster end-to-end verification harness for the AetherNet
// 3-node testnet. It exercises the complete dissemination + settlement chain:
//
//   register agent → onboarding grant → grant dissemination to all nodes →
//   auto-validator votes → consensus finalization → settlement event creation →
//   settlement application → balance > 0 on all nodes
//
// Usage:
//
//	# Against the testnet ALB (default):
//	go run ./cmd/aet-e2e
//
//	# Against specific node IPs:
//	AETHERNET_E2E_NODES="http://44.200.60.102:8338,http://3.87.68.158:8338,http://100.27.227.231:8338" \
//	  go run ./cmd/aet-e2e
//
//	# Against local dev cluster:
//	AETHERNET_E2E_NODES="http://localhost:8338,http://localhost:8339,http://localhost:8340" \
//	  go run ./cmd/aet-e2e
//
// Environment variables:
//
//	AETHERNET_E2E_NODES     Comma-separated node URLs (default: testnet ALB)
//	AETHERNET_E2E_TIMEOUT   Per-stage timeout (default: 30s)
//	AETHERNET_E2E_VERBOSE   Set to "true" for detailed stage output
//
// Exit codes:
//
//	0  All stages passed
//	1  One or more stages failed (diagnostics printed to stderr)
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/auth"
	"github.com/Aethernet-network/aethernet/pkg/sdk"
)

// ---------------------------------------------------------------------------
// TX-V1 Signing Transport
// ---------------------------------------------------------------------------

// signingTransport wraps an http.RoundTripper and adds AETHERNET-TX-V1 signing
// headers to every request. This authenticates the harness as a real agent.
type signingTransport struct {
	inner      http.RoundTripper
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	actorHex   string // hex-encoded public key = agent identity
	chainID    string
}

func newSigningTransport(inner http.RoundTripper) *signingTransport {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("generate keypair: " + err.Error())
	}
	return &signingTransport{
		inner:      inner,
		privateKey: priv,
		publicKey:  pub,
		actorHex:   hex.EncodeToString(pub),
		chainID:    "aethernet-testnet-1",
	}
}

func (st *signingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Read body for hashing (if present).
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("signing: read body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	// Compute body hash (JCS-canonicalized if valid JSON, raw otherwise).
	canonBody, err := auth.CanonicalizeJSON(bodyBytes)
	if err != nil {
		canonBody = bodyBytes
	}
	bodyHash := sha256.Sum256(canonBody)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	// Generate nonce (16 random bytes = 32 hex chars).
	nonceBytes := make([]byte, 16)
	_, _ = rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)

	now := time.Now().Unix()

	// Build transaction envelope.
	tx := &auth.Transaction{
		Version:    auth.TxVersion,
		ChainID:    st.chainID,
		Actor:      st.actorHex,
		Method:     req.Method,
		Path:       req.URL.Path,
		BodySHA256: bodyHashHex,
		CreatedAt:  now,
		ExpiresAt:  now + 120,
		Nonce:      nonce,
	}

	// Sign.
	sig, err := tx.Sign(st.privateKey)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}

	// Set headers.
	req.Header.Set("X-AetherNet-Version", auth.TxVersion)
	req.Header.Set("X-AetherNet-Chain-ID", st.chainID)
	req.Header.Set("X-AetherNet-Actor", st.actorHex)
	req.Header.Set("X-AetherNet-Created", strconv.FormatInt(now, 10))
	req.Header.Set("X-AetherNet-Expires", strconv.FormatInt(now+120, 10))
	req.Header.Set("X-AetherNet-Nonce", nonce)
	req.Header.Set("X-AetherNet-Signature", sig)

	return st.inner.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type config struct {
	nodes   []string
	timeout time.Duration
	verbose bool
}

func loadConfig() config {
	c := config{
		timeout: 30 * time.Second,
	}

	if v := os.Getenv("AETHERNET_E2E_NODES"); v != "" {
		for _, u := range strings.Split(v, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				c.nodes = append(c.nodes, u)
			}
		}
	}
	if len(c.nodes) == 0 {
		// Default: testnet ALB (round-robins across all 3 nodes).
		c.nodes = []string{"https://testnet.aethernet.network"}
	}

	if v := os.Getenv("AETHERNET_E2E_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.timeout = d
		}
	}

	c.verbose = os.Getenv("AETHERNET_E2E_VERBOSE") == "true"
	return c
}

// ---------------------------------------------------------------------------
// Stage runner
// ---------------------------------------------------------------------------

type stage struct {
	name    string
	runFn   func() error
	passed  bool
	err     error
	elapsed time.Duration
}

func (s *stage) run() {
	start := time.Now()
	s.err = s.runFn()
	s.elapsed = time.Since(start)
	s.passed = s.err == nil
}

// ---------------------------------------------------------------------------
// Diagnostic helpers
// ---------------------------------------------------------------------------

func diagnoseDisseminationFailure(clients []*sdk.Client, eventID string) string {
	var b strings.Builder
	b.WriteString("  Dissemination diagnosis:\n")
	for i, c := range clients {
		status, err := c.Status()
		if err != nil {
			b.WriteString(fmt.Sprintf("    node %d: UNREACHABLE (%v)\n", i, err))
			continue
		}
		b.WriteString(fmt.Sprintf("    node %d: peers=%d dag=%d ocs_pending=%d\n",
			i, status.Peers, status.DAGSize, status.OCSPending))

		_, evErr := c.GetEvent(eventID)
		if evErr != nil {
			b.WriteString(fmt.Sprintf("    node %d: event %s NOT FOUND\n", i, eventID[:16]))
		} else {
			b.WriteString(fmt.Sprintf("    node %d: event %s FOUND\n", i, eventID[:16]))
		}
	}
	return b.String()
}

func diagnoseSettlementFailure(clients []*sdk.Client) string {
	var b strings.Builder
	b.WriteString("  Settlement diagnosis:\n")
	for i, c := range clients {
		status, err := c.Status()
		if err != nil {
			b.WriteString(fmt.Sprintf("    node %d: UNREACHABLE\n", i))
			continue
		}
		b.WriteString(fmt.Sprintf("    node %d: dag=%d ocs_pending=%d\n",
			i, status.DAGSize, status.OCSPending))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	// Generate an Ed25519 keypair for TX-V1 signing. The actor identity
	// is the hex-encoded public key — the same format the protocol uses.
	signer := newSigningTransport(http.DefaultTransport)
	signedHTTP := &http.Client{Timeout: cfg.timeout, Transport: signer}
	unsignedHTTP := &http.Client{Timeout: cfg.timeout}

	// Create SDK clients for each node. The first client uses the signed
	// transport for write operations (registration, etc.). All clients use
	// unsigned transport for read operations (status, balance).
	signedClients := make([]*sdk.Client, len(cfg.nodes))
	readClients := make([]*sdk.Client, len(cfg.nodes))
	for i, url := range cfg.nodes {
		signedClients[i] = sdk.New(url, signedHTTP)
		readClients[i] = sdk.New(url, unsignedHTTP)
	}
	// Use readClients for diagnostic functions that only need GET access.
	clients := readClients

	fmt.Println("AetherNet E2E Verification Harness")
	fmt.Println("===================================")
	fmt.Printf("Nodes:   %s\n", strings.Join(cfg.nodes, ", "))
	fmt.Printf("Actor:   %s\n", signer.actorHex[:16]+"...")
	fmt.Printf("ChainID: %s\n", signer.chainID)
	fmt.Printf("Timeout: %s\n\n", cfg.timeout)

	// Track state across stages.
	var agentID string
	var initialDAGSizes []int

	stages := []*stage{
		// ── Stage 1: All nodes reachable ──────────────────────────────────
		{name: "All nodes reachable", runFn: func() error {
			for i, c := range clients {
				status, err := c.Status()
				if err != nil {
					return fmt.Errorf("node %d (%s): unreachable: %w", i, cfg.nodes[i], err)
				}
				if cfg.verbose {
					fmt.Printf("  node %d: version=%s peers=%d dag=%d\n",
						i, status.Version, status.Peers, status.DAGSize)
				}
			}
			return nil
		}},

		// ── Stage 2: All nodes have peers ─────────────────────────────────
		{name: "Peers connected (peers >= 1)", runFn: func() error {
			for i, c := range clients {
				status, _ := c.Status()
				if status.Peers < 1 {
					return fmt.Errorf("node %d: peers=%d (want >= 1) — node is isolated", i, status.Peers)
				}
			}
			return nil
		}},

		// ── Stage 3: DAG beyond genesis ───────────────────────────────────
		{name: "DAG beyond genesis (size > 0)", runFn: func() error {
			initialDAGSizes = make([]int, len(clients))
			for i, c := range clients {
				status, _ := c.Status()
				initialDAGSizes[i] = status.DAGSize
				if status.DAGSize == 0 {
					return fmt.Errorf("node %d: dag_size=0 — genesis not applied", i)
				}
			}
			return nil
		}},

		// ── Stage 4: Register agent ───────────────────────────────────────
		{name: "Register agent (TX-V1 signed)", runFn: func() error {
			// Use the signed client — the signing transport adds
			// AETHERNET-TX-V1 headers to the POST /v1/agents request.
			id, err := signedClients[0].Register(nil)
			if err != nil {
				return fmt.Errorf("POST /v1/agents (signed): %w\n"+
					"  DIAGNOSIS: TX-V1 signing may be misconfigured.\n"+
					"  Actor: %s\n"+
					"  ChainID: %s", err, signer.actorHex[:16]+"...", signer.chainID)
			}
			agentID = id
			if cfg.verbose {
				fmt.Printf("  agent_id: %s\n", agentID)
			}
			return nil
		}},

		// ── Stage 5: Grant event exists on all nodes ──────────────────────
		{name: "Grant/registration disseminated", runFn: func() error {
			// The registration creates a grant event. Wait for DAG to grow
			// on all nodes (registration + grant + potential vote events).
			deadline := time.Now().Add(cfg.timeout)
			for time.Now().Before(deadline) {
				allGrew := true
				for i, c := range clients {
					status, _ := c.Status()
					if status.DAGSize <= initialDAGSizes[i] {
						allGrew = false
						break
					}
				}
				if allGrew {
					return nil
				}
				time.Sleep(500 * time.Millisecond)
			}
			// Collect diagnostic info.
			var failing []string
			for i, c := range clients {
				status, _ := c.Status()
				if status.DAGSize <= initialDAGSizes[i] {
					failing = append(failing, fmt.Sprintf("node %d: dag=%d (was %d)",
						i, status.DAGSize, initialDAGSizes[i]))
				}
			}
			return fmt.Errorf("DAG did not grow on all nodes after registration:\n  %s\n"+
				"  DIAGNOSIS: publication failure — events created on node 0 not reaching peers.\n"+
				"  Check: is pub.Publish wired? Is node.Broadcast sending? Are peers V2-negotiated?",
				strings.Join(failing, "\n  "))
		}},

		// ── Stage 6: OCS pending clears (consensus votes occur) ───────────
		{name: "OCS pending clears (consensus finalization)", runFn: func() error {
			deadline := time.Now().Add(cfg.timeout)
			for time.Now().Before(deadline) {
				status, _ := clients[0].Status()
				if status.OCSPending == 0 {
					return nil
				}
				if cfg.verbose {
					fmt.Printf("  ocs_pending=%d, waiting...\n", status.OCSPending)
				}
				time.Sleep(1 * time.Second)
			}
			status, _ := clients[0].Status()
			return fmt.Errorf("ocs_pending=%d (want 0) — consensus did not finalize.\n"+
				"  DIAGNOSIS: finalization failure.\n"+
				"  Check: are auto-validators running? Are vote events broadcast? Is MinParticipants <= active voters?\n%s",
				status.OCSPending, diagnoseSettlementFailure(clients))
		}},

		// ── Stage 7: Balance > 0 (settlement applied) ─────────────────────
		{name: "Balance > 0 (settlement applied)", runFn: func() error {
			if agentID == "" {
				return fmt.Errorf("no agent_id — registration stage failed")
			}
			deadline := time.Now().Add(cfg.timeout)
			for time.Now().Before(deadline) {
				bal, err := clients[0].Balance(agentID)
				if err == nil && bal.Balance > 0 {
					if cfg.verbose {
						fmt.Printf("  balance=%d %s\n", bal.Balance, bal.Currency)
					}
					return nil
				}
				time.Sleep(1 * time.Second)
			}
			bal, _ := clients[0].Balance(agentID)
			var balStr string
			if bal != nil {
				balStr = fmt.Sprintf("%d", bal.Balance)
			} else {
				balStr = "ERROR"
			}
			return fmt.Errorf("balance=%s (want > 0) — settlement not applied.\n"+
				"  DIAGNOSIS: settlement creation or application failure.\n"+
				"  Check: does SetFinalizationHandler fire? Does settlementApp.Apply run?\n"+
				"  Check: does RecordFromSync + Settle transition the entry to SettlementSettled?\n%s",
				balStr, diagnoseSettlementFailure(clients))
		}},

		// ── Stage 8: Balance converges across nodes ───────────────────────
		{name: "Balance converges across nodes", runFn: func() error {
			if agentID == "" || len(clients) < 2 {
				return nil // skip for single-node or failed registration
			}
			deadline := time.Now().Add(cfg.timeout)
			for time.Now().Before(deadline) {
				balances := make([]uint64, len(clients))
				allPositive := true
				for i, c := range clients {
					bal, err := c.Balance(agentID)
					if err != nil || bal.Balance == 0 {
						allPositive = false
						break
					}
					balances[i] = bal.Balance
				}
				if allPositive {
					// Check convergence: all balances equal.
					for i := 1; i < len(balances); i++ {
						if balances[i] != balances[0] {
							return fmt.Errorf("balance divergence: node 0=%d, node %d=%d\n"+
								"  DIAGNOSIS: settlement applied on some nodes but not others.\n"+
								"  Check: is the Settlement event disseminated to all peers?",
								balances[0], i, balances[i])
						}
					}
					if cfg.verbose {
						fmt.Printf("  all nodes: balance=%d\n", balances[0])
					}
					return nil
				}
				time.Sleep(1 * time.Second)
			}
			var nodeBalances []string
			for i, c := range clients {
				bal, err := c.Balance(agentID)
				if err != nil {
					nodeBalances = append(nodeBalances, fmt.Sprintf("node %d: ERROR (%v)", i, err))
				} else {
					nodeBalances = append(nodeBalances, fmt.Sprintf("node %d: %d", i, bal.Balance))
				}
			}
			return fmt.Errorf("balance did not converge:\n  %s\n"+
				"  DIAGNOSIS: settlement not applied on all nodes.\n"+
				"  Check: are Settlement events broadcast? Does settlementApp.Apply fire on peer nodes?",
				strings.Join(nodeBalances, "\n  "))
		}},
	}

	// ── Run stages ──────────────────────────────────────────────────────────
	passed := 0
	failed := 0
	for _, s := range stages {
		fmt.Printf("%-50s ", s.name+"...")
		s.run()
		if s.passed {
			passed++
			fmt.Printf("\033[1;32mPASS\033[0m (%s)\n", s.elapsed.Round(time.Millisecond))
		} else {
			failed++
			fmt.Printf("\033[1;31mFAIL\033[0m (%s)\n", s.elapsed.Round(time.Millisecond))
			fmt.Fprintf(os.Stderr, "\n  %s: %v\n\n", s.name, s.err)
		}
	}

	// ── Summary ─────────────────────────────────────────────────────────────
	fmt.Println()
	if failed == 0 {
		fmt.Printf("\033[1;32m  All %d stages passed.\033[0m\n", passed)
		fmt.Println("  Dissemination + settlement are working end-to-end.")
	} else {
		fmt.Printf("\033[1;31m  %d/%d stages failed.\033[0m\n", failed, passed+failed)
		fmt.Println()
		fmt.Println("  Failure stage diagnostic key:")
		fmt.Println("    Stage 1-3: Infrastructure/deployment issue")
		fmt.Println("    Stage 4:   API/registration issue")
		fmt.Println("    Stage 5:   Publication/dissemination failure (localpub.Publisher)")
		fmt.Println("    Stage 6:   Consensus/vote dissemination failure (autovalidator → broadcast)")
		fmt.Println("    Stage 7:   Settlement creation/application failure (SetFinalizationHandler)")
		fmt.Println("    Stage 8:   Cross-node convergence failure (Settlement event broadcast)")
	}
	fmt.Println()

	if failed > 0 {
		os.Exit(1)
	}
}
