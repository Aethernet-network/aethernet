// Package main is the AetherNet node binary. It wires together every internal
// package — DAG, dual ledger, supply, identity, OCS engine, and p2p network —
// into a single runnable process.
//
// Subcommands:
//
//	aethernet init                      generate a new node identity
//	aethernet genesis                   seed initial token supply into the store
//	aethernet start [flags]             start the node
//	aethernet connect --peer <address>  start and dial a specific peer
//	aethernet status                    print identity and config, no networking
//
// Environment variables (all optional):
//
//	AETHERNET_DATA    base directory for key file and BadgerDB store (default: ".")
//	AETHERNET_LISTEN  p2p TCP listen address  (default: "0.0.0.0:8337")
//	AETHERNET_API     REST API listen address (default: ":8338")
//	AETHERNET_PEER    peer to auto-connect on startup (default: "")
//	AETHERNET_RESET   set to "true" to wipe the database on startup (testnet recovery)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/demo"
	"github.com/Aethernet-network/aethernet/internal/discovery"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/metrics"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	platformpkg "github.com/Aethernet-network/aethernet/internal/platform"
	"github.com/Aethernet-network/aethernet/internal/ratelimit"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/store"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/wallet"
)

// VERSION is the protocol and build version broadcast during handshake.
const VERSION = "0.1.0-testnet"

// envOr returns the value of the named environment variable, or defaultVal when
// the variable is unset or empty.
func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// dataDir returns the base directory for all node data files.
// When AETHERNET_DATA is set (e.g. in Docker), files are stored there.
// Otherwise "." (the current working directory) is used for backward compatibility.
func dataDir() string {
	return envOr("AETHERNET_DATA", ".")
}

// keyFilePath returns the path to the encrypted Ed25519 identity file.
func keyFilePath() string {
	return filepath.Join(dataDir(), "node_keys", "identity.json")
}

// storePath returns the path to the BadgerDB data store.
// The store lives directly inside the data directory, not in a "data"
// subdirectory — that would produce a double-nested path when AETHERNET_DATA
// is already set to something like "/data".
func storePath() string {
	return filepath.Join(dataDir(), "aethernet.db")
}

// wipePath removes all files inside dir and recreates it as an empty directory.
// It is used for database recovery and AETHERNET_RESET.
func wipePath(dir string) error {
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

// openStoreWithRecovery opens the BadgerDB store at path, handling two
// recovery scenarios:
//
//  1. AETHERNET_RESET=true — wipe the directory unconditionally before opening
//     (testnet operator recovery via environment variable).
//  2. Open failure (e.g. corrupt SST file) — log the error, wipe the directory,
//     and retry once.  If the retry also fails the process exits.
func openStoreWithRecovery(path string) *store.Store {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Error("failed to create store parent directory", "err", err)
		os.Exit(1)
	}

	if os.Getenv("AETHERNET_RESET") == "true" {
		slog.Warn("AETHERNET_RESET=true: wiping database before open", "path", path)
		if err := wipePath(path); err != nil {
			slog.Error("AETHERNET_RESET: failed to wipe database", "path", path, "err", err)
			os.Exit(1)
		}
	}

	s, err := store.NewStore(path)
	if err == nil {
		return s
	}

	// First open failed — attempt self-healing recovery.
	slog.Error("store open failed, attempting recovery by wiping database",
		"path", path, "err", err)
	if wipeErr := wipePath(path); wipeErr != nil {
		slog.Error("recovery: failed to wipe database directory", "path", path, "err", wipeErr)
		os.Exit(1)
	}
	slog.Warn("database wiped; retrying open with fresh store")
	s, err = store.NewStore(path)
	if err != nil {
		slog.Error("store open failed after recovery — cannot start", "path", path, "err", err)
		os.Exit(1)
	}
	slog.Info("database recovered successfully; node starting with empty store")
	return s
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
	case "genesis":
		cmdGenesis()
	case "start":
		cmdStart()
	case "connect":
		cmdConnect()
	case "status":
		cmdStatus()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "AetherNet node %s\n\nUsage:\n", VERSION)
	fmt.Fprintf(os.Stderr, "  aethernet init                                  generate a new node identity\n")
	fmt.Fprintf(os.Stderr, "  aethernet genesis                               seed genesis token supply\n")
	fmt.Fprintf(os.Stderr, "  aethernet start [--listen addr] [--api addr] [--peer addr] [--marketplace]\n")
	fmt.Fprintf(os.Stderr, "                                                  start the node\n")
	fmt.Fprintf(os.Stderr, "  aethernet connect --peer <address>              start and connect to a peer\n")
	fmt.Fprintf(os.Stderr, "  aethernet status                                print node identity and config\n")
	fmt.Fprintf(os.Stderr, "\nFlags for 'start':\n")
	fmt.Fprintf(os.Stderr, "  --marketplace   Enable built-in marketplace (task routing, escrow, explorer)\n")
	fmt.Fprintf(os.Stderr, "                  For split deployments, use the separate 'marketplace' binary\n")
	fmt.Fprintf(os.Stderr, "                  instead and point it at the protocol node with --node.\n")
	fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_DATA    data directory (default: current directory)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_LISTEN  p2p listen address (default: 0.0.0.0:8337)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_API     API listen address (default: :8338)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_PEER    peer to connect on startup\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_RESET   set to \"true\" to wipe the database on startup\n")
}

// readPassphrase prints prompt and reads one line from stdin, stripping the
// trailing newline.
func readPassphrase(prompt string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(prompt)
	line, _ := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// loadKeyPair loads the node keypair, choosing the right strategy based on context:
//   - Docker / non-interactive (AETHERNET_DATA is set): if no key file exists yet,
//     auto-generate one with an empty passphrase. If it exists, load with empty
//     passphrase (Docker-generated keys always use an empty passphrase).
//   - Interactive (AETHERNET_DATA not set): prompt for a passphrase as before.
func loadKeyPair() *crypto.KeyPair {
	path := keyFilePath()

	if os.Getenv("AETHERNET_DATA") == "" {
		// Interactive mode — original passphrase-prompt flow.
		return loadKeyPairInteractive(path)
	}

	// Docker / non-interactive mode.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return autoInitKeyPair(path)
	}
	kp, err := crypto.LoadKeyPair(path, "")
	if err != nil {
		// Key file exists but empty passphrase doesn't work (manually copied key?).
		slog.Warn("empty passphrase failed, falling back to interactive prompt", "path", path)
		return loadKeyPairInteractive(path)
	}
	return kp
}

// loadKeyPairInteractive prompts for a passphrase and loads the keypair from path.
func loadKeyPairInteractive(path string) *crypto.KeyPair {
	passphrase := readPassphrase("Passphrase: ")
	kp, err := crypto.LoadKeyPair(path, passphrase)
	if err != nil {
		slog.Error("failed to load keypair", "path", path, "err", err)
		os.Exit(1)
	}
	return kp
}

// autoInitKeyPair generates a new Ed25519 keypair, saves it with an empty
// passphrase (suitable for non-interactive Docker startup), and returns it.
func autoInitKeyPair(path string) *crypto.KeyPair {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Error("failed to create key directory", "err", err)
		os.Exit(1)
	}
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		slog.Error("failed to generate keypair", "err", err)
		os.Exit(1)
	}
	if err := kp.Save(path, ""); err != nil {
		slog.Error("failed to save keypair", "path", path, "err", err)
		os.Exit(1)
	}
	agentID := kp.AgentID()
	fmt.Printf("Auto-generated identity.\nAgentID : %s\nKey file: %s\n", agentID, path)
	slog.Info("auto-generated node identity", "agent_id", agentID)
	return kp
}

// ---------------------------------------------------------------------------
// nodeStack — the assembled set of core components
// ---------------------------------------------------------------------------

// nodeStack bundles the runtime components so they can be passed around and
// shut down together.
type nodeStack struct {
	dag          *dag.DAG
	transfer     *ledger.TransferLedger
	generation   *ledger.GenerationLedger
	supply       *ledger.SupplyManager
	reg          *identity.Registry
	engine       *ocs.Engine
	store        *store.Store
	kp           *crypto.KeyPair
	apiSrv       *api.Server
	svcRegistry  *registry.Registry
	bus          *eventbus.Bus
	stakeManager *staking.StakeManager
	feeCollector *fees.Collector
	walletMgr    *wallet.Wallet
	metricsReg   *metrics.Registry
	nodeMetrics  *metrics.AetherNetMetrics
	metricsStop  chan struct{} // closed to terminate the gauge-update goroutine
	autoVal         *autovalidator.AutoValidator
	taskMgr         *tasks.TaskManager
	escrowMgr       *escrow.Escrow
	reputationMgr   *reputation.ReputationManager
	discoveryEngine *discovery.Engine
	activityGen     *demo.ActivityGenerator
	platformKeys    *platformpkg.KeyManager
	taskRouter      *router.Router
}

// taskManagerSource adapts *tasks.TaskManager to the router.TaskSource interface,
// converting []*tasks.Task slices into []router.RoutableTask without importing
// the tasks package from the router (which would create an import cycle).
type taskManagerSource struct {
	tm *tasks.TaskManager
}

func (s *taskManagerSource) OpenTasks() []router.RoutableTask {
	open := s.tm.OpenTasks(0)
	result := make([]router.RoutableTask, len(open))
	for i, t := range open {
		result[i] = t
	}
	return result
}

// buildStack wires all internal packages together and returns a ready-to-start
// nodeStack. When s is non-nil, state is restored from the store.
func buildStack(s *store.Store, kp *crypto.KeyPair) *nodeStack {
	var (
		d   *dag.DAG
		tl  *ledger.TransferLedger
		gl  *ledger.GenerationLedger
		reg *identity.Registry
		err error
	)

	if s != nil {
		d, err = dag.LoadFromStore(s)
		if err != nil {
			slog.Error("failed to load DAG from store", "err", err)
			os.Exit(1)
		}
		tl, err = ledger.LoadTransferLedgerFromStore(s)
		if err != nil {
			slog.Error("failed to load transfer ledger", "err", err)
			os.Exit(1)
		}
		gl, err = ledger.LoadGenerationLedgerFromStore(s)
		if err != nil {
			slog.Error("failed to load generation ledger", "err", err)
			os.Exit(1)
		}
		reg, err = identity.LoadRegistryFromStore(s)
		if err != nil {
			slog.Error("failed to load identity registry", "err", err)
			os.Exit(1)
		}
		slog.Info("restored state from store",
			"events", d.Size(),
			"identities", len(reg.All(0, 0)),
		)
	} else {
		d = dag.New()
		tl = ledger.NewTransferLedger()
		gl = ledger.NewGenerationLedger()
		reg = identity.NewRegistry()
	}

	sm := ledger.NewSupplyManager(tl, gl)
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if s != nil {
		eng.SetStore(s)
		if err := eng.LoadPendingFromStore(s); err != nil {
			slog.Error("failed to load pending items", "err", err)
			os.Exit(1)
		}
	}

	// Consensus: reputation-weighted BFT voting for multi-node agreement.
	// MinParticipants=1 preserves single-node semantics: one validator with any
	// positive weight reaches supermajority immediately, identical to the
	// previous direct-settlement behaviour. Peer nodes raise effective
	// participation counts automatically as they join and cast votes.
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           30 * time.Second,
		MinParticipants:        1,
	}
	votingRound := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(votingRound)

	// Economics: staking, fee collection, and deposit-address wallet.
	// These are optional — engine and API server nil-check them — but the
	// node should always wire them so that trust limits, fee distribution,
	// and the /stake endpoints work correctly.
	stakeMgr := staking.NewStakeManager()
	if s != nil {
		stakeMgr.SetStore(s)
		if err := stakeMgr.LoadFromStore(s); err != nil {
			// Non-fatal: node can still run, stake timestamps just reset.
			slog.Warn("failed to restore stake metadata from store", "err", err)
		}
	}
	// Wire the transfer ledger so that Stake/Unstake debit the agent's balance,
	// preventing over-staking beyond available funds (Fix 12).
	stakeMgr.SetTransferLedger(tl)
	feeCollector := fees.NewCollector(tl)
	if s != nil {
		// Persist fee stats so total_collected survives node restarts.
		feeCollector.SetStore(s)
	}
	walletMgr := wallet.New()
	treasuryID := crypto.AgentID(genesis.BucketTreasury)
	eng.SetEconomics(feeCollector, stakeMgr, treasuryID)

	svcReg := registry.New()
	if s != nil {
		svcReg.SetStore(s)
		if err := svcReg.LoadFromStore(); err != nil {
			slog.Error("failed to load service registry", "err", err)
			os.Exit(1)
		}
	}

	// Task marketplace: task manager + escrow.
	taskMgr := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)
	if s != nil {
		taskMgr.SetStore(s)
		if err := taskMgr.LoadFromStore(s); err != nil {
			slog.Warn("failed to restore task marketplace from store", "err", err)
		}
	}

	// Category-specific reputation tracking.
	reputationMgr := reputation.NewReputationManager()
	if s != nil {
		reputationMgr.SetStore(s)
		if err := reputationMgr.LoadFromStore(); err != nil {
			slog.Warn("failed to restore reputation data from store", "err", err)
		}
	}

	// Capability-aware discovery engine — matches task requirements to agent
	// capabilities using service registry listings and reputation data.
	discoveryEng := discovery.NewEngine(svcReg, reputationMgr)

	// Developer platform API key manager — tracks third-party apps building on AetherNet.
	// Persist keys to the store so they survive restarts.
	platformKeys := platformpkg.NewKeyManager()
	if s != nil {
		platformKeys.SetStore(s)
		if err := platformKeys.LoadFromStore(s); err != nil {
			slog.Warn("failed to restore platform API keys from store", "err", err)
		}
	}

	// Autonomous task router — matches open tasks to the best registered agent.
	// The claimFunc and reputationFunc closures bridge the router to the live
	// task and reputation managers without creating an import cycle.
	// ROUTING: The router marks tasks with RoutedTo (assigns) rather than
	// immediately claiming them. The assigned agent then claims explicitly via
	// the API, benefiting from 60-second priority over unregistered agents.
	claimFn := func(taskID string, agentID crypto.AgentID) error {
		return taskMgr.SetRoutedTo(taskID, string(agentID))
	}
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		rep := reputationMgr.GetReputation(agentID)
		cat, ok := rep.Categories[category]
		if !ok || cat == nil {
			return 0, 0, 0, 0
		}
		return cat.TasksCompleted, cat.AvgScore, cat.AvgDeliveryTime, cat.CompletionRate()
	}
	taskRouter := router.New(&taskManagerSource{tm: taskMgr}, claimFn, repFn, 5*time.Second)

	return &nodeStack{
		dag:          d,
		transfer:     tl,
		generation:   gl,
		supply:       sm,
		reg:          reg,
		engine:       eng,
		store:        s,
		kp:           kp,
		svcRegistry:  svcReg,
		stakeManager: stakeMgr,
		feeCollector: feeCollector,
		walletMgr:    walletMgr,
		taskMgr:         taskMgr,
		escrowMgr:       escrowMgr,
		reputationMgr:   reputationMgr,
		discoveryEngine: discoveryEng,
		platformKeys:    platformKeys,
		taskRouter:      taskRouter,
	}
}

// printStatus writes a single-line status summary every tick.
func printStatus(agentID crypto.AgentID, d *dag.DAG, n *network.Node, eng *ocs.Engine, sm *ledger.SupplyManager, bus *eventbus.Bus) {
	ratio, _ := sm.SupplyRatio()
	id := string(agentID)
	if len(id) > 16 {
		id = id[:16] + "..."
	}
	wsSubs := 0
	if bus != nil {
		wsSubs = bus.SubscriberCount()
	}
	fmt.Printf("[%s]  peers=%-3d  dag=%-6d  ocs_pending=%-4d  supply=%.4fx  ws_subs=%-2d\n",
		id, n.PeerCount(), d.Size(), eng.PendingCount(), ratio, wsSubs)
}

// startStack starts the OCS engine, network node, and HTTP API server.
// p2pAddr and apiListenAddr override the defaults and may come from flags or
// environment variables. enableMarketplace controls whether task marketplace
// components (task routing, auto-settlement, discovery) are started.
func startStack(stack *nodeStack, agentID crypto.AgentID, p2pAddr, apiListenAddr string, enableMarketplace bool) *network.Node {
	// Create the metrics registry and wire it to the OCS engine.
	metricsReg := metrics.NewRegistry()
	nodeMetrics := metrics.NewAetherNetMetrics(metricsReg)
	stack.metricsReg = metricsReg
	stack.nodeMetrics = nodeMetrics
	stack.engine.SetMetrics(nodeMetrics)

	// Create the event bus and wire it to the OCS engine before starting.
	bus := eventbus.New()
	stack.engine.SetEventBus(bus)
	stack.bus = bus

	if err := stack.engine.Start(); err != nil {
		slog.Error("failed to start OCS engine", "err", err)
		os.Exit(1)
	}

	// Auto-validator: on testnet, automatically settle pending OCS transactions.
	// The "testnet-validator" agent is registered in the identity registry so
	// it appears in the explorer as a known participant.
	testnetValidatorID := crypto.AgentID("testnet-validator")
	tvFP, err := identity.NewFingerprint(testnetValidatorID, make([]byte, 32), nil)
	if err == nil {
		// Give the testnet validator non-zero reputation and stake so that its
		// votes carry weight in the consensus round (weight = rep×stake/10000).
		// Without weight the VotingRound supermajority check can never fire.
		tvFP.ReputationScore = 5000  // 50 % reputation
		tvFP.StakedAmount = 10000    // 10 000 micro-AET
		_ = stack.reg.Register(tvFP)
	}
	av := autovalidator.NewAutoValidator(stack.engine, testnetValidatorID, 5*time.Second)
	av.SetFeeCollector(stack.feeCollector, crypto.AgentID(genesis.BucketTreasury))
	av.SetGenerationLedger(stack.generation)
	av.SetRegistry(stack.reg)
	av.SetVerifierRegistry(evidence.NewVerifierRegistry())
	// Task marketplace integration is conditional on --marketplace flag.
	if enableMarketplace {
		av.SetTaskManager(stack.taskMgr, stack.escrowMgr)
		av.SetReputationManager(stack.reputationMgr)
	}
	av.Start()
	stack.autoVal = av

	// Seed the task marketplace on first run only when marketplace is enabled.
	// Only runs when TotalTasks == 0 to avoid duplicating tasks across restarts.
	if enableMarketplace && stack.taskMgr.Stats().TotalTasks == 0 {
		seedMarketplace(stack.transfer, stack.reg, stack.taskMgr, stack.escrowMgr, stack.stakeManager)
	}

	cfg := network.DefaultNodeConfig(agentID)
	cfg.ListenAddr = p2pAddr
	node := network.NewNode(cfg, stack.dag)
	if err := node.Start(); err != nil {
		slog.Error("failed to start network listener", "addr", p2pAddr, "err", err)
		stack.engine.Stop()
		os.Exit(1)
	}

	// Wire consensus ↔ P2P: locally-originated votes are broadcast to peers;
	// votes received from peers are fed into the local consensus round.
	stack.engine.SetVoteBroadcaster(func(eventID event.EventID, verdict bool, voterID crypto.AgentID) {
		_ = node.BroadcastVote(eventID, verdict)
	})
	node.SetVoteHandler(func(voterID crypto.AgentID, eventID event.EventID, verdict bool) {
		_ = stack.engine.AcceptPeerVote(eventID, voterID, verdict)
	})

	apiSrv := api.NewServer(
		apiListenAddr,
		stack.dag, stack.transfer, stack.generation,
		stack.reg, stack.engine, stack.supply,
		node, stack.kp,
	)
	if stack.store != nil {
		// Persist onboarding counter so the declining-curve survives restarts.
		apiSrv.SetStore(stack.store)
	}
	if stack.svcRegistry != nil {
		apiSrv.SetServiceRegistry(stack.svcRegistry)
	}
	// Marketplace endpoints are only wired when --marketplace is active.
	if enableMarketplace {
		apiSrv.SetTaskManager(stack.taskMgr, stack.escrowMgr)
		apiSrv.SetReputationManager(stack.reputationMgr)
		if stack.discoveryEngine != nil {
			apiSrv.SetDiscoveryEngine(stack.discoveryEngine)
		}
		if stack.taskRouter != nil {
			seedRouterCapabilities(stack.taskRouter)
			stack.taskRouter.Start()
			apiSrv.SetTaskRouter(stack.taskRouter)
		}
	}
	apiSrv.SetEconomics(stack.walletMgr, stack.stakeManager, stack.feeCollector)
	apiSrv.SetEventBus(bus)
	if stack.platformKeys != nil {
		apiSrv.SetPlatformKeys(stack.platformKeys)
	}
	apiSrv.SetRateLimiters(
		ratelimit.New(ratelimit.DefaultConfig()),
		ratelimit.New(ratelimit.ReadOnlyConfig()),
	)
	// Sybil resistance: limit registrations to 5 per hour per IP.
	// Rate = 5/3600 tokens/second ≈ 0.00139; burst = 5 allows a small burst
	// for legitimate simultaneous registrations (e.g. onboarding a small team).
	apiSrv.SetRegistrationLimiter(ratelimit.New(ratelimit.Config{
		Rate:       float64(5) / 3600, // 5 registrations per hour
		Burst:      5,
		CleanupAge: 2 * time.Hour,
	}))
	apiSrv.SetMetrics(metricsReg, nodeMetrics)
	// Configure which route groups are active. L1 is always on; L2 network
	// coordination is always on; L3 marketplace routes follow --marketplace.
	apiSrv.SetLayerConfig(true, enableMarketplace)
	if err := apiSrv.Start(); err != nil {
		slog.Error("failed to start API server", "addr", apiListenAddr, "err", err)
		node.Stop()
		stack.engine.Stop()
		os.Exit(1)
	}
	stack.apiSrv = apiSrv

	// Periodic gauge updater — refreshes DAG size, tip count, peer count, and
	// uptime every 10 seconds. Stops when metricsStop is closed.
	stop := make(chan struct{})
	stack.metricsStop = stop
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		nodeStart := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				nodeMetrics.DAGSize.Set(int64(stack.dag.Size()))
				nodeMetrics.DAGTips.Set(int64(len(stack.dag.Tips())))
				nodeMetrics.PeerCount.Set(int64(node.PeerCount()))
				nodeMetrics.UptimeSeconds.Set(int64(time.Since(nodeStart).Seconds()))
			}
		}
	}()

	// Background activity generator — simulates transfers between seed agents
	// every 30 s so the explorer's activity feed stays live on the testnet.
	// Gated behind AETHERNET_TESTNET=true to prevent spurious activity on
	// mainnet or production nodes that don't need synthetic traffic.
	if os.Getenv("AETHERNET_TESTNET") == "true" {
		activityAgents := []string{"alpha-researcher", "data-scientist", "code-auditor", "doc-writer"}
		transferFn := func(from, to string, amount uint64, memo string) error {
			tips := stack.dag.Tips()
			priorTS := make(map[event.EventID]uint64, len(tips))
			for _, ref := range tips {
				if ev, err := stack.dag.Get(ref); err == nil {
					priorTS[ref] = ev.CausalTimestamp
				}
			}
			e, err := event.New(
				event.EventTypeTransfer,
				tips,
				event.TransferPayload{FromAgent: from, ToAgent: to, Amount: amount, Currency: "AET", Memo: memo},
				string(agentID),
				priorTS,
				stack.engine.MinEventStake(),
			)
			if err != nil {
				return err
			}
			if err := crypto.SignEvent(e, stack.kp); err != nil {
				return err
			}
			if err := stack.engine.Submit(e); err != nil {
				return err
			}
			return stack.dag.Add(e)
		}
		actGen := demo.NewActivityGenerator(transferFn, activityAgents, 30*time.Second)
		actGen.Start()
		stack.activityGen = actGen
		slog.Info("testnet activity generator started")
	}

	return node
}

// stopStack tears down the API server, network node, OCS engine, and persistence
// store in safe reverse-startup order.
func stopStack(node *network.Node, stack *nodeStack) {
	if stack.taskRouter != nil {
		stack.taskRouter.Stop()
	}
	if stack.activityGen != nil {
		stack.activityGen.Stop()
	}
	if stack.metricsStop != nil {
		close(stack.metricsStop)
	}
	if stack.autoVal != nil {
		stack.autoVal.Stop()
	}
	if stack.apiSrv != nil {
		stack.apiSrv.Stop()
	}
	node.Stop()
	stack.engine.Stop()
	if stack.store != nil {
		stack.store.Close()
	}
}

// runLoop prints status every 10 seconds and blocks until SIGINT or SIGTERM.
func runLoop(agentID crypto.AgentID, d *dag.DAG, node *network.Node, eng *ocs.Engine, sm *ledger.SupplyManager, bus *eventbus.Bus) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	printStatus(agentID, d, node, eng, sm, bus)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			slog.Info("shutdown signal received")
			return
		case <-ticker.C:
			printStatus(agentID, d, node, eng, sm, bus)
		}
	}
}

// ---------------------------------------------------------------------------
// Subcommand implementations
// ---------------------------------------------------------------------------

// cmdInit generates a new Ed25519 keypair, saves it encrypted to the key file
// path, and prints the resulting AgentID.
func cmdInit() {
	kfPath := keyFilePath()
	if err := os.MkdirAll(filepath.Dir(kfPath), 0o700); err != nil {
		slog.Error("failed to create key directory", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}

	if _, err := os.Stat(kfPath); err == nil {
		fmt.Fprintf(os.Stderr, "identity already exists at %s\nRemove it to reinitialise.\n", kfPath)
		os.Exit(1)
	}

	passphrase := readPassphrase("Choose a passphrase: ")
	if passphrase == "" {
		fmt.Fprintln(os.Stderr, "error: passphrase must not be empty")
		os.Exit(1)
	}

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		slog.Error("failed to generate keypair", "err", err)
		os.Exit(1)
	}
	if err := kp.Save(kfPath, passphrase); err != nil {
		slog.Error("failed to save keypair", "path", kfPath, "err", err)
		os.Exit(1)
	}

	agentID := kp.AgentID()
	fmt.Printf("Identity created.\nAgentID : %s\nKey file: %s\n", agentID, kfPath)
	slog.Info("node identity initialised", "agent_id", agentID)
}

// cmdStart loads (or auto-generates) the keypair, then starts the full node
// stack and enters the status loop until SIGINT or SIGTERM.
func cmdStart() {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	p2pAddr := fs.String("listen", envOr("AETHERNET_LISTEN", "0.0.0.0:8337"), "TCP address for p2p connections")
	apiListenAddr := fs.String("api", envOr("AETHERNET_API", ":8338"), "TCP address for the REST API")
	peerAddr := fs.String("peer", envOr("AETHERNET_PEER", ""), "peer to auto-connect on startup (host:port)")
	enableMarketplace := fs.Bool("marketplace", false, "Enable built-in marketplace (task routing, escrow, explorer) in the combined single-binary deployment")
	_ = fs.Parse(os.Args[2:])

	// The --marketplace flag controls whether marketplace components (tasks,
	// escrow, router, discovery, activity generator, auto-validator) are wired
	// to the protocol API server. Without it, only protocol endpoints are active.
	// This flag preserves backward compatibility with existing deployments while
	// introducing the separation between the protocol layer and the marketplace
	// application layer. Use cmd/marketplace for the standalone deployment.
	// Pass the flag to startStack so marketplace components are only wired
	// when explicitly requested (protocol-only deployments skip them).
	_ = enableMarketplace // used by startStack below

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s := openStoreWithRecovery(storePath())

	stack := buildStack(s, kp)

	// Auto-genesis: on first Docker start, seed the initial token supply when
	// the founders bucket is empty. Only runs in non-interactive mode
	// (AETHERNET_DATA is set) to preserve the manual genesis workflow in
	// interactive / development environments. Pass the store so seedGenesis
	// writes an idempotency marker preventing double-runs.
	if os.Getenv("AETHERNET_DATA") != "" {
		foundersBalance, _ := stack.transfer.Balance(crypto.AgentID(genesis.BucketFounders))
		if foundersBalance == 0 {
			slog.Info("auto-genesis: seeding initial token supply")
			seedGenesis(stack.transfer, stack.store)
			fmt.Println("Auto-genesis: initial token supply seeded.")
		}
	}

	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr, *enableMarketplace)

	fmt.Printf("AetherNet %s\nAgentID  : %s\nListening: %s\nAPI      : %s\n\n",
		VERSION, agentID, node.ListenAddr(), *apiListenAddr)

	if *peerAddr != "" {
		fmt.Printf("Connecting to %s...\n", *peerAddr)
		peer, err := node.Connect(*peerAddr)
		if err != nil {
			// Non-fatal: log and continue. In Docker, the peer container may not
			// be ready yet; the operator can retry or rely on sync interval.
			slog.Warn("failed to auto-connect to peer", "addr", *peerAddr, "err", err)
			fmt.Printf("Warning: could not connect to %s: %v\n\n", *peerAddr, err)
		} else {
			fmt.Printf("Connected  : %s  (%s)\n\n", peer.AgentID, *peerAddr)
		}
	}

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply, stack.bus)
	stopStack(node, stack)
	slog.Info("node stopped cleanly")
}

// cmdConnect is the legacy subcommand that requires --peer. It is equivalent to
// `aethernet start --peer <address>` and is kept for backward compatibility.
func cmdConnect() {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	peerAddr := fs.String("peer", "", "address of the peer to connect to (host:port)")
	p2pAddr := fs.String("listen", envOr("AETHERNET_LISTEN", "0.0.0.0:8337"), "TCP address for p2p connections")
	apiListenAddr := fs.String("api", envOr("AETHERNET_API", ":8338"), "TCP address for the REST API")
	_ = fs.Parse(os.Args[2:])

	if *peerAddr == "" {
		fmt.Fprintln(os.Stderr, "usage: aethernet connect --peer <host:port>")
		os.Exit(1)
	}

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s := openStoreWithRecovery(storePath())

	stack := buildStack(s, kp)
	// cmdConnect is the legacy subcommand; marketplace is disabled by default.
	// Use 'aethernet start --marketplace' for the combined deployment.
	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr, false)

	fmt.Printf("AetherNet %s\nAgentID  : %s\nListening: %s\nAPI      : %s\n\n",
		VERSION, agentID, node.ListenAddr(), *apiListenAddr)

	fmt.Printf("Connecting to %s...\n", *peerAddr)
	peer, err := node.Connect(*peerAddr)
	if err != nil {
		slog.Error("failed to connect to peer", "addr", *peerAddr, "err", err)
		stopStack(node, stack)
		os.Exit(1)
	}
	fmt.Printf("Connected  : %s  (%s)\n\n", peer.AgentID, *peerAddr)

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply, stack.bus)
	stopStack(node, stack)
	slog.Info("node stopped cleanly")
}

// seedRouterCapabilities registers the four testnet seed agents in the
// autonomous task router so they can receive auto-routed tasks from the moment
// the node starts.
func seedRouterCapabilities(r *router.Router) {
	type seedAgent struct {
		id           string
		categories   []string
		tags         []string
		description  string
		pricePerTask uint64 // micro-AET
		maxConcurrent int
	}
	seeds := []seedAgent{
		{
			id:            "alpha-researcher",
			categories:    []string{"research", "writing"},
			tags:          []string{"papers", "nlp", "summarisation", "translation"},
			description:   "Research and writing specialist — arxiv papers, summaries, translations",
			pricePerTask:  20_000,
			maxConcurrent: 3,
		},
		{
			id:            "data-scientist",
			categories:    []string{"data", "ml"},
			tags:          []string{"csv", "classification", "sql", "analytics", "sentiment"},
			description:   "Data science and ML workloads — classification, analytics, SQL generation",
			pricePerTask:  30_000,
			maxConcurrent: 2,
		},
		{
			id:            "code-auditor",
			categories:    []string{"code", "security"},
			tags:          []string{"solidity", "audit", "reentrancy", "smart-contracts"},
			description:   "Code review and security auditing — Solidity, reentrancy, access control",
			pricePerTask:  50_000,
			maxConcurrent: 2,
		},
		{
			id:            "doc-writer",
			categories:    []string{"writing", "documentation"},
			tags:          []string{"openapi", "yaml", "docs", "technical-writing"},
			description:   "Technical documentation — OpenAPI specs, quickstarts, API docs",
			pricePerTask:  15_000,
			maxConcurrent: 4,
		},
	}
	for _, s := range seeds {
		r.RegisterCapability(router.AgentCapability{
			AgentID:       crypto.AgentID(s.id),
			Categories:    s.categories,
			Tags:          s.tags,
			Description:   s.description,
			PricePerTask:  s.pricePerTask,
			MaxConcurrent: s.maxConcurrent,
			Available:     true,
		})
	}
	slog.Info("seedRouterCapabilities: registered seed agents", "count", len(seeds))
}

// seedMarketplace pre-populates the task marketplace with realistic starter
// tasks on the very first node run (when TotalTasks == 0). It registers four
// poster agent identities, funds each from the ecosystem allocation bucket, and
// creates six tasks with escrow already held. This gives the explorer a live,
// interactive economy from the moment the node starts.
//
// This is intentionally testnet-only: on mainnet, real agents post real tasks.
func seedMarketplace(tl *ledger.TransferLedger, reg *identity.Registry, taskMgr *tasks.TaskManager, escrowMgr *escrow.Escrow, stakeMgr *staking.StakeManager) {
	type poster struct {
		id    string
		funds uint64
		stake uint64
	}
	posters := []poster{
		{"alpha-researcher", 500_000, 100_000},
		{"data-scientist", 800_000, 150_000},
		{"code-auditor", 1_000_000, 200_000},
		{"doc-writer", 400_000, 75_000},
	}

	// Register each poster agent in the identity registry so it shows up in
	// the explorer leaderboard. Use a deterministic zero-filled public key for
	// simplicity — testnet only.
	for _, p := range posters {
		fp, err := identity.NewFingerprint(crypto.AgentID(p.id), make([]byte, 32), nil)
		if err == nil {
			_ = reg.Register(fp)
		}
		if err := tl.FundAgent(crypto.AgentID(p.id), p.funds); err != nil {
			slog.Warn("seedMarketplace: failed to fund poster", "id", p.id, "err", err)
		}
		if stakeMgr != nil {
			stakeMgr.Stake(crypto.AgentID(p.id), p.stake)
		}
	}

	type task struct {
		posterID    string
		title       string
		description string
		budget      uint64
	}
	seedTasks := []task{
		{
			posterID: "alpha-researcher",
			title:    "Summarise top 10 AI research papers from arXiv this week",
			description: "Retrieve and summarise the top 10 most-cited AI/ML papers published on " +
				"arXiv in the last 7 days. Provide a 2-paragraph summary per paper, key findings, " +
				"and a relevance score (1–10) for applied NLP work.",
			budget: 50_000,
		},
		{
			posterID: "data-scientist",
			title:    "Classify 5 000-row customer review dataset",
			description: "Apply multi-label sentiment classification (positive / neutral / negative + " +
				"topic tags) to a CSV of 5 000 customer support messages. Return the augmented CSV " +
				"with added columns: sentiment, confidence, primary_topic.",
			budget: 75_000,
		},
		{
			posterID: "code-auditor",
			title:    "Audit Solidity escrow contract for reentrancy vulnerabilities",
			description: "Review the provided Solidity escrow contract (≤300 lines). Identify reentrancy " +
				"risks, integer overflow/underflow, and access-control issues. Deliver a structured report " +
				"with severity ratings and suggested mitigations.",
			budget: 100_000,
		},
		{
			posterID: "doc-writer",
			title:    "Write OpenAPI documentation for AetherNet task endpoints",
			description: "Produce OpenAPI 3.0-compatible YAML for the 10 /v1/tasks/* endpoints. " +
				"Include request/response schemas, example payloads, error codes, and a 1-page " +
				"quick-start guide.",
			budget: 30_000,
		},
		{
			posterID: "alpha-researcher",
			title:    "Translate ML paper abstract from French to English",
			description: "Translate a 500-word French-language abstract of a machine-learning paper " +
				"into professional academic English. Preserve technical terminology and ensure fluency " +
				"for a native-English audience.",
			budget: 15_000,
		},
		{
			posterID: "data-scientist",
			title:    "Generate analytics SQL queries for a SaaS dashboard",
			description: "Write 10 optimised PostgreSQL queries for a SaaS product-analytics dashboard: " +
				"DAU/MAU, retention cohorts, funnel drop-off, feature adoption, and revenue metrics. " +
				"Include comments explaining each query.",
			budget: 25_000,
		},
	}

	for _, t := range seedTasks {
		task, err := taskMgr.PostTask(t.posterID, t.title, t.description, "", t.budget)
		if err != nil {
			slog.Warn("seedMarketplace: failed to post task", "title", t.title, "err", err)
			continue
		}
		if err := escrowMgr.Hold(task.ID, crypto.AgentID(t.posterID), t.budget); err != nil {
			slog.Warn("seedMarketplace: failed to hold escrow", "task_id", task.ID, "err", err)
		}
	}
	slog.Info("seedMarketplace: seeded task marketplace", "tasks", len(seedTasks))
}

// genesisStore is the subset of store.Store used by genesis idempotency checks.
type genesisStore interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

const genesisMarkerKey = "genesis_complete"

// seedGenesis funds the six genesis allocation buckets using the provided
// TransferLedger. It is called automatically on first start when the store has
// no genesis allocation yet (Docker mode). It is also the implementation shared
// by cmdGenesis to avoid code duplication.
//
// When s is non-nil, seedGenesis is idempotent: it checks for a
// "meta:genesis_complete" marker and returns immediately if found, preventing
// double-funding on repeated invocations.
func seedGenesis(tl *ledger.TransferLedger, s genesisStore) {
	if s != nil {
		data, _ := s.GetMeta(genesisMarkerKey)
		if len(data) > 0 {
			// Verify the treasury was actually funded. If the ledger was wiped but the
			// marker survived (partial store state), re-seed rather than leave all
			// balances at zero.
			if bal, _ := tl.Balance(crypto.AgentID(genesis.BucketTreasury)); bal > 0 {
				slog.Info("auto-genesis: genesis already complete, skipping")
				return
			}
			slog.Warn("auto-genesis: genesis marker present but treasury balance is zero; re-seeding")
		}
	}

	buckets := []struct {
		name   string
		amount uint64
	}{
		{genesis.BucketFounders, genesis.FoundersAllocation},
		{genesis.BucketInvestors, genesis.InvestorsAllocation},
		{genesis.BucketEcosystem, genesis.EcosystemAllocation},
		{genesis.BucketRewards, genesis.NetworkRewards},
		{genesis.BucketTreasury, genesis.TreasuryAllocation},
		{genesis.BucketPublic, genesis.PublicAllocation},
	}
	for _, b := range buckets {
		if err := tl.FundAgent(crypto.AgentID(b.name), b.amount); err != nil {
			slog.Warn("auto-genesis: failed to fund bucket", "bucket", b.name, "err", err)
		}
	}

	if s != nil {
		_ = s.PutMeta(genesisMarkerKey, []byte("1"))
	}
}

// cmdGenesis seeds the initial token supply into the BadgerDB store by funding
// the six protocol-controlled allocation buckets. It is idempotent: running it
// a second time on a store that already has a genesis_complete marker is a
// no-op, protecting operators from accidentally double-funding.
//
// Genesis allocations (micro-AET):
//
//	founders  : 150,000,000,000
//	investors : 150,000,000,000
//	ecosystem : 300,000,000,000
//	rewards   : 200,000,000,000
//	treasury  : 100,000,000,000
//	public    : 100,000,000,000
func cmdGenesis() {
	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}
	s, err := store.NewStore(storePath())
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	// Idempotency check: refuse to run genesis twice on the same store.
	if data, _ := s.GetMeta(genesisMarkerKey); len(data) > 0 {
		fmt.Println("Genesis already complete on this store. Skipping.")
		fmt.Println("To re-run genesis, delete the store first (AETHERNET_RESET=true or wipe manually).")
		return
	}

	tl, err := ledger.LoadTransferLedgerFromStore(s)
	if err != nil {
		slog.Error("failed to load transfer ledger", "err", err)
		os.Exit(1)
	}

	buckets := []struct {
		name   string
		amount uint64
	}{
		{genesis.BucketFounders, genesis.FoundersAllocation},
		{genesis.BucketInvestors, genesis.InvestorsAllocation},
		{genesis.BucketEcosystem, genesis.EcosystemAllocation},
		{genesis.BucketRewards, genesis.NetworkRewards},
		{genesis.BucketTreasury, genesis.TreasuryAllocation},
		{genesis.BucketPublic, genesis.PublicAllocation},
	}

	fmt.Printf("AetherNet Genesis Allocation\nStore: %s\n\n", storePath())
	var total uint64
	for _, b := range buckets {
		if err := tl.FundAgent(crypto.AgentID(b.name), b.amount); err != nil {
			slog.Error("failed to fund genesis bucket", "bucket", b.name, "err", err)
			os.Exit(1)
		}
		fmt.Printf("  %-30s %15d micro-AET\n", b.name, b.amount)
		total += b.amount
	}
	fmt.Printf("\n  %-30s %15d micro-AET\n", "TOTAL", total)

	// Write idempotency marker so repeated runs are safe.
	if err := s.PutMeta(genesisMarkerKey, []byte("1")); err != nil {
		slog.Warn("cmdGenesis: failed to write genesis marker", "err", err)
	}

	fmt.Println("\nGenesis complete.")
}

// cmdStatus loads the keypair and prints node identity and configuration.
// It does not start any networking or background services.
func cmdStatus() {
	kp := loadKeyPair()
	agentID := kp.AgentID()
	p2pAddr := envOr("AETHERNET_LISTEN", "0.0.0.0:8337")
	apiListenAddr := envOr("AETHERNET_API", ":8338")
	cfg := network.DefaultNodeConfig(agentID)

	fmt.Printf("AetherNet %s\n", VERSION)
	fmt.Printf("AgentID    : %s\n", agentID)
	fmt.Printf("Listen addr: %s\n", p2pAddr)
	fmt.Printf("API addr   : %s\n", apiListenAddr)
	fmt.Printf("Max peers  : %d\n", cfg.MaxPeers)
	fmt.Printf("Sync every : %s\n", cfg.SyncInterval)
	fmt.Printf("Key file   : %s\n", keyFilePath())
}
