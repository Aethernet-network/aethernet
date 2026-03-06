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
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/metrics"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/ratelimit"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/store"
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
func storePath() string {
	return filepath.Join(dataDir(), "data", "aethernet.db")
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
	fmt.Fprintf(os.Stderr, "  aethernet init                            generate a new node identity\n")
	fmt.Fprintf(os.Stderr, "  aethernet genesis                         seed genesis token supply into the store\n")
	fmt.Fprintf(os.Stderr, "  aethernet start [--listen addr] [--api addr] [--peer addr]\n")
	fmt.Fprintf(os.Stderr, "                                            start the node\n")
	fmt.Fprintf(os.Stderr, "  aethernet connect --peer <address>        start and connect to a peer\n")
	fmt.Fprintf(os.Stderr, "  aethernet status                          print node identity and config\n")
	fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_DATA    data directory (default: current directory)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_LISTEN  p2p listen address (default: 0.0.0.0:8337)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_API     API listen address (default: :8338)\n")
	fmt.Fprintf(os.Stderr, "  AETHERNET_PEER    peer to connect on startup\n")
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
	dag         *dag.DAG
	transfer    *ledger.TransferLedger
	generation  *ledger.GenerationLedger
	supply      *ledger.SupplyManager
	reg         *identity.Registry
	engine      *ocs.Engine
	store       *store.Store
	kp          *crypto.KeyPair
	apiSrv      *api.Server
	svcRegistry *registry.Registry
	bus         *eventbus.Bus
	metricsReg  *metrics.Registry
	nodeMetrics *metrics.AetherNetMetrics
	metricsStop chan struct{} // closed to terminate the gauge-update goroutine
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
	svcReg := registry.New()
	if s != nil {
		svcReg.SetStore(s)
		if err := svcReg.LoadFromStore(); err != nil {
			slog.Error("failed to load service registry", "err", err)
			os.Exit(1)
		}
	}

	return &nodeStack{
		dag:         d,
		transfer:    tl,
		generation:  gl,
		supply:      sm,
		reg:         reg,
		engine:      eng,
		store:       s,
		kp:          kp,
		svcRegistry: svcReg,
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
// environment variables.
func startStack(stack *nodeStack, agentID crypto.AgentID, p2pAddr, apiListenAddr string) *network.Node {
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

	cfg := network.DefaultNodeConfig(agentID)
	cfg.ListenAddr = p2pAddr
	node := network.NewNode(cfg, stack.dag)
	if err := node.Start(); err != nil {
		slog.Error("failed to start network listener", "addr", p2pAddr, "err", err)
		stack.engine.Stop()
		os.Exit(1)
	}

	apiSrv := api.NewServer(
		apiListenAddr,
		stack.dag, stack.transfer, stack.generation,
		stack.reg, stack.engine, stack.supply,
		node, stack.kp,
	)
	if stack.svcRegistry != nil {
		apiSrv.SetServiceRegistry(stack.svcRegistry)
	}
	apiSrv.SetEventBus(bus)
	apiSrv.SetRateLimiters(
		ratelimit.New(ratelimit.DefaultConfig()),
		ratelimit.New(ratelimit.ReadOnlyConfig()),
	)
	apiSrv.SetMetrics(metricsReg, nodeMetrics)
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
	if stack.metricsStop != nil {
		close(stack.metricsStop)
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
	if err := os.MkdirAll(filepath.Join(dataDir(), "data"), 0o700); err != nil {
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
	_ = fs.Parse(os.Args[2:])

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}
	s, err := store.NewStore(storePath())
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}

	stack := buildStack(s, kp)

	// Auto-genesis: on first Docker start, seed the initial token supply when
	// the founders bucket is empty. Only runs in non-interactive mode
	// (AETHERNET_DATA is set) to preserve the manual genesis workflow in
	// interactive / development environments.
	if os.Getenv("AETHERNET_DATA") != "" {
		foundersBalance, _ := stack.transfer.Balance(crypto.AgentID(genesis.BucketFounders))
		if foundersBalance == 0 {
			slog.Info("auto-genesis: seeding initial token supply")
			seedGenesis(stack.transfer)
			fmt.Println("Auto-genesis: initial token supply seeded.")
		}
	}

	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr)

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

	if err := os.MkdirAll(filepath.Dir(storePath()), 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}
	s, err := store.NewStore(storePath())
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}

	stack := buildStack(s, kp)
	node := startStack(stack, agentID, *p2pAddr, *apiListenAddr)

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

// seedGenesis funds the six genesis allocation buckets using the provided
// TransferLedger. It is called automatically on first start when the store has
// no genesis allocation yet (Docker mode). It is also the implementation shared
// by cmdGenesis to avoid code duplication.
func seedGenesis(tl *ledger.TransferLedger) {
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
}

// cmdGenesis seeds the initial token supply into the BadgerDB store by funding
// the six protocol-controlled allocation buckets. It is idempotent: running it
// again after the store already has balances will add the allocations again, so
// operators should call it exactly once on a fresh store.
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
