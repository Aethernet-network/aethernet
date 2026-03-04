// Package main is the AetherNet node binary. It wires together every internal
// package — DAG, dual ledger, supply, identity, OCS engine, and p2p network —
// into a single runnable process.
//
// Subcommands:
//
//	aethernet init                      generate a new node identity
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

	"github.com/aethernet/core/internal/api"
	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/network"
	"github.com/aethernet/core/internal/ocs"
	"github.com/aethernet/core/internal/store"
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
	dag        *dag.DAG
	transfer   *ledger.TransferLedger
	generation *ledger.GenerationLedger
	supply     *ledger.SupplyManager
	reg        *identity.Registry
	engine     *ocs.Engine
	store      *store.Store
	kp         *crypto.KeyPair
	apiSrv     *api.Server
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
	return &nodeStack{
		dag:        d,
		transfer:   tl,
		generation: gl,
		supply:     sm,
		reg:        reg,
		engine:     eng,
		store:      s,
		kp:         kp,
	}
}

// printStatus writes a single-line status summary every tick.
func printStatus(agentID crypto.AgentID, d *dag.DAG, n *network.Node, eng *ocs.Engine, sm *ledger.SupplyManager) {
	ratio, _ := sm.SupplyRatio()
	id := string(agentID)
	if len(id) > 16 {
		id = id[:16] + "..."
	}
	fmt.Printf("[%s]  peers=%-3d  dag=%-6d  ocs_pending=%-4d  supply=%.4fx\n",
		id, n.PeerCount(), d.Size(), eng.PendingCount(), ratio)
}

// startStack starts the OCS engine, network node, and HTTP API server.
// p2pAddr and apiListenAddr override the defaults and may come from flags or
// environment variables.
func startStack(stack *nodeStack, agentID crypto.AgentID, p2pAddr, apiListenAddr string) *network.Node {
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
	if err := apiSrv.Start(); err != nil {
		slog.Error("failed to start API server", "addr", apiListenAddr, "err", err)
		node.Stop()
		stack.engine.Stop()
		os.Exit(1)
	}
	stack.apiSrv = apiSrv
	return node
}

// stopStack tears down the API server, network node, OCS engine, and persistence
// store in safe reverse-startup order.
func stopStack(node *network.Node, stack *nodeStack) {
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
func runLoop(agentID crypto.AgentID, d *dag.DAG, node *network.Node, eng *ocs.Engine, sm *ledger.SupplyManager) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	printStatus(agentID, d, node, eng, sm)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			slog.Info("shutdown signal received")
			return
		case <-ticker.C:
			printStatus(agentID, d, node, eng, sm)
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

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply)
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

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply)
	stopStack(node, stack)
	slog.Info("node stopped cleanly")
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
