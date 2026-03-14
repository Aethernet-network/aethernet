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
//	AETHERNET_DATA      base directory for key file and BadgerDB store (default: ".")
//	AETHERNET_LISTEN    p2p TCP listen address  (default: "0.0.0.0:8337")
//	AETHERNET_API       REST API listen address (default: ":8338")
//	AETHERNET_PEER      comma-separated peer addresses to auto-connect on startup (default: "")
//	AETHERNET_RESET     set to "true" to wipe the database on startup (testnet recovery)
//	AETHERNET_DISCOVER  DNS name resolved periodically for automatic peer discovery (default: "")
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/cloudmap"
	"github.com/Aethernet-network/aethernet/internal/config"
	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/verification"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
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
	"github.com/Aethernet-network/aethernet/internal/replay"
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
	fmt.Fprintf(os.Stderr, "  AETHERNET_PEER      comma-separated peer addresses to connect on startup\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_RESET     set to \"true\" to wipe the database on startup\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_DISCOVER  DNS name for automatic peer discovery (e.g. nodes.aethernet.local)\n")
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
	platformKeys    *platformpkg.KeyManager
	taskRouter      *router.Router
	peerDiscovery   *network.PeerDiscovery
	cloudmapReg     *cloudmap.Registrar
	replayRunner    *replay.ReplayRunner
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

// replayGenTrigger adapts *ledger.GenerationLedger to the
// replay.generationTrigger interface. It is used by ReplayEnforcer to release
// held generation credits after a replay confirms the original work.
type replayGenTrigger struct {
	gl      *ledger.GenerationLedger
	agentID crypto.AgentID
}

func (t *replayGenTrigger) RecordGeneration(taskID, agentID, resultHash, title string, value uint64) error {
	id := crypto.AgentID(agentID)
	if id == "" {
		id = t.agentID
	}
	return t.gl.RecordTaskGeneration(id, resultHash, title, value, taskID)
}

// routerCalibrationAdapter bridges *canary.CanaryManager (L3) to the router's
// calibrationSource interface (L2). The router cannot import canary directly
// (layer boundary: L2 must not import L3), so this adapter lives in cmd/node
// where all layers are already imported.
type routerCalibrationAdapter struct {
	mgr *canary.CanaryManager
}

func (a *routerCalibrationAdapter) CategoryCalibrationForActor(agentID, category string) (*router.CalibrationData, error) {
	cal, err := a.mgr.CategoryCalibrationForActor(agentID, category)
	if err != nil || cal == nil {
		return nil, err
	}
	return &router.CalibrationData{
		TotalSignals: cal.TotalSignals,
		Accuracy:     cal.Accuracy,
		AvgSeverity:  cal.AvgSeverity,
	}, nil
}

// taskReplayDetailsAdapter adapts *tasks.TaskManager to replay.TaskDetailsProvider.
// It is used by the ReplayRunner to look up task metadata for ProcessReplayOutcome.
type taskReplayDetailsAdapter struct {
	tm *tasks.TaskManager
}

func (a *taskReplayDetailsAdapter) GetReplayDetails(taskID string) (agentID, resultHash, title string, verifiedValue uint64, generationEligible bool, err error) {
	task, taskErr := a.tm.Get(taskID)
	if taskErr != nil {
		return "", "", "", 0, false, taskErr
	}
	if task.VerificationScore != nil {
		verifiedValue = uint64(float64(task.Budget) * task.VerificationScore.Overall)
	}
	return task.ClaimerID, task.ResultHash, task.Title, verifiedValue, task.Contract.GenerationEligible, nil
}

// buildStack wires all internal packages together and returns a ready-to-start
// nodeStack. When s is non-nil, state is restored from the store.
// cfg controls all tunable protocol parameters; nil falls back to defaults.
func buildStack(s *store.Store, kp *crypto.KeyPair, cfg *config.ProtocolConfig) *nodeStack {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
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
	ocsCfg := ocs.DefaultConfig()
	ocsCfg.MaxPendingItems = cfg.OCS.MaxPendingItems
	ocsCfg.MinStakeRequired = cfg.OCS.MinStakeRequired
	ocsCfg.VerificationTimeout = cfg.OCS.SettlementTimeout.Duration
	ocsCfg.CheckInterval = cfg.OCS.CheckInterval.Duration
	eng := ocs.NewEngine(ocsCfg, tl, gl, reg)
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
	// Wire VotingRound persistence so in-flight consensus rounds survive node
	// restarts. Votes are written to BadgerDB after each RegisterVote and
	// reloaded on boot, preventing silent vote loss (NEW-1).
	if s != nil {
		votingRound.SetPersistence(s)
		if err := votingRound.LoadPersistedVotes(s); err != nil {
			slog.Warn("failed to reload persisted votes from store", "err", err)
		}
	}

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
	// Use LoadTaskManagerFromStore when a store is available so that tasks,
	// results, and completion history survive restarts.
	var taskMgr *tasks.TaskManager
	if s != nil {
		var err error
		taskMgr, err = tasks.LoadTaskManagerFromStore(s)
		if err != nil {
			slog.Warn("failed to restore task marketplace from store; starting fresh", "err", err)
			taskMgr = tasks.NewTaskManager()
		}
	} else {
		taskMgr = tasks.NewTaskManager()
	}
	escrowMgr := escrow.New(tl)
	if s != nil {
		escrowMgr.SetStore(s)
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
	taskRouter := router.New(&taskManagerSource{tm: taskMgr}, claimFn, repFn, cfg.Router.RoutingInterval.Duration)
	taskRouter.SetNewcomerParams(cfg.Router.NewcomerThreshold, cfg.Router.NewcomerAllocation, cfg.Router.MaxNewcomerBudget)
	taskRouter.SetWebhookTimeout(cfg.Router.WebhookTimeout.Duration)
	taskRouter.SetClearRoutedToFunc(func(taskID string) error {
		return taskMgr.ClearRoutedTo(taskID)
	})

	// Apply configurable task lifecycle params.
	taskMgr.SetClaimDeadline(cfg.Tasks.DefaultClaimDeadline.Duration)
	taskMgr.SetMaxCompletedAge(cfg.Tasks.MaxCompletedAge.Duration)

	// Apply staking decay configuration.
	staking.SetDecayParams(cfg.Staking.DecayPeriodDays, cfg.Staking.DecayTasksPenalty)

	// Apply fee distribution configuration.
	feeCollector.SetFeeParams(cfg.Fees.FeeBasisPoints, cfg.Fees.FeeValidatorShare, cfg.Fees.FeeTreasuryShare)

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
// cfg controls all tunable protocol parameters; nil falls back to defaults.
func startStack(stack *nodeStack, agentID crypto.AgentID, p2pAddr, apiListenAddr string, enableMarketplace bool, cfg *config.ProtocolConfig, noAuth bool) *network.Node {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
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
		tvFP.ReputationScore = 5000 // 50 % reputation
		tvFP.StakedAmount = 10000   // 10 000 micro-AET (consensus metadata only)
		_ = stack.reg.Register(tvFP)
	}

	// Fund and stake the testnet-validator from the network rewards allocation.
	// Idempotent: skips funding when balance >= tvValidatorMinBalance and skips
	// staking when already staked. This gives the validator real economic
	// participation so its 80% fee share lands in a spendable balance and it
	// can participate in transfers with collateral at risk.
	const (
		tvValidatorFundTarget = uint64(200_000_000_000) // 200,000 AET target balance
		tvValidatorMinBalance = uint64(10_000_000_000)  // 10,000 AET top-up threshold
		tvValidatorStakeAmt   = uint64(50_000_000_000)  // 50,000 AET stake
	)
	tvInitialBal, _ := stack.transfer.Balance(testnetValidatorID)
	tvStakedBefore := stack.stakeManager.StakedAmount(testnetValidatorID)

	tvTopUp := uint64(0)
	if tvInitialBal < tvValidatorMinBalance {
		tvTopUp = tvValidatorFundTarget - tvInitialBal
		if err := stack.transfer.TransferFromBucket(
			crypto.AgentID(genesis.BucketRewards), testnetValidatorID, tvTopUp,
		); err != nil {
			slog.Warn("startStack: failed to fund testnet-validator from rewards",
				"err", err, "top_up", tvTopUp)
			tvTopUp = 0
		}
	}

	tvStaked := false
	if tvStakedBefore == 0 {
		if err := stack.stakeManager.Stake(testnetValidatorID, tvValidatorStakeAmt); err != nil {
			slog.Warn("startStack: failed to stake testnet-validator", "err", err)
		} else {
			tvStaked = true
		}
	}

	tvPostBal, _ := stack.transfer.Balance(testnetValidatorID)
	tvStalePurged := 0
	if tvPostBal < tvValidatorMinBalance {
		tvStalePurged = stack.transfer.ResetOptimisticOutflows(testnetValidatorID)
		tvPostBal, _ = stack.transfer.Balance(testnetValidatorID)
		slog.Warn("startStack: purged stale optimistic outflows for testnet-validator",
			"entries_removed", tvStalePurged, "balance_after", tvPostBal)
	}
	slog.Info("startStack: testnet-validator ready",
		"balance_before", tvInitialBal,
		"top_up", tvTopUp,
		"staked_before", tvStakedBefore,
		"newly_staked", tvStaked,
		"stale_purged", tvStalePurged,
		"balance_after", tvPostBal,
	)

	av := autovalidator.NewAutoValidator(stack.engine, testnetValidatorID, 5*time.Second)
	av.SetFeeCollector(stack.feeCollector, crypto.AgentID(genesis.BucketTreasury))
	av.SetGenerationLedger(stack.generation)
	av.SetRegistry(stack.reg)
	av.SetDAG(stack.dag)
	av.SetKeyPair(stack.kp)
	vr := evidence.NewVerifierRegistry()
	vr.SetPassThresholds(cfg.Evidence.CodePassThreshold, cfg.Evidence.DataPassThreshold, cfg.Evidence.ContentPassThreshold)
	av.SetVerifierRegistry(vr)
	av.SetVerificationService(verification.NewInProcessVerifier(vr))

	// Wire the replay coordinator so the auto-validator can schedule async
	// verification replays for selected tasks. The coordinator is backed by
	// the node's BadgerDB store via the replayStore interface.
	var replayEnforcer *replay.ReplayEnforcer
	var submissionProc *replay.SubmissionProcessor
	var canaryMgr *canary.CanaryManager
	if stack.store != nil {
		replayCoord := replay.NewReplayCoordinator(replay.DefaultReplayPolicy(), stack.store)
		av.SetReplayCoordinator(replayCoord)

		// ReplayEnforcer maps completed outcomes to task state changes.
		// The generation trigger releases held generation credits after a
		// replay confirms the original work.
		replayResolver := replay.NewReplayResolver(stack.store)
		genTrigger := &replayGenTrigger{gl: stack.generation, agentID: agentID}
		replayEnforcer = replay.NewReplayEnforcer(stack.taskMgr, replayResolver, genTrigger)

		// ReplayRunner polls for pending replay jobs and executes them via
		// the InspectionExecutor (testnet: material assessment, no sandbox).
		replayDetails := &taskReplayDetailsAdapter{tm: stack.taskMgr}
		stack.replayRunner = replay.NewReplayRunner(
			replayCoord,
			replay.NewInspectionExecutor(),
			replayEnforcer,
			replayDetails,
			30*time.Second, // poll every 30 seconds
		)
		stack.replayRunner.Start()

		// SubmissionProcessor handles POST /v1/replay/submit: external replay
		// executors submit raw check results; the protocol performs the comparison.
		submissionProc = replay.NewSubmissionProcessor(stack.store, replayEnforcer, replayDetails)

		// Wire canary evaluation. The CanaryManager bridges the raw store to
		// typed canary operations. Injection is disabled by default; set
		// AETHERNET_CANARY_ENABLED=true to activate. The injection rate can be
		// overridden with AETHERNET_CANARY_RATE (float, 0.0–1.0).
		canaryMgr = canary.NewCanaryManager(stack.store)
		injCfg := canary.DefaultInjectorConfig()
		if os.Getenv("AETHERNET_CANARY_ENABLED") == "true" {
			injCfg.Enabled = true
		}
		if rateStr := os.Getenv("AETHERNET_CANARY_RATE"); rateStr != "" {
			if rate, parseErr := strconv.ParseFloat(rateStr, 64); parseErr == nil {
				injCfg.InjectionRate = rate
			}
		}
		canaryInj := canary.NewInjector(injCfg, canaryMgr)
		canaryEval := canary.NewEvaluator(canaryMgr)
		// Wire injection into the task creation path so that PostTask
		// probabilistically links measurement canaries to new tasks.
		stack.taskMgr.SetCanaryInjector(canaryInj)
		// Wire into auto-validator (IsCanary lookup on settlement path).
		av.SetCanaryInjector(canaryInj)
		av.SetCanaryEvaluator(canaryMgr, canaryEval)
		if replayEnforcer != nil {
			replayEnforcer.SetCanaryEvaluator(canaryMgr, canaryEval)
		}
		// Wire calibration-aware scrutiny: the replay coordinator uses the
		// canary manager to look up per-actor per-category accuracy and adjust
		// effective sample rates accordingly. Opt-in via config or
		// AETHERNET_CALIBRATION_SCRUTINY=true.
		replayCoord.SetCalibrationSource(canaryMgr)
		replayCoord.SetCalibrationEnabled(cfg.Calibration.ScrutinyEnabled)

		// Wire calibration-aware routing: agents with strong per-category
		// calibration receive a mild routing score boost; agents with weak
		// calibration are mildly penalized. Disabled by default; opt-in via
		// AETHERNET_CALIBRATION_ROUTING=true or the config file.
		if stack.taskRouter != nil {
			stack.taskRouter.SetCalibrationSource(&routerCalibrationAdapter{mgr: canaryMgr})
			stack.taskRouter.SetCalibrationRoutingEnabled(cfg.Calibration.RoutingEnabled)
			stack.taskRouter.SetCalibrationFactors(
				cfg.Calibration.BoostFactor,
				cfg.Calibration.PenaltyFactor,
				cfg.Calibration.StrongThreshold,
				cfg.Calibration.WeakThreshold,
			)
		}
	}

	// Task marketplace integration is conditional on --marketplace flag.
	if enableMarketplace {
		av.SetTaskManager(stack.taskMgr, stack.escrowMgr)
		av.SetReputationManager(stack.reputationMgr)
	}
	av.Start()
	stack.autoVal = av

	// Activate ledger archival: evict Settled/Adjusted entries older than the
	// configured threshold from memory. Data is never deleted from the store —
	// this prevents OOM on long-running nodes processing thousands of transactions.
	archiveCfg := ledger.ArchiveConfig{
		Threshold: cfg.Archival.ArchiveThreshold.Duration,
		Interval:  cfg.Archival.ArchiveInterval.Duration,
	}
	stack.transfer.Start(archiveCfg)
	stack.generation.Start(archiveCfg)

	// Fix 4: activate background cleanup goroutine (evicts tasks > MaxCompletedAge).
	stack.taskMgr.Start()

	nodeCfg := network.DefaultNodeConfig(agentID)
	nodeCfg.ListenAddr = p2pAddr
	nodeCfg.KeyPair = stack.kp // Fix 1: wire keypair so P2P votes are signed
	nodeCfg.MaxPeers = cfg.Network.MaxPeers
	nodeCfg.SyncInterval = cfg.Network.SyncInterval.Duration
	nodeCfg.HandshakeTimeout = cfg.Network.HandshakeTimeout.Duration
	nodeCfg.VoteMaxAge = cfg.Network.VoteMaxAge
	nodeCfg.MaxMessageBytes = cfg.Network.P2PMaxMessageBytes
	node := network.NewNode(nodeCfg, stack.dag)
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
			stack.taskRouter.Start()
			apiSrv.SetTaskRouter(stack.taskRouter)
		}
		// Wire the replay enforcer so POST /v1/replay/outcome is active when the
		// marketplace is enabled. The enforcer is nil when store is not available.
		if replayEnforcer != nil {
			apiSrv.SetReplayEnforcer(replayEnforcer)
		}
		// Wire the submission processor so POST /v1/replay/submit accepts raw
		// check results from external replay executors.
		if submissionProc != nil {
			apiSrv.SetSubmissionProcessor(submissionProc)
		}
	}
	apiSrv.SetEconomics(stack.walletMgr, stack.stakeManager, stack.feeCollector)
	apiSrv.SetMinTaskBudget(cfg.Tasks.MinTaskBudget)
	// Wire canary calibration endpoints. Only available when the store is present.
	if canaryMgr != nil {
		apiSrv.SetCalibrationStore(canaryMgr)
		apiSrv.SetCalibrationAgentsStore(canaryMgr)
	}
	apiSrv.SetEventBus(bus)
	if stack.platformKeys != nil {
		apiSrv.SetPlatformKeys(stack.platformKeys)
	}
	// CRITICAL-1: auth defaults to true in NewServer. Disable only when --no-auth
	// is explicitly requested (testnet/development). A warning is emitted below.
	if noAuth {
		apiSrv.SetRequireAuth(false)
		slog.Warn("⚠️  API authentication is DISABLED — all write endpoints are open to unauthenticated callers. Do NOT use in production.")
	}
	// CRITICAL-5: wire identity registry lookup so P2P votes are verified against
	// the registered public key, preventing voter impersonation.
	node.SetIdentityLookup(func(id crypto.AgentID) []byte {
		fp, err := stack.reg.Get(id)
		if err != nil {
			return nil
		}
		return fp.PublicKey
	})
	apiSrv.SetRateLimiters(
		ratelimit.New(ratelimit.Config{Rate: cfg.RateLimit.WriteRatePerSec, Burst: cfg.RateLimit.WriteBurst, CleanupAge: 5 * time.Minute}),
		ratelimit.New(ratelimit.Config{Rate: cfg.RateLimit.ReadRatePerSec, Burst: cfg.RateLimit.ReadBurst, CleanupAge: 5 * time.Minute}),
	)
	// Sybil resistance: limit registrations per hour per IP.
	regRate := float64(cfg.RateLimit.RegistrationPerHour) / 3600
	apiSrv.SetRegistrationLimiter(ratelimit.New(ratelimit.Config{
		Rate:       regRate,
		Burst:      cfg.RateLimit.RegistrationPerHour,
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

	return node
}

// stopStack tears down the API server, network node, OCS engine, and persistence
// store in safe reverse-startup order.
func stopStack(node *network.Node, stack *nodeStack) {
	// Deregister from Cloud Map before stopping everything else so the DNS
	// entry is removed while the node is still partially functional.
	stack.cloudmapReg.Stop()

	if stack.peerDiscovery != nil {
		stack.peerDiscovery.Stop()
	}
	// Stop ledger archival goroutines before shutting down other components.
	if stack.transfer != nil {
		stack.transfer.Stop()
	}
	if stack.generation != nil {
		stack.generation.Stop()
	}
	if stack.taskMgr != nil {
		stack.taskMgr.Stop() // Fix 4: stop background cleanup goroutine
	}
	if stack.taskRouter != nil {
		stack.taskRouter.Stop()
	}
	if stack.metricsStop != nil {
		close(stack.metricsStop)
	}
	if stack.replayRunner != nil {
		stack.replayRunner.Stop()
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
	peerAddr := fs.String("peer", envOr("AETHERNET_PEER", ""), "comma-separated peer addresses to auto-connect on startup (host:port[,host:port...])")
	discoverAddr := fs.String("discover", envOr("AETHERNET_DISCOVER", ""), "DNS name resolved periodically for automatic peer discovery (e.g. nodes.aethernet.local)")
	enableMarketplace := fs.Bool("marketplace", false, "Enable built-in marketplace (task routing, escrow, explorer) in the combined single-binary deployment")
	configPath := fs.String("config", envOr("AETHERNET_CONFIG", ""), "path to protocol config JSON file (default: built-in defaults)")
	noAuth := fs.Bool("no-auth", false, "Disable API authentication (testnet/development only — NOT safe for production)")
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

	// Load protocol configuration. LoadFromFile returns DefaultConfig when path
	// is empty. LoadFromEnv applies AETHERNET_* overrides on top.
	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "err", err)
		os.Exit(1)
	}
	config.LoadFromEnv(cfg)

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s := openStoreWithRecovery(storePath())

	stack := buildStack(s, kp, cfg)

	// Genesis consistency check: if the stored bucket totals don't match the
	// current TotalSupply constant the binary was built with different allocation
	// constants than the store was seeded with (stale data). On testnet we wipe
	// and re-seed automatically; on mainnet we log an error and continue.
	if !checkGenesisConsistency(stack.transfer, stack.reg) {
		if os.Getenv("AETHERNET_TESTNET") == "true" {
			slog.Warn("genesis consistency check failed on testnet: wiping store and re-seeding")
			stack.store.Close()
			if err := wipePath(storePath()); err != nil {
				slog.Error("genesis reset: failed to wipe store", "err", err)
				os.Exit(1)
			}
			s = openStoreWithRecovery(storePath())
			stack = buildStack(s, kp, cfg)
		} else {
			slog.Error("genesis consistency check failed: store was seeded with different allocation constants; manual intervention required")
		}
	}

	// Auto-genesis: on first Docker start, seed the initial token supply when
	// any genesis bucket is empty. Checks both founders and ecosystem so that
	// a partial-wipe scenario (e.g. EFS loses ledger entries but keeps the
	// meta:genesis_complete marker) still triggers a re-seed. Only runs in
	// non-interactive mode (AETHERNET_DATA is set) to preserve the manual
	// genesis workflow in interactive / development environments. Pass the
	// store so seedGenesis writes an idempotency marker preventing double-runs.
	if os.Getenv("AETHERNET_DATA") != "" {
		foundersBalance, _ := stack.transfer.Balance(crypto.AgentID(genesis.BucketFounders))
		ecosystemBalance, _ := stack.transfer.Balance(crypto.AgentID(genesis.BucketEcosystem))
		if foundersBalance == 0 || ecosystemBalance == 0 {
			slog.Info("auto-genesis: seeding initial token supply")
			seedGenesis(stack.transfer, stack.store)
			fmt.Println("Auto-genesis: initial token supply seeded.")
		}
	}

	// Enforce the protocol-level mint cap immediately after genesis completes.
	// When totalMinted > 0 the genesis allocation is on record; any subsequent
	// FundAgent call that would push totalMinted past this cap is rejected at
	// the ledger level rather than relying solely on application-level guards.
	// A zero totalMinted means genesis has not yet run (interactive / manual
	// flow) — leave cap unlimited so the operator can run it separately.
	if minted := stack.transfer.TotalMinted(); minted > 0 {
		stack.transfer.SetMintCap(minted)
		slog.Info("ledger: mint cap enforced", "cap_micro_aet", minted)
	}

	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr, *enableMarketplace, cfg, *noAuth)

	// AWS Cloud Map registration — auto-registers this node's private IP so other
	// ECS tasks can discover peers via DNS. No-op when
	// AETHERNET_CLOUDMAP_SERVICE_ID is not set (non-ECS deployments).
	_, p2pPortStr, _ := net.SplitHostPort(*p2pAddr)
	_, apiPortStr, _ := net.SplitHostPort(*apiListenAddr)
	reg := cloudmap.NewRegistrar(p2pPortStr, apiPortStr)
	reg.Start()
	stack.cloudmapReg = reg

	// One-time cleanup: remove ghost agents (0 balance, 0 stake, 0 tasks) that
	// were registered by an older binary before the TransferFromBucket onboarding
	// fix. Gated to AETHERNET_TESTNET=true and idempotent via a store meta key.
	if os.Getenv("AETHERNET_TESTNET") == "true" && stack.store != nil {
		const ghostCleanKey = "seed_agents_cleaned"
		if _, err := stack.store.GetMeta(ghostCleanKey); err != nil {
			cleaned := 0
			for _, fp := range stack.reg.All(0, 0) {
				if fp.TasksCompleted > 0 || fp.TasksFailed > 0 {
					continue
				}
				bal, _ := stack.transfer.Balance(fp.AgentID)
				staked := uint64(0)
				if stack.stakeManager != nil {
					staked = stack.stakeManager.StakedAmount(fp.AgentID)
				}
				if bal == 0 && staked == 0 {
					if err := stack.reg.Remove(fp.AgentID); err == nil {
						cleaned++
					}
				}
			}
			_ = stack.store.PutMeta(ghostCleanKey, []byte("1"))
			slog.Info("testnet: ghost agent cleanup complete", "removed", cleaned)
		}
	}

	// DNS-based peer discovery: periodically resolve a DNS name and connect to
	// any new IP addresses it returns. Designed for AWS Cloud Map or any other
	// service-discovery system that publishes peer addresses as DNS A records.
	// Additive: --peer static connections are still applied below.
	if *discoverAddr != "" {
		_, portStr, err := net.SplitHostPort(*p2pAddr)
		if err != nil {
			portStr = "8337"
		}
		pd := network.NewPeerDiscovery(*discoverAddr, portStr, node, 30*time.Second)
		pd.Start()
		stack.peerDiscovery = pd
		slog.Info("peer discovery started", "dns", *discoverAddr, "port", portStr, "interval", "30s")
	}

	fmt.Printf("AetherNet %s\nAgentID  : %s\nListening: %s\nAPI      : %s\n\n",
		VERSION, agentID, node.ListenAddr(), *apiListenAddr)

	// Connect to one or more bootstrap peers. AETHERNET_PEER (and --peer) accepts
	// comma-separated addresses so multi-node deployments can be wired from env.
	// Failures are non-fatal: in Docker the peer container may not be ready yet;
	// the operator can retry or rely on the periodic sync interval to catch up.
	if *peerAddr != "" {
		for _, addr := range strings.Split(*peerAddr, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			fmt.Printf("Connecting to %s...\n", addr)
			p, err := node.Connect(addr)
			if err != nil {
				slog.Warn("failed to auto-connect to peer", "addr", addr, "err", err)
				fmt.Printf("Warning: could not connect to %s: %v\n", addr, err)
			} else {
				slog.Info("connected to peer", "addr", addr, "agent_id", p.AgentID)
				fmt.Printf("Connected  : %s  (%s)\n", p.AgentID, addr)
			}
		}
		fmt.Println()
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
	configPath := fs.String("config", envOr("AETHERNET_CONFIG", ""), "path to protocol config JSON file (default: built-in defaults)")
	noAuth := fs.Bool("no-auth", false, "Disable API authentication (testnet/development only — NOT safe for production)")
	_ = fs.Parse(os.Args[2:])

	if *peerAddr == "" {
		fmt.Fprintln(os.Stderr, "usage: aethernet connect --peer <host:port>")
		os.Exit(1)
	}

	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "err", err)
		os.Exit(1)
	}
	config.LoadFromEnv(cfg)

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s := openStoreWithRecovery(storePath())

	stack := buildStack(s, kp, cfg)
	// Enforce mint cap if genesis has already been run on this store.
	if minted := stack.transfer.TotalMinted(); minted > 0 {
		stack.transfer.SetMintCap(minted)
		slog.Info("ledger: mint cap enforced", "cap_micro_aet", minted)
	}
	// cmdConnect is the legacy subcommand; marketplace is disabled by default.
	// Use 'aethernet start --marketplace' for the combined deployment.
	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr, false, cfg, *noAuth)

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


// checkGenesisConsistency runs two checks against the loaded store:
//
//  1. Bucket sum check: the sum of all six genesis bucket balances must equal
//     genesis.TotalSupply. A mismatch means the store was seeded by a different
//     binary (different allocation constants).
//
//  2. Zombie-agent check: if the identity registry has registered agents but the
//     ecosystem bucket balance equals exactly genesis.EcosystemAllocation (i.e.
//     no onboarding transfer has ever drawn from it), those agents were registered
//     before the TransferFromBucket onboarding fix and hold zero balance. The
//     store is stale and must be wiped so they re-register and receive funds.
//
// Returns true when the store is consistent, false when either check fails.
// A zero bucket total means genesis hasn't run yet; the auto-genesis block
// handles that case, so we return true here.
func checkGenesisConsistency(tl *ledger.TransferLedger, reg *identity.Registry) bool {
	buckets := []struct {
		name string
	}{
		{genesis.BucketFounders},
		{genesis.BucketInvestors},
		{genesis.BucketEcosystem},
		{genesis.BucketRewards},
		{genesis.BucketTreasury},
		{genesis.BucketPublic},
	}

	var total uint64
	for _, b := range buckets {
		bal, _ := tl.Balance(crypto.AgentID(b.name))
		total += bal
	}

	// Zero total means genesis hasn't run yet; auto-genesis handles this.
	if total == 0 {
		return true
	}

	if total != genesis.TotalSupply {
		// Total doesn't match — log each bucket for diagnosis.
		slog.Warn("genesis consistency check failed: bucket total does not match TotalSupply",
			"bucket_total", total,
			"expected", genesis.TotalSupply,
			genesis.BucketFounders, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketFounders)); return b }(),
			genesis.BucketInvestors, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketInvestors)); return b }(),
			genesis.BucketEcosystem, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketEcosystem)); return b }(),
			genesis.BucketRewards, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketRewards)); return b }(),
			genesis.BucketTreasury, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketTreasury)); return b }(),
			genesis.BucketPublic, func() uint64 { b, _ := tl.Balance(crypto.AgentID(genesis.BucketPublic)); return b }(),
		)
		return false
	}

	// Zombie-agent check: agents registered before the TransferFromBucket
	// onboarding fix received no allocation (ecosystem balance was never drawn
	// down). Detect this by checking whether any agents are registered while the
	// ecosystem bucket still holds its full genesis allocation.
	ecosystemBal, _ := tl.Balance(crypto.AgentID(genesis.BucketEcosystem))
	if ecosystemBal == genesis.EcosystemAllocation && len(reg.All(1, 0)) > 0 {
		slog.Warn("genesis consistency check failed: agents registered but ecosystem bucket is untouched (zombie agents from pre-onboarding-fix binary)",
			"registered_agents", len(reg.All(0, 0)),
			"ecosystem_balance", ecosystemBal,
		)
		return false
	}

	return true
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
			// Verify the treasury AND ecosystem buckets were actually funded. Either
			// bucket being zero indicates a partial-wipe (ledger entries lost but the
			// marker key survived), so we re-seed rather than leave balances at zero.
			treasuryBal, _ := tl.Balance(crypto.AgentID(genesis.BucketTreasury))
			ecosystemBal, _ := tl.Balance(crypto.AgentID(genesis.BucketEcosystem))
			if treasuryBal > 0 && ecosystemBal > 0 {
				slog.Info("auto-genesis: genesis already complete, skipping")
				return
			}
			slog.Warn("auto-genesis: genesis marker present but balances incomplete; re-seeding",
				"treasury", treasuryBal, "ecosystem", ecosystemBal)
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
