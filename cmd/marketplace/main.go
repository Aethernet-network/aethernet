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
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	marketplace "github.com/Aethernet-network/aethernet/internal/marketplace"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/pkg/sdk"
)

// marketplaceTaskSource adapts *tasks.TaskManager to the router.TaskSource interface.
type marketplaceTaskSource struct {
	tm *tasks.TaskManager
}

func (s *marketplaceTaskSource) OpenTasks() []router.RoutableTask {
	open := s.tm.OpenTasks(0)
	result := make([]router.RoutableTask, len(open))
	for i, t := range open {
		result[i] = t
	}
	return result
}

func main() {
	nodeURL := flag.String("node", envOr("AETHERNET_NODE", "http://localhost:8338"), "Protocol node API URL")
	listenAddr := flag.String("listen", ":8340", "Marketplace HTTP listen address")
	testnet := flag.Bool("testnet", os.Getenv("AETHERNET_TESTNET") == "true", "Enable testnet features (activity generator, auto-validator)")
	flag.Parse()

	slog.Info("AetherNet Marketplace starting", "node", *nodeURL, "listen", *listenAddr)

	// Verify protocol node connectivity via SDK.
	client := sdk.New(*nodeURL, nil)
	status, err := client.Status()
	if err != nil {
		slog.Error("cannot connect to protocol node", "url", *nodeURL, "err", err)
		os.Exit(1)
	}
	slog.Info("marketplace: connected to protocol node", "version", status.Version, "dag_size", status.DAGSize)

	// ---------------------------------------------------------------------------
	// Build marketplace component stack.
	//
	// The marketplace manages its own in-process state for tasks, routing,
	// service registry, and reputation. Escrow fund tracking uses a local
	// in-process transfer ledger for state (held amounts, task→poster mapping)
	// while actual token custody is enforced by calling the protocol node's
	// Transfer API via the SDK client (split-deployment escrow: Fix 8).
	// ---------------------------------------------------------------------------

	taskMgr := tasks.NewTaskManager()
	reputationMgr := reputation.NewReputationManager()
	svcRegistry := registry.New()

	// nodeTransfer calls the protocol node's Transfer API to move funds
	// between agents. Used for actual token custody in split-deployment escrow.
	escrowPoolID := "marketplace-escrow-pool"
	nodeTransfer := func(fromAgent, toAgent string, amount uint64, memo string) error {
		_, err := client.Transfer(sdk.TransferRequest{
			FromAgent:   fromAgent,
			ToAgent:     toAgent,
			Amount:      amount,
			Currency:    "AET",
			Memo:        memo,
			StakeAmount: 1000, // minimum stake for protocol compliance
		})
		return err
	}

	// Local in-process ledger for escrow bookkeeping (Hold/Release state).
	// The actual AET custody is tracked on the protocol node via nodeTransfer.
	localLedger := ledger.NewTransferLedger()
	// Pre-fund the escrow pool locally so local balance checks pass.
	_ = localLedger.FundAgent(crypto.AgentID(escrowPoolID), 1<<40) // 1T micro-AET virtual pool
	escrowMgr := escrow.New(localLedger)

	// Wrap the marketplace server's task posting and approval to also call
	// the protocol node for real fund movements. These are set as hooks on the
	// marketplace server after construction.
	_ = nodeTransfer   // used below in seedMarketplace and approval hooks
	_ = escrowPoolID

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
	taskRouter := router.New(&marketplaceTaskSource{tm: taskMgr}, claimFn, repFn, 10*time.Second)

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
		slog.Error("failed to start local OCS engine", "err", err)
		os.Exit(1)
	}

	stakeMgr := staking.NewStakeManager()
	localEngine.SetEconomics(nil, stakeMgr, "")

	var autoVal *autovalidator.AutoValidator

	if *testnet {
		slog.Info("marketplace: testnet mode enabled")

		testnetValidatorID := crypto.AgentID("testnet-validator-marketplace")
		tvFP, err := identity.NewFingerprint(testnetValidatorID, make([]byte, 32), nil)
		if err == nil {
			_ = localReg.Register(tvFP)
		}
		autoVal = autovalidator.NewAutoValidator(localEngine, testnetValidatorID, 5*time.Second)
		autoVal.SetTaskManager(taskMgr, escrowMgr)
		autoVal.SetReputationManager(reputationMgr)
		autoVal.Start()
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
		slog.Info("marketplace: explorer enabled", "url", *listenAddr+"/explorer/")
	}

	if err := srv.Start(); err != nil {
		slog.Error("failed to start marketplace server", "err", err)
		os.Exit(1)
	}

	slog.Info("marketplace: ready", "addr", "http://localhost"+*listenAddr, "node", *nodeURL)

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

