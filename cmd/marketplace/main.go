// Package main is the AetherNet marketplace binary.
//
// The marketplace is a separate application layer that sits above the protocol
// node. It manages task lifecycle, escrow, autonomous routing, service
// discovery, and the web explorer. It connects to the protocol node via the
// Go SDK to verify connectivity and to read protocol state (agent balances,
// reputation, settlement status).
//
// This binary is the reference implementation of "an application built on
// AetherNet". Third-party developers use the same SDK and the same public API
// to build their own applications without touching any internal protocol code.
//
// Usage:
//
//	marketplace [flags]
//
// Flags:
//
//	-node   string  Protocol node API URL (default "http://localhost:8338")
//	-listen string  Marketplace HTTP listen address (default ":8340")
//	-testnet        Enable testnet features (activity generator)
//
// Environment variables:
//
//	AETHERNET_NODE    Protocol node URL (overrides -node)
//	AETHERNET_TESTNET Set to "true" to enable testnet activity generator
package main

import (
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/demo"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	marketplace "github.com/Aethernet-network/aethernet/internal/marketplace"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/validator"
	"github.com/Aethernet-network/aethernet/pkg/sdk"
)

func main() {
	nodeURL := flag.String("node", envOr("AETHERNET_NODE", "http://localhost:8338"), "Protocol node API URL")
	listenAddr := flag.String("listen", ":8340", "Marketplace HTTP listen address")
	testnet := flag.Bool("testnet", os.Getenv("AETHERNET_TESTNET") == "true", "Enable testnet features (activity generator, auto-validator)")
	flag.Parse()

	log.Printf("AetherNet Marketplace")
	log.Printf("  Protocol node : %s", *nodeURL)
	log.Printf("  Marketplace   : %s", *listenAddr)

	// Verify protocol node connectivity via SDK.
	client := sdk.New(*nodeURL, nil)
	status, err := client.Status()
	if err != nil {
		log.Fatalf("Cannot connect to protocol node at %s: %v\n"+
			"Start the protocol node first with: aethernet start", *nodeURL, err)
	}
	log.Printf("  Connected     : protocol node v%s (DAG size: %d)", status.Version, status.DAGSize)

	// ---------------------------------------------------------------------------
	// Build marketplace component stack.
	//
	// The marketplace manages its own in-process state for tasks, routing,
	// service registry, and reputation. Escrow fund tracking uses a local
	// in-process transfer ledger; actual token custody is enforced by the
	// protocol node.
	//
	// In a production split deployment, escrow Hold/Release would call the
	// protocol node's transfer API to move funds atomically. That integration
	// is tracked as a future enhancement once the protocol supports dedicated
	// escrow accounts.
	// ---------------------------------------------------------------------------

	taskMgr := tasks.NewTaskManager()
	reputationMgr := reputation.NewReputationManager()
	svcRegistry := registry.New()

	// Local in-process ledger for escrow bookkeeping (see comment above).
	localLedger := ledger.NewTransferLedger()
	escrowMgr := escrow.New(localLedger)

	// Discovery engine: ranks registered agents by relevance + reputation.
	discoveryEng := discovery.NewEngine(svcRegistry, reputationMgr)

	// Autonomous task router: matches open tasks to best available agent.
	claimFn := func(taskID string, agentID crypto.AgentID) error {
		return taskMgr.ClaimTask(taskID, agentID)
	}
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		rep := reputationMgr.GetReputation(agentID)
		cat, ok := rep.Categories[category]
		if !ok || cat == nil {
			return 0, 0, 0, 0
		}
		return cat.TasksCompleted, cat.AvgScore, cat.AvgDeliveryTime, cat.CompletionRate()
	}
	taskRouter := router.New(taskMgr, claimFn, repFn, 10*time.Second)

	// Register seed agent capabilities for the testnet router.
	seedRouterCapabilities(taskRouter)
	taskRouter.Start()

	// ---------------------------------------------------------------------------
	// Start auto-validator and activity generator (testnet only).
	//
	// Auto-validator: auto-settles submitted tasks using the OCS engine built
	// on top of the local ledger stack. In split-deployment, settlement should
	// go through the protocol node; a future version will use client.Verify().
	//
	// Activity generator: fires synthetic transfers via the protocol node's
	// Transfer API, keeping the explorer feed active on the testnet.
	// ---------------------------------------------------------------------------

	// Build a minimal local protocol stack for the auto-validator.
	localGL := ledger.NewGenerationLedger()
	localReg := identity.NewRegistry()
	localEngine := ocs.NewEngine(ocs.DefaultConfig(), localLedger, localGL, localReg)
	if err := localEngine.Start(); err != nil {
		log.Fatalf("Failed to start local OCS engine: %v", err)
	}

	stakeMgr := staking.NewStakeManager()
	localEngine.SetEconomics(nil, stakeMgr, "")

	// Generate a temporary keypair for the local engine (not used for signing real events).
	localKP, _ := crypto.GenerateKeyPair()
	localAgentID := localKP.AgentID()

	var autoVal *validator.AutoValidator
	var actGen *demo.ActivityGenerator

	if *testnet {
		log.Printf("  Testnet mode  : enabled (auto-validator + activity generator)")

		testnetValidatorID := crypto.AgentID("testnet-validator-marketplace")
		tvFP, err := identity.NewFingerprint(testnetValidatorID, make([]byte, 32), nil)
		if err == nil {
			_ = localReg.Register(tvFP)
		}
		autoVal = validator.NewAutoValidator(localEngine, testnetValidatorID, 5*time.Second)
		autoVal.SetTaskManager(taskMgr, escrowMgr)
		autoVal.SetReputationManager(reputationMgr)
		autoVal.Start()

		// Activity generator uses the protocol node's Transfer API via an event
		// construction path that mirrors what the protocol node does internally.
		activityAgents := []string{"alpha-researcher", "data-scientist", "code-auditor", "doc-writer"}
		transferFn := func(from, to string, amount uint64, memo string) error {
			// Fund activity agents locally so the local ledger balance checks pass.
			_ = localLedger.FundAgent(crypto.AgentID(from), amount)
			e, err := event.New(
				event.EventTypeTransfer,
				nil,
				event.TransferPayload{
					FromAgent: from,
					ToAgent:   to,
					Amount:    amount,
					Currency:  "AET",
					Memo:      memo,
				},
				string(localAgentID),
				nil,
				localEngine.MinEventStake(),
			)
			if err != nil {
				return err
			}
			if signErr := crypto.SignEvent(e, localKP); signErr != nil {
				return signErr
			}
			return localEngine.Submit(e)
		}
		actGen = demo.NewActivityGenerator(transferFn, activityAgents, 30*time.Second)
		actGen.Start()
	}

	// ---------------------------------------------------------------------------
	// Build and start the marketplace HTTP server.
	// ---------------------------------------------------------------------------

	explorerDir := resolveExplorerDir()

	srv := marketplace.New(*listenAddr)
	srv.SetTaskManager(taskMgr, escrowMgr)
	srv.SetServiceRegistry(svcRegistry)
	srv.SetReputationManager(reputationMgr)
	srv.SetDiscoveryEngine(discoveryEng)
	srv.SetTaskRouter(taskRouter)
	if explorerDir != "" {
		srv.SetExplorerDir(explorerDir)
		log.Printf("  Explorer      : %s/explorer/", *listenAddr)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start marketplace server: %v", err)
	}

	log.Printf("Marketplace ready at http://localhost%s", *listenAddr)
	log.Printf("Protocol node API at %s", *nodeURL)

	// ---------------------------------------------------------------------------
	// Wait for shutdown signal.
	// ---------------------------------------------------------------------------

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("marketplace: shutdown signal received")

	if taskRouter != nil {
		taskRouter.Stop()
	}
	if actGen != nil {
		actGen.Stop()
	}
	if autoVal != nil {
		autoVal.Stop()
	}
	localEngine.Stop()
	srv.Stop()

	slog.Info("marketplace: stopped cleanly")
}

// envOr returns the value of the named environment variable, or defaultVal
// when the variable is unset or empty.
func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// resolveExplorerDir returns the first existing path from a set of
// well-known locations for the web explorer assets.
func resolveExplorerDir() string {
	candidates := []string{
		"explorer",                                    // development (CWD)
		"/usr/local/share/aethernet/explorer",         // Docker install path
		filepath.Join(os.Getenv("AETHERNET_DATA"), "explorer"), // data dir
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}

// seedRouterCapabilities registers the four testnet seed agents in the
// autonomous task router so they can receive auto-routed tasks from startup.
func seedRouterCapabilities(r *router.Router) {
	seeds := []router.AgentCapability{
		{
			AgentID:       "alpha-researcher",
			Categories:    []string{"research", "writing"},
			Tags:          []string{"papers", "nlp", "summarisation", "translation"},
			Description:   "Research and writing specialist — arxiv papers, summaries, translations",
			PricePerTask:  20_000,
			MaxConcurrent: 3,
			Available:     true,
		},
		{
			AgentID:       "data-scientist",
			Categories:    []string{"data", "ml"},
			Tags:          []string{"csv", "classification", "sql", "analytics", "sentiment"},
			Description:   "Data science and ML workloads — classification, analytics, SQL generation",
			PricePerTask:  30_000,
			MaxConcurrent: 2,
			Available:     true,
		},
		{
			AgentID:       "code-auditor",
			Categories:    []string{"code", "security"},
			Tags:          []string{"solidity", "audit", "reentrancy", "smart-contracts"},
			Description:   "Code review and security auditing — Solidity, reentrancy, access control",
			PricePerTask:  50_000,
			MaxConcurrent: 2,
			Available:     true,
		},
		{
			AgentID:       "doc-writer",
			Categories:    []string{"writing", "documentation"},
			Tags:          []string{"openapi", "yaml", "docs", "technical-writing"},
			Description:   "Technical documentation — OpenAPI specs, quickstarts, API docs",
			PricePerTask:  15_000,
			MaxConcurrent: 4,
			Available:     true,
		},
	}
	for _, s := range seeds {
		r.RegisterCapability(s)
	}
}
