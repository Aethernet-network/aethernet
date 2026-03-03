// Package main is the AetherNet node binary. It wires together every internal
// package — DAG, dual ledger, supply, identity, OCS engine, and p2p network —
// into a single runnable process.
//
// Subcommands:
//
//	aethernet init                      generate a new node identity
//	aethernet start                     start the node
//	aethernet connect --peer <address>  start and dial a specific peer
//	aethernet status                    print identity and config, no networking
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

// apiAddr is the TCP address the HTTP REST API listens on.
const apiAddr = ":8338"

// keyPath is the default location of the encrypted Ed25519 identity file.
const keyPath = "./node_keys/identity.json"

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
	fmt.Fprintf(os.Stderr, "  aethernet init                      generate a new node identity\n")
	fmt.Fprintf(os.Stderr, "  aethernet start                     start the node\n")
	fmt.Fprintf(os.Stderr, "  aethernet connect --peer <address>  start and connect to a peer\n")
	fmt.Fprintf(os.Stderr, "  aethernet status                    print node identity and config\n")
}

// readPassphrase prints prompt and reads one line from stdin, stripping the
// trailing newline. Uses bufio.NewReader so it works on any platform.
func readPassphrase(prompt string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(prompt)
	line, _ := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// loadKeyPair prompts for a passphrase and loads the keypair from keyPath.
// Exits with a structured log message on any error.
func loadKeyPair() *crypto.KeyPair {
	passphrase := readPassphrase("Passphrase: ")
	kp, err := crypto.LoadKeyPair(keyPath, passphrase)
	if err != nil {
		slog.Error("failed to load keypair", "path", keyPath, "err", err)
		os.Exit(1)
	}
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
// nodeStack. When s is non-nil, state is restored from the store and all
// subsequent mutations write through. When s is nil, all components start fresh
// (in-memory only). The OCS engine is constructed but not started; call
// engine.Start() before serving traffic.
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
// Uses fmt.Printf as specified — not slog — so it is easy to read at a glance.
func printStatus(agentID crypto.AgentID, d *dag.DAG, n *network.Node, eng *ocs.Engine, sm *ledger.SupplyManager) {
	ratio, _ := sm.SupplyRatio()
	// Abbreviate the 64-char AgentID for readability in the terminal.
	id := string(agentID)
	if len(id) > 16 {
		id = id[:16] + "..."
	}
	fmt.Printf("[%s]  peers=%-3d  dag=%-6d  ocs_pending=%-4d  supply=%.4fx\n",
		id, n.PeerCount(), d.Size(), eng.PendingCount(), ratio)
}

// startStack starts the OCS engine, network node, and HTTP API server.
// Returns the network node. On any error it stops whatever has started and exits.
func startStack(stack *nodeStack, agentID crypto.AgentID) *network.Node {
	if err := stack.engine.Start(); err != nil {
		slog.Error("failed to start OCS engine", "err", err)
		os.Exit(1)
	}

	cfg := network.DefaultNodeConfig(agentID)
	node := network.NewNode(cfg, stack.dag)
	if err := node.Start(); err != nil {
		slog.Error("failed to start network listener", "addr", cfg.ListenAddr, "err", err)
		stack.engine.Stop()
		os.Exit(1)
	}

	apiSrv := api.NewServer(
		apiAddr,
		stack.dag, stack.transfer, stack.generation,
		stack.reg, stack.engine, stack.supply,
		node, stack.kp,
	)
	if err := apiSrv.Start(); err != nil {
		slog.Error("failed to start API server", "addr", apiAddr, "err", err)
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

	// Print once immediately so the operator sees current state on startup.
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

// cmdInit generates a new Ed25519 keypair, saves it encrypted to keyPath,
// and prints the resulting AgentID.
func cmdInit() {
	if err := os.MkdirAll("./node_keys", 0o700); err != nil {
		slog.Error("failed to create key directory", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("./data", 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}

	// Guard against accidental overwrite of an existing identity.
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Fprintf(os.Stderr, "identity already exists at %s\nRemove it to reinitialise.\n", keyPath)
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
	if err := kp.Save(keyPath, passphrase); err != nil {
		slog.Error("failed to save keypair", "path", keyPath, "err", err)
		os.Exit(1)
	}

	agentID := kp.AgentID()
	fmt.Printf("Identity created.\nAgentID : %s\nKey file: %s\n", agentID, keyPath)
	slog.Info("node identity initialised", "agent_id", agentID)
}

// cmdStart loads the keypair, starts the OCS engine, network listener, and API
// server, then enters the status loop until the process receives SIGINT or SIGTERM.
func cmdStart() {
	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s, err := store.NewStore("./data/aethernet.db")
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}

	stack := buildStack(s, kp)
	node := startStack(stack, agentID)

	cfg := network.DefaultNodeConfig(agentID)
	fmt.Printf("AetherNet %s\nAgentID  : %s\nListening: %s\nAPI      : http://localhost%s\n\n",
		VERSION, agentID, cfg.ListenAddr, apiAddr)

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply)
	stopStack(node, stack)
	slog.Info("node stopped cleanly")
}

// cmdConnect loads the keypair, starts the node, dials the given peer address,
// then enters the status loop until the process is interrupted.
func cmdConnect() {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	peerAddr := fs.String("peer", "", "address of the peer to connect to (host:port)")
	_ = fs.Parse(os.Args[2:])

	if *peerAddr == "" {
		fmt.Fprintln(os.Stderr, "usage: aethernet connect --peer <host:port>")
		os.Exit(1)
	}

	kp := loadKeyPair()
	agentID := kp.AgentID()

	slog.Info("starting AetherNet node", "version", VERSION, "agent_id", agentID)

	s, err := store.NewStore("./data/aethernet.db")
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}

	stack := buildStack(s, kp)
	node := startStack(stack, agentID)

	cfg := network.DefaultNodeConfig(agentID)
	fmt.Printf("AetherNet %s\nAgentID  : %s\nListening: %s\nAPI      : http://localhost%s\n\n",
		VERSION, agentID, cfg.ListenAddr, apiAddr)

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
	cfg := network.DefaultNodeConfig(agentID)

	fmt.Printf("AetherNet %s\n", VERSION)
	fmt.Printf("AgentID    : %s\n", agentID)
	fmt.Printf("Listen addr: %s\n", cfg.ListenAddr)
	fmt.Printf("Max peers  : %d\n", cfg.MaxPeers)
	fmt.Printf("Sync every : %s\n", cfg.SyncInterval)
	fmt.Printf("Key file   : %s\n", keyPath)
}
