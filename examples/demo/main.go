// Command demo is a self-contained end-to-end demonstration of the AetherNet
// protocol. It starts an in-memory node, wires up the HTTP API server, and
// exercises every API endpoint via the Go SDK — no running node or database
// required.
//
// Run with:
//
//	go run ./examples/demo
package main

import (
	"fmt"
	"log"
	"net/http/httptest"

	"github.com/aethernet/core/internal/api"
	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/ocs"
	"github.com/aethernet/core/pkg/sdk"
)

func main() {
	// -----------------------------------------------------------------------
	// 1. Build an in-memory node stack (no TCP listener, no BadgerDB).
	// -----------------------------------------------------------------------
	kp, err := crypto.GenerateKeyPair()
	must(err, "generate keypair")

	d := dag.New()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	must(eng.Start(), "start OCS engine")
	defer eng.Stop()
	sm := ledger.NewSupplyManager(tl, gl)

	// -----------------------------------------------------------------------
	// 2. Start the HTTP API server on an httptest listener.
	//    The :0 listenAddr is never used — httptest.NewServer provides its own.
	// -----------------------------------------------------------------------
	srv := api.NewServer(":0", d, tl, gl, reg, eng, sm, nil, kp)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	agentID := kp.AgentID()
	fmt.Printf("AetherNet demo node\nAgentID : %s\nAPI     : %s\n\n", agentID, ts.URL)

	// -----------------------------------------------------------------------
	// 3. Pre-fund the agent via the ledger so it can submit Transfer events.
	//    (There is no HTTP /fund endpoint; funding is an internal operation.)
	// -----------------------------------------------------------------------
	must(tl.FundAgent(agentID, 1_000_000), "fund agent")
	fmt.Println("[setup] Funded agent with 1,000,000 micro-AET")

	// -----------------------------------------------------------------------
	// 4. Use the SDK to interact with the node over HTTP.
	// -----------------------------------------------------------------------
	client := sdk.New(ts.URL, nil)

	// --- Register ---
	registeredID, err := client.Register([]sdk.Capability{
		{Domain: "nlp.summarization", Confidence: 7500, EvidenceCount: 0},
	})
	must(err, "register agent")
	fmt.Printf("\n[1] Registered agent: %s\n", registeredID)

	// --- Balance ---
	bal, err := client.Balance(registeredID)
	must(err, "balance")
	fmt.Printf("[2] Balance: %d %s\n", bal.Balance, bal.Currency)

	// --- Generation event (AI work claim) ---
	genID, err := client.Generate(sdk.GenerationRequest{
		ClaimedValue:    5000,
		EvidenceHash:    "sha256:4e07408562bedb8b60ce05c1deceb1d8d4b72e7c",
		TaskDescription: "Summarised a 10-page research paper",
		StakeAmount:     1000,
	})
	must(err, "generate event")
	fmt.Printf("[3] Generation event : %s\n", genID)

	// --- Transfer ---
	txID, err := client.Transfer(sdk.TransferRequest{
		ToAgent:     registeredID, // self-transfer for the demo
		Amount:      250,
		Currency:    "AET",
		Memo:        "demo payment",
		StakeAmount: 1000,
	})
	must(err, "transfer")
	fmt.Printf("[4] Transfer event   : %s\n", txID)

	// --- DAG tips ---
	tips, err := client.Tips()
	must(err, "dag tips")
	fmt.Printf("[5] DAG tips (%d): %v\n", len(tips.Tips), tips.Tips)

	// --- Fetch generation event ---
	ev, err := client.GetEvent(genID)
	must(err, "get event")
	fmt.Printf("[6] Event type=%s  settlement=%s  timestamp=%d\n",
		ev.Type, ev.SettlementState, ev.CausalTimestamp)

	// --- Agent profile ---
	profile, err := client.Profile(registeredID)
	must(err, "profile")
	fmt.Printf("[7] Agent profile: reputation=%d  trust_limit=%d\n",
		profile.ReputationScore, profile.OptimisticTrustLimit)

	// --- Node status ---
	status, err := client.Status()
	must(err, "status")
	fmt.Printf("[8] Node status: peers=%d  dag=%d  ocs_pending=%d  supply=%.4fx\n",
		status.Peers, status.DAGSize, status.OCSPending, status.SupplyRatio)

	fmt.Println("\nDemo complete!")
}

func must(err error, context string) {
	if err != nil {
		log.Fatalf("%s: %v", context, err)
	}
}
