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
	"bytes"
	"context"
	"encoding/hex"
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
	"github.com/Aethernet-network/aethernet/internal/auth"
	"github.com/Aethernet-network/aethernet/internal/assurance"
	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/blobstore"
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
	"github.com/Aethernet-network/aethernet/internal/localpub"
	"github.com/Aethernet-network/aethernet/internal/metrics"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	platformpkg "github.com/Aethernet-network/aethernet/internal/platform"
	"github.com/Aethernet-network/aethernet/internal/protocol"
	"github.com/Aethernet-network/aethernet/internal/ratelimit"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/router"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/store"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/trajectory"
	"github.com/Aethernet-network/aethernet/internal/validator"
	"github.com/Aethernet-network/aethernet/internal/validatorlifecycle"
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
	case "validator-set":
		cmdValidatorSet()
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
	fmt.Fprintf(os.Stderr, "  aethernet validator-set [--manifest path] [--verify digest]\n")
	fmt.Fprintf(os.Stderr, "                                                  show or verify genesis validator set\n")
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
	validatorReg    *validator.ValidatorRegistry
	assignmentEng   *validator.AssignmentEngine
	slashEng        *validator.SlashEngine
	challengeMgr    *assurance.ChallengeManager
	bootstrapOvr    *assurance.BootstrapOverride
	replayReserve   *assurance.ReplayReserve
	votingRound     *consensus.VotingRound
	protoClient     *protocol.Client
	lifecycleReducer *validatorlifecycle.Reducer
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

// validatorCalibrationAdapter bridges *canary.CanaryManager (L3) to the
// validator package's calibrationSource interface (L2). Same pattern as
// routerCalibrationAdapter but targets validator.CalibrationData.
type validatorCalibrationAdapter struct {
	mgr *canary.CanaryManager
}

func (a *validatorCalibrationAdapter) CategoryCalibrationForActor(agentID, category string) (*validator.CalibrationData, error) {
	cal, err := a.mgr.CategoryCalibrationForActor(agentID, category)
	if err != nil || cal == nil {
		return nil, err
	}
	return &validator.CalibrationData{
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

// replayAssignerAdapter bridges *validator.AssignmentEngine (L2) to the
// replay.replayAssigner interface (L3) in cmd/node — where both layers are
// already imported. SelectReplayExecutor returns the selected validator's ID.
type replayAssignerAdapter struct {
	eng *validator.AssignmentEngine
}

func (a *replayAssignerAdapter) SelectReplayExecutor(category, originalVerifierID, originalVerifierCluster string) (string, error) {
	v, err := a.eng.SelectReplayExecutor(category, originalVerifierID, originalVerifierCluster)
	if err != nil {
		return "", err
	}
	return v.ID, nil
}

// slashEngineAdapter bridges *validator.SlashEngine (L2) to the
// replay.slashExecutor interface (L3). Translates offense string to
// validator.SlashOffense and extracts SlashAmount from the result.
type slashEngineAdapter struct {
	eng *validator.SlashEngine
}

func (a *slashEngineAdapter) Slash(validatorID string, offense string) (uint64, error) {
	result, err := a.eng.Slash(validatorID, validator.SlashOffense(offense))
	if err != nil {
		return 0, err
	}
	return result.SlashAmount, nil
}

// nodeNetworkState bridges *validator.ValidatorRegistry, *assurance.BootstrapOverride,
// reducerCommitteeSource adapts the lifecycle Reducer into the consensus
// CommitteeSource interface. It reads the current snapshot from the Reducer
// on each call, so committee selection always reflects the latest validator
// set state (including post-genesis lifecycle events).
type reducerCommitteeSource struct {
	reducer *validatorlifecycle.Reducer
}

func (s *reducerCommitteeSource) SelectForRound(eventID event.EventID) map[crypto.AgentID]bool {
	snap := s.reducer.Snapshot()
	cs := snap.SelectCommittee(string(eventID))
	members := make(map[crypto.AgentID]bool, len(cs.Members))
	for _, seatID := range cs.Members {
		seat, ok := snap.Seats[seatID]
		if ok {
			members[seat.OperatorKey] = true
		}
	}
	return members
}

// applyLifecycleEventFromSync extracts lifecycle events from a DAG event and
// applies them to the Reducer. After a successful apply, the VotingRound's
// snapshot is rebound to reflect the updated validator set. Errors from
// extraction or application are logged and the event is skipped — a single
// invalid lifecycle event must not crash the node.
func applyLifecycleEventFromSync(ev *event.Event, reducer *validatorlifecycle.Reducer, vr *consensus.VotingRound) {
	if reducer == nil {
		return
	}
	lcEvents, err := validatorlifecycle.ExtractLifecycleEvent(ev)
	if err != nil {
		slog.Warn("validator lifecycle: failed to extract lifecycle event",
			"event_id", ev.ID, "type", ev.Type, "err", err)
		return
	}
	applied := false
	for _, lc := range lcEvents {
		if err := reducer.Apply(lc); err != nil {
			slog.Warn("validator lifecycle: failed to apply lifecycle event",
				"event_id", ev.ID, "kind", lc.Kind, "seat_id", lc.SeatID, "err", err)
			continue
		}
		applied = true
		slog.Info("validator lifecycle: applied",
			"kind", lc.Kind,
			"seat_id", lc.SeatID,
			"event_id", ev.ID,
			"version", reducer.Version(),
		)
	}
	// Rebind the VotingRound's snapshot if any lifecycle events were applied.
	if applied && vr != nil {
		vr.SetValidatorSet(reducer.Snapshot())
	}
}

// replayLifecycleEventsFromDAG iterates all events in the loaded DAG and
// applies any validator lifecycle events to the Reducer. This reconstructs
// the full validator set state from the DAG history, not just genesis.
//
// Events are sorted by CausalTimestamp for deterministic ordering. The
// Reducer's Apply function uses EffectiveFromVersion for temporal correctness,
// so processing order within the same timestamp does not affect the final
// snapshot (version increments monotonically regardless of intra-timestamp order).
func replayLifecycleEventsFromDAG(d *dag.DAG, reducer *validatorlifecycle.Reducer) int {
	if d == nil || reducer == nil {
		return 0
	}
	allEvents := d.All()
	if len(allEvents) == 0 {
		return 0
	}

	// Sort by CausalTimestamp for deterministic replay order.
	sortEventsByCausalTS(allEvents)

	applied := 0
	for _, ev := range allEvents {
		if !isLifecycleEventType(ev.Type) {
			continue
		}
		lcEvents, err := validatorlifecycle.ExtractLifecycleEvent(ev)
		if err != nil {
			slog.Debug("validator lifecycle: skip invalid event during replay",
				"event_id", ev.ID, "type", ev.Type, "err", err)
			continue
		}
		for _, lc := range lcEvents {
			if err := reducer.Apply(lc); err != nil {
				slog.Debug("validator lifecycle: skip failed apply during replay",
					"event_id", ev.ID, "kind", lc.Kind, "seat_id", lc.SeatID, "err", err)
				continue
			}
			applied++
		}
	}
	return applied
}

// isLifecycleEventType returns true if the event type is a validator lifecycle
// event that should be routed to the Reducer.
func isLifecycleEventType(t event.EventType) bool {
	switch t {
	case event.EventTypeValidatorGenesisSet,
		event.EventTypeValidatorJoin,
		event.EventTypeValidatorActivate,
		event.EventTypeValidatorSuspend,
		event.EventTypeValidatorResume,
		event.EventTypeValidatorExit,
		event.EventTypeValidatorKeyRotate,
		event.EventTypeValidatorSlashApplied:
		return true
	default:
		return false
	}
}

// sortEventsByCausalTS sorts events by CausalTimestamp ascending, with EventID
// as a tiebreaker for determinism.
func sortEventsByCausalTS(events []*event.Event) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0; j-- {
			if events[j].CausalTimestamp < events[j-1].CausalTimestamp {
				events[j], events[j-1] = events[j-1], events[j]
			} else if events[j].CausalTimestamp == events[j-1].CausalTimestamp &&
				string(events[j].ID) < string(events[j-1].ID) {
				events[j], events[j-1] = events[j-1], events[j]
			} else {
				break
			}
		}
	}
}

// and *assurance.ReplayReserve into the api.networkStateSource interface. It is
// constructed once in startStack and wired via apiSrv.SetNetworkStateSource.
type nodeNetworkState struct {
	validatorReg  *validator.ValidatorRegistry
	bootstrapOvr  *assurance.BootstrapOverride
	replayReserve *assurance.ReplayReserve
	categories    []string
}

func (n *nodeNetworkState) IsBootstrapActive() bool {
	if n.bootstrapOvr == nil {
		return false
	}
	return n.bootstrapOvr.IsBootstrapActive()
}

func (n *nodeNetworkState) ActiveValidatorCount() int {
	if n.validatorReg == nil {
		return 0
	}
	return n.validatorReg.ActiveEligibleCount()
}

func (n *nodeNetworkState) ValidatorCountForCategory(category string) int {
	if n.validatorReg == nil {
		return 0
	}
	return n.validatorReg.ActiveCountForCategory(category)
}

func (n *nodeNetworkState) IsReplayReserveHealthy(category string) bool {
	if n.replayReserve == nil {
		return true // optimistic when no reserve is configured
	}
	return n.replayReserve.CategoryHealthy(category)
}

func (n *nodeNetworkState) AssuranceCategories() []string {
	return n.categories
}

// challengeManagerAdapter bridges *assurance.ChallengeManager (L3) to the
// api.challengeSource interface (also L3). The api package uses primitive
// return types to avoid an assurance import; this adapter converts between
// the two representations.
type challengeManagerAdapter struct {
	mgr *assurance.ChallengeManager
}

func (a *challengeManagerAdapter) OpenChallenge(taskID, challengerID, targetID string, bond uint64) (string, string, error) {
	c, err := a.mgr.OpenChallenge(taskID, challengerID, targetID, bond)
	if err != nil {
		return "", "", err
	}
	return c.ID, c.CreatedAt.Format(time.RFC3339), nil
}

func (a *challengeManagerAdapter) ResolveChallenge(challengeID string, outcome string, fraudBounty uint64) (uint64, uint64, error) {
	res, err := a.mgr.ResolveChallenge(challengeID, assurance.ChallengeStatus(outcome), fraudBounty)
	if err != nil {
		return 0, 0, err
	}
	return res.RefundedBond, res.ForfeitAmount, nil
}

func (a *challengeManagerAdapter) ChallengesForTask(taskID string) []api.ChallengeRecord {
	challenges := a.mgr.ChallengesForTask(taskID)
	out := make([]api.ChallengeRecord, 0, len(challenges))
	for _, c := range challenges {
		rec := api.ChallengeRecord{
			ID:           c.ID,
			TaskID:       c.TaskID,
			ChallengerID: c.ChallengerID,
			TargetID:     c.TargetID,
			Bond:         c.Bond,
			Status:       string(c.Status),
			CreatedAt:    c.CreatedAt.Format(time.RFC3339),
		}
		if !c.ResolvedAt.IsZero() {
			rec.ResolvedAt = c.ResolvedAt.Format(time.RFC3339)
		}
		out = append(out, rec)
	}
	return out
}

func (a *challengeManagerAdapter) GetChallenge(id string) (api.ChallengeRecord, error) {
	c, err := a.mgr.GetChallenge(id)
	if err != nil {
		return api.ChallengeRecord{}, err
	}
	rec := api.ChallengeRecord{
		ID:           c.ID,
		TaskID:       c.TaskID,
		ChallengerID: c.ChallengerID,
		TargetID:     c.TargetID,
		Bond:         c.Bond,
		Status:       string(c.Status),
		CreatedAt:    c.CreatedAt.Format(time.RFC3339),
	}
	if !c.ResolvedAt.IsZero() {
		rec.ResolvedAt = c.ResolvedAt.Format(time.RFC3339)
	}
	return rec, nil
}

func (a *challengeManagerAdapter) MinBond(taskBudget uint64) uint64 {
	return a.mgr.MinBond(taskBudget)
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
	minParticipants := 1
	if mpStr := os.Getenv("AETHERNET_CONSENSUS_MIN_PARTICIPANTS"); mpStr != "" {
		if mp, err := strconv.Atoi(mpStr); err == nil && mp > 0 {
			minParticipants = mp
		}
	}
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           30 * time.Second,
		MinParticipants:        minParticipants,
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

	// Testnet: register a shared well-known API key on every node so requests
	// behind the ALB are accepted regardless of which node serves them.
	if os.Getenv("AETHERNET_TESTNET") == "true" {
		platformKeys.RegisterKnownKey(
			"aethernet-testnet-arena-key-v1",
			"arena-testnet",
			"arena@aethernet.network",
			platformpkg.TierFree,
		)
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

	// Wire assurance lane fee schedule into the task manager so PostTask can
	// compute AssuranceFee and WorkerNetPayout for assured tasks. The security
	// floor is not wired here — no live validator state exists at boot time;
	// operators wire it externally once the validator roster is populated.
	taskMgr.SetAssuranceConfig(&cfg.Assurance)

	// Apply staking decay configuration.
	staking.SetDecayParams(cfg.Staking.DecayPeriodDays, cfg.Staking.DecayTasksPenalty)

	// Apply fee distribution configuration.
	feeCollector.SetFeeParams(cfg.Fees.FeeBasisPoints, cfg.Fees.FeeValidatorShare, cfg.Fees.FeeTreasuryShare)

	// Validator registry: permissionless entry, dynamic stake, probation lifecycle.
	// LoadFromStore restores any previously registered validators on restart.
	var validatorReg *validator.ValidatorRegistry
	if s != nil {
		var loadErr error
		validatorReg, loadErr = validator.LoadFromStore(&cfg.Validator, s)
		if loadErr != nil {
			slog.Error("failed to load validator registry from store", "err", loadErr)
			validatorReg = validator.NewValidatorRegistry(&cfg.Validator, s)
		}
	} else {
		validatorReg = validator.NewValidatorRegistry(&cfg.Validator, nil)
	}

	// Assignment engine: weighted validator selection with calibration and
	// probation modifiers, hard caps, and affiliated-cluster handling.
	// Calibration source is wired in startStack once the canary manager is ready.
	assignmentEng := validator.NewAssignmentEngine(validatorReg, &cfg.Validator)

	// Slash engine: applies stake penalties and cooldown suspensions for
	// protocol violations (fraudulent approval, dishonest replay, collusion).
	slashEng := validator.NewSlashEngine(validatorReg, &cfg.Validator)

	// Challenge manager: tracks challenge bonds posted against validators.
	var challengeMgr *assurance.ChallengeManager
	var chalLoadErr error
	if s != nil {
		challengeMgr, chalLoadErr = assurance.LoadChallengesFromStore(&cfg.Assurance, s)
		if chalLoadErr != nil {
			slog.Error("failed to load challenge records from store", "err", chalLoadErr)
			challengeMgr = assurance.NewChallengeManager(&cfg.Assurance, s)
		}
	} else {
		challengeMgr = assurance.NewChallengeManager(&cfg.Assurance, nil)
	}

	// Bootstrap override: determines elevated replay rates and reward
	// supplements during network launch phase. Launch time is persisted so
	// it survives node restarts.
	launchTime := time.Now()
	if s != nil {
		if data, metaErr := s.GetMeta("launch_time"); metaErr == nil && len(data) > 0 {
			if nanos, parseErr := strconv.ParseInt(string(data), 10, 64); parseErr == nil {
				launchTime = time.Unix(0, nanos)
			}
		} else {
			// First boot: persist the launch time.
			if putErr := s.PutMeta("launch_time", []byte(strconv.FormatInt(launchTime.UnixNano(), 10))); putErr != nil {
				slog.Error("failed to persist launch_time", "err", putErr)
			}
		}
	}
	normalReplRates := assurance.BootstrapRates{
		BaselineReplay:   0.20,
		GenerationReplay: 0.35,
		NewAgentReplay:   0.50,
	}
	bootstrapOvr := assurance.NewBootstrapOverride(&cfg.Assurance, validatorReg, launchTime, normalReplRates)

	// ReplayReserve: per-category pool that funds replay-executor minimum payouts.
	// Persisted to store; balances survive node restarts.
	replayReserve := assurance.NewReplayReserve(&cfg.Assurance, s)

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
		validatorReg:    validatorReg,
		assignmentEng:   assignmentEng,
		slashEng:        slashEng,
		challengeMgr:    challengeMgr,
		bootstrapOvr:    bootstrapOvr,
		replayReserve:   replayReserve,
		votingRound:     votingRound,
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

	// ── Validator Lifecycle: seed genesis snapshot ──────────────────────────
	// The lifecycle Reducer must be seeded before the OCS engine, auto-
	// validator, or networking start so that snapshot version 1 exists when
	// consensus-consuming code begins. All nodes load the same manifest
	// and produce the same snapshot digest.
	//
	// Startup safety: the node fails closed if the manifest is invalid or
	// the snapshot cannot be seeded. Consensus services never start without
	// a valid snapshot.
	//
	// Manifest loading priority:
	//   1. AETHERNET_VALIDATOR_MANIFEST — path to a JSON manifest file
	//      containing the actual Ed25519 public keys of all validator nodes.
	//      Required for multi-node testnets where each node has a persistent
	//      keypair that must match the validator seat's operator key.
	//   2. Auto-manifest from node's own key — when no manifest file is
	//      specified and AETHERNET_TESTNET=true, a single-seat manifest is
	//      generated from this node's actual keypair. This ensures the
	//      auto-validator can vote on its own events (single-node dev mode).
	//   3. DefaultTestnetManifest() — fallback using the symbolic
	//      "testnet-validator" identity. Only appropriate when the validator
	//      lifecycle snapshot is not used for vote eligibility.
	var genesisManifest *validatorlifecycle.GenesisManifest
	if manifestPath := os.Getenv("AETHERNET_VALIDATOR_MANIFEST"); manifestPath != "" {
		var err error
		genesisManifest, err = validatorlifecycle.LoadManifestFromFile(manifestPath)
		if err != nil {
			slog.Error("validator lifecycle: failed to load manifest file",
				"path", manifestPath, "err", err)
			os.Exit(1)
		}
		slog.Info("validator lifecycle: loaded manifest from file",
			"path", manifestPath, "seats", len(genesisManifest.Entries))
	} else {
		// Auto-manifest: use this node's actual keypair as the sole
		// validator seat. This ensures vote eligibility works in
		// single-node testnet / dev mode without a shared manifest.
		genesisManifest = validatorlifecycle.SingleNodeManifest(agentID)
		slog.Info("validator lifecycle: using auto-manifest from node key",
			"agent_id", agentID)
	}
	startupCheck := validatorlifecycle.DefaultStartupCheck()
	if err := startupCheck.ValidateManifest(genesisManifest); err != nil {
		slog.Error("validator lifecycle: manifest validation failed", "err", err)
		os.Exit(1)
	}
	lifecycleReducer, err := validatorlifecycle.SeedReducerFromManifest(genesisManifest)
	if err != nil {
		slog.Error("validator lifecycle: failed to seed genesis manifest", "err", err)
		os.Exit(1)
	}
	if err := startupCheck.ValidateReducer(lifecycleReducer); err != nil {
		slog.Error("validator lifecycle: startup check failed — consensus cannot start", "err", err)
		os.Exit(1)
	}
	stack.lifecycleReducer = lifecycleReducer

	// Replay any validator lifecycle events from the loaded DAG to reconstruct
	// the full validator set state from history, not just genesis. This ensures
	// a restarted node's Reducer matches the state of a node that processed
	// the events live.
	replayedCount := replayLifecycleEventsFromDAG(stack.dag, lifecycleReducer)
	if replayedCount > 0 {
		slog.Info("validator lifecycle: replayed lifecycle events from DAG",
			"replayed", replayedCount, "version", lifecycleReducer.Version())
	}
	validatorlifecycle.LogSnapshotDigest(lifecycleReducer)

	// Bind the validator snapshot to the VotingRound only when a shared
	// manifest was loaded (AETHERNET_VALIDATOR_MANIFEST). With a shared
	// manifest, all nodes have the same snapshot containing all validator
	// keys, so votes are correctly accepted across nodes.
	//
	// When using the auto-generated SingleNodeManifest (no shared manifest),
	// do NOT bind the snapshot. The auto-manifest contains only this node's
	// own key, which means peer votes would be rejected as "not in snapshot"
	// because peer keys are not in the local snapshot. Without binding, the
	// VotingRound falls back to the identity registry which contains all
	// registered peers, allowing consensus to finalize.
	manifestFromFile := os.Getenv("AETHERNET_VALIDATOR_MANIFEST") != ""
	if stack.votingRound != nil && manifestFromFile {
		stack.votingRound.SetValidatorSet(lifecycleReducer.Snapshot())
		stack.votingRound.SetCommitteeSource(&reducerCommitteeSource{reducer: lifecycleReducer})
		slog.Info("validator lifecycle: snapshot bound to VotingRound (shared manifest)")
	} else if stack.votingRound != nil {
		slog.Info("validator lifecycle: VotingRound using identity registry fallback (no shared manifest)")
	}

	// Create the authoritative local event publisher. Started with nil
	// disseminator — startup events (genesis funding, registration) are
	// persisted in the DAG but not broadcast. After node.Start(), the
	// disseminator is wired and future events are broadcast immediately.
	// Pre-networking events are broadcast by broadcastLocalEvents after
	// peer connections are established.
	pub := localpub.New(stack.dag, nil)

	if err := stack.engine.Start(); err != nil {
		slog.Error("failed to start OCS engine", "err", err)
		os.Exit(1)
	}

	// Protocol client: canonical interface for all token movement.
	stack.protoClient = protocol.NewClient(stack.dag, stack.kp, stack.engine, agentID)
	stack.protoClient.SetPublisher(pub)

	// Auto-validator: on testnet, automatically settle pending OCS transactions.
	// The "testnet-validator" agent is registered in the identity registry so
	// it appears in the explorer as a known participant.
	// ── Genesis validator setup ─────────────────────────────────────────────
	// The testnet-validator is funded in seedGenesis (deterministic, every node).
	// Here we register it in the identity registry and stake it. Staking is
	// in-memory consensus data — it must happen on every node identically.
	testnetValidatorID := crypto.AgentID(genesis.GenesisValidatorID)
	tvFP, err := identity.NewFingerprint(testnetValidatorID, make([]byte, 32), nil)
	if err == nil {
		tvFP.ReputationScore = 5000
		tvFP.StakedAmount = genesis.GenesisValidatorStake
		_ = stack.reg.Register(tvFP)
	}

	// Stake the genesis validator (idempotent: skips if already staked).
	if stack.stakeManager.StakedAmount(testnetValidatorID) == 0 {
		if err := stack.stakeManager.Stake(testnetValidatorID, genesis.GenesisValidatorStake); err != nil {
			slog.Warn("startStack: failed to stake genesis validator", "err", err)
		}
	}

	// Register in ValidatorRegistry as genesis participant (skips probation).
	if stack.validatorReg != nil {
		if _, lookupErr := stack.validatorReg.GetByAgentID(string(testnetValidatorID)); lookupErr != nil {
			if _, regErr := stack.validatorReg.Register(
				string(testnetValidatorID),
				genesis.GenesisValidatorStake,
				nil, true,
			); regErr != nil {
				slog.Warn("startStack: failed to register genesis validator in ValidatorRegistry", "err", regErr)
			}
		}
	}

	tvBal, _ := stack.transfer.Balance(testnetValidatorID)
	slog.Info("startStack: genesis validator ready",
		"balance", tvBal,
		"staked", stack.stakeManager.StakedAmount(testnetValidatorID),
	)

	// Emit canonical GenesisFunding DAG events for post-mint transfers
	// (validator bootstrap, faucet pool). These are auditable — new nodes
	// replay them from the DAG. Idempotent: skips if already emitted.
	emitGenesisTransfers(stack.dag, pub, stack.kp, agentID, stack.transfer, stack.store)

	// ── Node agent setup: fund, stake, register ────────────────────────────
	// Each node's own agentID needs funds (for transfers), stake (for OCS
	// MinStakeRequired), and identity registry entry (for vote weight).
	const (
		nodeAgentFundTarget = uint64(50_000_000_000)  // 50,000 AET
		nodeAgentMinBalance = uint64(1_000_000_000)   // 1,000 AET top-up threshold
		nodeAgentStakeAmt   = uint64(25_000_000_000)  // 25,000 AET stake
	)

	// 1. Fund via canonical GenesisFunding DAG event (idempotent: skips if
	// above threshold). The event propagates to all peers via DAG sync and
	// is applied deterministically without consensus — same as Registration.
	nodeAgentBal, _ := stack.transfer.Balance(agentID)
	if nodeAgentBal < nodeAgentMinBalance {
		gfPayload := event.GenesisFundingPayload{
			Version:    1,
			FromBucket: genesis.BucketRewards,
			ToAgent:    string(agentID),
			Amount:     nodeAgentFundTarget,
			Reason:     "node-bootstrap",
		}
		tips := stack.dag.Tips()
		priorTS := make(map[event.EventID]uint64, len(tips))
		for _, ref := range tips {
			if te, err := stack.dag.Get(ref); err == nil {
				priorTS[ref] = te.CausalTimestamp
			}
		}
		gfEv, err := event.New(
			event.EventTypeGenesisFunding, tips, gfPayload,
			string(agentID), priorTS, 0,
		)
		if err == nil {
			_ = crypto.SignEvent(gfEv, stack.kp)
			if pubErr := pub.Publish(gfEv); pubErr == nil {
				// Apply locally immediately (peers apply via sync handler).
				if err := stack.transfer.TransferFromBucket(
					crypto.AgentID(genesis.BucketRewards), agentID, nodeAgentFundTarget,
				); err != nil {
					slog.Warn("startStack: genesis funding local apply failed", "err", err)
				} else {
					if stack.store != nil {
						_ = stack.store.PutMeta("genesis-funding:"+string(gfEv.ID), []byte("1"))
					}
					slog.Info("startStack: genesis funding applied",
						"agent_id", agentID, "amount", nodeAgentFundTarget)
				}
			}
		}
	}

	// 2. Stake (idempotent: skips if already staked).
	nodeStakedBefore := stack.stakeManager.StakedAmount(agentID)
	if nodeStakedBefore == 0 {
		if err := stack.stakeManager.Stake(agentID, nodeAgentStakeAmt); err != nil {
			slog.Warn("startStack: failed to stake node agentID", "err", err, "agent_id", agentID)
		} else {
			slog.Info("startStack: staked node agentID", "agent_id", agentID, "amount", nodeAgentStakeAmt)
		}
	}

	// 3. Register in identity registry with real stake for vote weight.
	actualStake := stack.stakeManager.StakedAmount(agentID)
	if nodeFP, err := identity.NewFingerprint(agentID, stack.kp.PublicKey, nil); err == nil {
		nodeFP.ReputationScore = 5000
		nodeFP.StakedAmount = actualStake
		if regErr := stack.reg.Register(nodeFP); regErr != nil {
			if existing, lookupErr := stack.reg.Get(agentID); lookupErr == nil {
				existing.ReputationScore = 5000
				existing.StakedAmount = actualStake
			}
		}
	}

	// 4. Register in ValidatorRegistry as genesis participant.
	if stack.validatorReg != nil {
		if _, lookupErr := stack.validatorReg.GetByAgentID(string(agentID)); lookupErr != nil {
			if _, regErr := stack.validatorReg.Register(
				string(agentID), nodeAgentStakeAmt, nil, true,
			); regErr != nil {
				slog.Warn("startStack: failed to register node agent in ValidatorRegistry", "err", regErr)
			}
		}
	}

	nodeAgentFinalBal, _ := stack.transfer.Balance(agentID)
	slog.Info("startStack: node agent ready",
		"agent_id", agentID,
		"balance", nodeAgentFinalBal,
		"staked", stack.stakeManager.StakedAmount(agentID),
	)

	// Create a Registration event in the DAG so this node's identity
	// propagates to all peers via P2P sync.
	regPayload := event.RegistrationPayload{
		Version:         1,
		AgentID:         string(agentID),
		PublicKey:       hex.EncodeToString(stack.kp.PublicKey),
		ReputationScore: 5000,
		StakedAmount:    actualStake,
	}
	if regEv, err := event.New(event.EventTypeRegistration, stack.dag.Tips(), regPayload, string(agentID), nil, 0); err == nil {
		_ = crypto.SignEvent(regEv, stack.kp)
		if pubErr := pub.Publish(regEv); pubErr == nil {
			slog.Info("startStack: registration event published", "agent_id", agentID)
		}
	}

	// SecurityFloor: enforce minimum validator coverage before accepting assured tasks.
	// The floor checks both validator count (from ValidatorRegistry) and replay-reserve
	// health (circuit-breaker). Coverage counts are refreshed every 10 s in the metrics
	// goroutine; the initial population happens here so the first tasks are not
	// spuriously rejected due to a zero count.
	secFloor := assurance.NewSecurityFloor(&cfg.Assurance)
	secFloor.SetReplayReserve(stack.replayReserve)
	if stack.validatorReg != nil {
		for _, cat := range cfg.Assurance.StructuredCategories {
			count := stack.validatorReg.ActiveCountForCategory(cat)
			secFloor.SetState(assurance.CategorySecurityState{
				Category:       cat,
				ValidatorCount: float64(count),
			})
		}
	}
	stack.taskMgr.SetSecurityFloor(secFloor)

	// Variables used by both the auto-validator block and the API wiring below.
	// Declared here so they remain in scope; nil when auto-validator is disabled.
	var replayEnforcer *replay.ReplayEnforcer
	var submissionProc *replay.SubmissionProcessor
	var canaryMgr *canary.CanaryManager

	// AETHERNET_AUTOVALIDATOR controls whether the auto-validator starts.
	// Default: "true" (backward compatible). Set to "false" to run a
	// passive node that participates in P2P, DAG sync, and API serving
	// but does not settle events locally.
	if os.Getenv("AETHERNET_AUTOVALIDATOR") == "false" {
		slog.Info("auto-validator disabled (AETHERNET_AUTOVALIDATOR=false)")
	} else {
	// Use the node's own unique agentID as the voter identity — NOT the shared
	// testnetValidatorID. Each node must vote with a distinct identity so the
	// VotingRound sees them as separate voters.
	av := autovalidator.NewAutoValidator(stack.engine, agentID, 5*time.Second)
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
	if stack.store != nil {
		replayCoord := replay.NewReplayCoordinator(replay.DefaultReplayPolicy(), stack.store)
		// Bootstrap overlay: replaces the hardcoded policy sample rates with the
		// lifecycle-aware rates from BootstrapOverride. Elevated rates (40/50/75%)
		// during bootstrap phase; normal rates (20/35/50%) once both exit conditions
		// (duration + validator count) are met.
		replayCoord.SetBootstrapRateSource(stack.bootstrapOvr)
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
		// Wire assignment engine so each replay job is assigned to an
		// independent validator before the InspectionExecutor processes it.
		if stack.assignmentEng != nil {
			stack.replayRunner.SetReplayAssigner(&replayAssignerAdapter{eng: stack.assignmentEng})
		}
		// Wire slash engine so "slash_recommended" verdicts apply an economic
		// penalty to the original worker immediately.
		if stack.slashEng != nil {
			replayEnforcer.SetSlashExecutor(&slashEngineAdapter{eng: stack.slashEng})
		}
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
		// Wire calibration into the assignment engine so it can apply per-
		// category accuracy modifiers when selecting validators.
		if stack.assignmentEng != nil {
			stack.assignmentEng.SetCalibrationSource(&validatorCalibrationAdapter{mgr: canaryMgr})
		}
	}

	// ReplayReserve: accrue a fraction of each assured task's assurance fee into
	// the per-category pool that funds replay-executor minimum payouts.
	av.SetReplayReserve(stack.replayReserve, cfg.Assurance.ReplayReserveShare)

	// Task marketplace integration is conditional on --marketplace flag.
	if enableMarketplace {
		av.SetTaskManager(stack.taskMgr, stack.escrowMgr)
		av.SetReputationManager(stack.reputationMgr)
	}
	// NOTE: av.Start() is deferred until AFTER SetFinalizationHandler is wired.
	// If the auto-validator starts before the handler, it can vote and trigger
	// finalization while onFinalized is nil — causing ProcessResult to clear
	// pending without creating a Settlement event. This was the root cause of
	// "OCS pending clears but balance stays 0."
	stack.autoVal = av
	} // end AETHERNET_AUTOVALIDATOR gate

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
	if stack.lifecycleReducer != nil {
		nodeCfg.ManifestDigest = stack.lifecycleReducer.Snapshot().ComputeDigest()
	}
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

	// Wire the network disseminator onto the publisher. Before this point,
	// startup events (genesis funding, registration) were published to the
	// DAG only. After this, all new events are broadcast to peers immediately.
	pub.SetDisseminator(node)
	if stack.autoVal != nil {
		stack.autoVal.SetPublisher(pub)
	}

	// ── Settlement Applicator ────────────────────────────────────────────────
	// The ONLY component that mutates ledgers in response to consensus.
	settlementApp := settlement.NewApplicator(
		stack.transfer, stack.generation, stack.reg,
		func(id event.EventID) (*event.Event, error) {
			return stack.dag.Get(id)
		},
	)
	if stack.bus != nil {
		settlementApp.SetEventBus(stack.bus)
	}
	if stack.store != nil {
		settlementApp.SetStore(stack.store)
	}
	settlementApp.SetFeeCollector(stack.feeCollector, crypto.AgentID(genesis.BucketTreasury))
	settlementApp.SetStakeManager(stack.stakeManager)
	settlementApp.SetEscrowManager(stack.escrowMgr)
	settlementApp.SetTaskSettler(func(payload settlement.TaskSettlementPayload) error {
		if payload.ClaimerID == "" {
			return nil // no claimer — nothing to release
		}
		posterID := crypto.AgentID(payload.PosterID)
		claimerID := crypto.AgentID(payload.ClaimerID)
		treasuryID := crypto.AgentID(genesis.BucketTreasury)

		// Ensure escrow exists on this node. On the posting node, escrow was
		// locked via canonical Transfer (prompt 2). On peer nodes, the
		// SettlementApplicator's applyTransfer registered the escrow entry
		// when the escrow-lock transfer settled. If for any reason the escrow
		// doesn't exist yet (e.g. deferred settlement), create it now.
		if !stack.escrowMgr.IsLocked(payload.TaskID) {
			if err := stack.escrowMgr.Hold(payload.TaskID, posterID, payload.Budget); err != nil {
				slog.Warn("task-settler: escrow catch-up hold failed",
					"task_id", payload.TaskID, "err", err)
				return err
			}
		}

		// Calculate fee splits.
		fee := fees.CalculateFee(payload.Budget)
		netAmount := payload.Budget - fee
		validatorAmount := fee * fees.ValidatorShare / 100
		treasuryAmount := fee * fees.TreasuryShare / 100
		burned := fee - validatorAmount - treasuryAmount

		// Release escrow with canonical fee distribution.
		if err := stack.escrowMgr.ReleaseNet(
			payload.TaskID,
			claimerID, netAmount,
			crypto.AgentID(""), validatorAmount,
			treasuryID, treasuryAmount,
		); err != nil {
			return fmt.Errorf("task-settler: escrow release: %w", err)
		}

		// Track fee stats.
		if stack.feeCollector != nil && fee > 0 {
			stack.feeCollector.TrackFee(fee, burned, treasuryAmount)
		}

		slog.Info("task-settler: escrow released",
			"task_id", payload.TaskID,
			"worker", payload.ClaimerID,
			"net", netAmount,
			"fee", fee)
		return nil
	})
	if stack.nodeMetrics != nil {
		settlementApp.SetMetrics(stack.nodeMetrics)
	}
	settlementApp.Start()
	defer settlementApp.Stop()

	// ── Consensus finalization handler ───────────────────────────────────────
	// When processVoteInternal detects supermajority, this handler fires to
	// create the Settlement DAG event and apply it. Settlement creation is
	// now inevitable on finalization — it does not depend on a later sync
	// handler noticing that finalization happened.
	stack.engine.SetFinalizationHandler(func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64) {
		slog.Warn("settlement: finalization handler invoked",
			"target_id", targetID,
			"verdict", verdict,
			"verified_value", verifiedValue,
			"final_order", finalOrder,
		)

		// Idempotency: skip if already applied.
		if settlementApp.IsApplied(targetID) {
			return
		}

		rec, err := stack.votingRound.GetRecord(targetID)
		if err != nil {
			return
		}

		consensusVerdict := settlement.VerdictAccepted
		if !verdict {
			consensusVerdict = settlement.VerdictRejected
		}

		// Build attestations from vote record.
		var attestations []settlement.VoterAttestation
		for voterKey, vote := range rec.Votes {
			v := settlement.VerdictAccepted
			if !vote {
				v = settlement.VerdictRejected
			}
			attestations = append(attestations, settlement.VoterAttestation{
				VoterID: string(voterKey),
				Verdict: string(v),
			})
		}
		sp := settlement.SettlementPayload{
			Version:        1,
			TargetEventID:  string(targetID),
			Verdict:        string(consensusVerdict),
			VerifiedValue:  verifiedValue,
			ConsensusRound: finalOrder,
			Attestations:   attestations,
		}
		sp.SortAttestations()

		// Create the Settlement DAG event.
		tips := stack.dag.Tips()
		priorTS := make(map[event.EventID]uint64, len(tips))
		for _, ref := range tips {
			if te, err := stack.dag.Get(ref); err == nil {
				priorTS[ref] = te.CausalTimestamp
			}
		}
		settlementEv, err := event.New(
			event.EventTypeSettlement, tips, sp,
			string(agentID), priorTS, 0,
		)
		if err != nil {
			slog.Error("settlement: failed to create settlement event",
				"target", targetID, "err", err)
			return
		}
		if stack.kp != nil {
			_ = crypto.SignEvent(settlementEv, stack.kp)
		}
		if err := pub.Publish(settlementEv); err != nil {
			return // duplicate — another node already created one
		}

		slog.Info("settlement: consensus finalized, settlement event created",
			"target", targetID, "verdict", consensusVerdict,
			"settlement_id", settlementEv.ID)

		// Apply immediately on the creating node.
		_ = settlementApp.Apply(&sp)
	})

	// ── DAG sync handler ────────────────────────────────────────────────────
	// Route events by type. VerificationVotes feed into VotingRound (the
	// finalization handler creates settlements). Settlements feed into the
	// Applicator. Transfer/Generation/TaskSettlement enter the OCS pending
	// queue. Registration updates identity.
	node.SetSyncHandler(func(ev *event.Event) {
		switch ev.Type {
		case event.EventTypeTransfer, event.EventTypeGeneration, event.EventTypeTaskSettlement:
			_ = stack.engine.SubmitFromSync(ev)

		case event.EventTypeVerificationVote:
			// Route the vote through the OCS engine so it reaches the
			// finalization handler. AcceptPeerVote → processVoteInternal →
			// if finalized: onFinalized callback fires → creates Settlement
			// event + calls Apply. This ensures settlement creation is
			// inevitable regardless of which path (MsgVote or DAG sync)
			// delivers the finalizing vote.
			vp, err := event.GetPayload[settlement.VerificationVotePayload](ev)
			if err != nil {
				return
			}
			_ = stack.engine.AcceptPeerVote(
				event.EventID(vp.TargetEventID),
				crypto.AgentID(vp.VoterID),
				vp.Verdict == string(settlement.VerdictAccepted),
			)

		case event.EventTypeSettlement:
			sp, err := event.GetPayload[settlement.SettlementPayload](ev)
			if err != nil {
				return
			}
			_ = settlementApp.Apply(&sp)

		case event.EventTypeRegistration:
			rp, err := event.GetPayload[event.RegistrationPayload](ev)
			if err != nil {
				return
			}
			id := crypto.AgentID(rp.AgentID)
			pubKeyBytes, err := hex.DecodeString(rp.PublicKey)
			if err != nil {
				return
			}
			fp, err := identity.NewFingerprint(id, pubKeyBytes, nil)
			if err != nil {
				return
			}
			fp.ReputationScore = rp.ReputationScore
			fp.StakedAmount = rp.StakedAmount
			if regErr := stack.reg.Register(fp); regErr != nil {
				if existing, lookupErr := stack.reg.Get(id); lookupErr == nil {
					existing.ReputationScore = rp.ReputationScore
					existing.StakedAmount = rp.StakedAmount
				}
			}
			// Stake the remote validator in the local StakeManager so the
			// explorer shows consistent staking data across all nodes.
			if rp.StakedAmount > 0 && stack.stakeManager.StakedAmount(id) == 0 {
				_ = stack.stakeManager.Stake(id, rp.StakedAmount)
			}
			// Register in ValidatorRegistry for assignment/security floor.
			if stack.validatorReg != nil && rp.StakedAmount > 0 {
				if _, lookupErr := stack.validatorReg.GetByAgentID(rp.AgentID); lookupErr != nil {
					_, _ = stack.validatorReg.Register(rp.AgentID, rp.StakedAmount, nil, true)
				}
			}
			slog.Info("network: registered remote validator identity",
				"agent_id", rp.AgentID, "staked", rp.StakedAmount)

		case event.EventTypeGenesisFunding:
			// Idempotency: skip if already applied.
			if stack.store != nil {
				key := "genesis-funding:" + string(ev.ID)
				if data, _ := stack.store.GetMeta(key); len(data) > 0 {
					return
				}
			}
			gfp, err := event.GetPayload[event.GenesisFundingPayload](ev)
			if err != nil {
				return
			}
			if err := stack.transfer.TransferFromBucket(
				crypto.AgentID(gfp.FromBucket),
				crypto.AgentID(gfp.ToAgent),
				gfp.Amount,
			); err != nil {
				slog.Warn("genesis-funding: transfer failed",
					"to", gfp.ToAgent, "err", err)
				return
			}
			if stack.store != nil {
				_ = stack.store.PutMeta("genesis-funding:"+string(ev.ID), []byte("1"))
			}
			slog.Info("genesis-funding: applied",
				"to", gfp.ToAgent, "amount", gfp.Amount, "reason", gfp.Reason)

		// Task lifecycle events — applied deterministically on every node.
		// ApplyDAGEvent is idempotent: if this node created the event, the
		// state transition was already applied locally and is silently skipped.
		case event.EventTypeTaskPosted, event.EventTypeTaskClaimed,
			event.EventTypeTaskSubmitted, event.EventTypeTaskApproved,
			event.EventTypeTaskDisputed:
			if stack.taskMgr != nil {
				stack.taskMgr.ApplyDAGEvent(ev)
			}

		// Validator lifecycle events — applied deterministically via the
		// Reducer. After each successful apply, the VotingRound's snapshot
		// is rebound so future rounds use the updated validator set.
		case event.EventTypeValidatorGenesisSet,
			event.EventTypeValidatorJoin,
			event.EventTypeValidatorActivate,
			event.EventTypeValidatorSuspend,
			event.EventTypeValidatorResume,
			event.EventTypeValidatorExit,
			event.EventTypeValidatorKeyRotate,
			event.EventTypeValidatorSlashApplied:
			applyLifecycleEventFromSync(ev, stack.lifecycleReducer, stack.votingRound)
		}
	})

	// Keep legacy P2P vote handler for backward compatibility with existing
	// MsgVote messages. The new architecture uses DAG sync for vote propagation
	// but legacy peers may still send MsgVote directly.
	stack.engine.SetVoteBroadcaster(func(eventID event.EventID, verdict bool, voterID crypto.AgentID) {
		_ = node.BroadcastVote(eventID, verdict)
	})
	node.SetVoteHandler(func(voterID crypto.AgentID, eventID event.EventID, verdict bool) {
		_ = stack.engine.AcceptPeerVote(eventID, voterID, verdict)
	})

	// Start the auto-validator AFTER the finalization handler, sync handler,
	// and vote handler are all wired. If the auto-validator starts before the
	// finalization handler, it can vote and trigger finalization while
	// onFinalized is nil — causing ProcessResult to clear pending without
	// creating a Settlement event (the "OCS pending clears but balance=0" bug).
	if stack.autoVal != nil {
		stack.autoVal.Start()
		slog.Info("auto-validator started (post-handler wiring)",
			"validator_id", agentID)
	}

	apiSrv := api.NewServer(
		apiListenAddr,
		stack.dag, stack.transfer, stack.generation,
		stack.reg, stack.engine, stack.supply,
		node, stack.kp,
	)
	apiSrv.SetPublisher(pub)
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

		// Wire trajectory commit service so POST /v1/tasks/{id}/trajectory/commit
		// and GET /v1/tasks/trajectories/{id} are active.
		blobDir := filepath.Join(dataDir(), "blobs")
		blobStore, err := blobstore.NewFSStore(blobDir, 4<<20) // 4 MiB max blob
		if err != nil {
			slog.Error("trajectory: failed to create blob store", "error", err, "path", blobDir)
		} else {
			trajSvc := trajectory.NewService(
				trajectory.DefaultTrajectoryConfig(),
				stack.dag, blobStore, node, stack.taskMgr, stack.kp,
			)
			trajSvc.SetPublisher(pub)
			apiSrv.SetTrajectoryService(trajSvc)
			slog.Info("trajectory service wired", "blob_dir", blobDir)
		}
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
		// Wire the challenge manager so the challenge bond lifecycle endpoints
		// are active (POST /v1/challenges, POST /v1/challenges/{id}/resolve,
		// GET /v1/challenges/{task_id}).
		if stack.challengeMgr != nil {
			apiSrv.SetChallengeManager(&challengeManagerAdapter{mgr: stack.challengeMgr})
		}
		// Wire network state source so GET /v1/network/state returns live
		// bootstrap status, validator coverage, and replay reserve health.
		apiSrv.SetNetworkStateSource(&nodeNetworkState{
			validatorReg:  stack.validatorReg,
			bootstrapOvr:  stack.bootstrapOvr,
			replayReserve: stack.replayReserve,
			categories:    cfg.Assurance.StructuredCategories,
		})
	}
	apiSrv.SetEconomics(stack.walletMgr, stack.stakeManager, stack.feeCollector)
	apiSrv.SetMinTaskBudget(cfg.Tasks.MinTaskBudget)
	apiSrv.SetProtocolClient(stack.protoClient)
	// Wire canary calibration endpoints. Only available when the store is present.
	if canaryMgr != nil {
		apiSrv.SetCalibrationStore(canaryMgr)
		apiSrv.SetCalibrationAgentsStore(canaryMgr)
	}
	apiSrv.SetEventBus(bus)
	if stack.platformKeys != nil {
		apiSrv.SetPlatformKeys(stack.platformKeys)
	}
	// Wire Ed25519 request signature verification.
	if stack.store != nil {
		nonceStore := auth.NewBadgerNonceStore(stack.store)
		nonceStore.Start()
		defer nonceStore.Stop()
		agentLimiter := auth.NewAgentRateLimiter(100)
		apiSrv.SetAuthVerifier(nonceStore, agentLimiter)
		txIDStore := auth.NewTxIDStore(stack.store)
		txIDStore.Start()
		defer txIDStore.Stop()
		apiSrv.SetTxAuth(auth.DefaultChainID(), txIDStore)
		apiSrv.SetEndpointRateLimiter(auth.NewEndpointRateLimiter(auth.DefaultLimits()))
	}
	// CRITICAL-1: auth defaults to true in NewServer. Disable only when --no-auth
	// is explicitly requested (testnet/development). A warning is emitted below.
	if noAuth {
		apiSrv.SetRequireAuth(false)
		slog.Warn("⚠️  API authentication is DISABLED — all write endpoints are open to unauthenticated callers. Do NOT use in production.")
	}
	// CRITICAL-5: wire snapshot-based vote admission so P2P votes are verified
	// against the validator-set snapshot (seat eligibility + key match).
	// The lifecycle snapshot is the canonical authority; the identity registry
	// is used as fallback when the voter is not in the snapshot (backward
	// compatibility for the transition period).
	node.SetVoteAdmission(func(voterID crypto.AgentID, publicKey []byte) error {
		// Primary path: check the lifecycle snapshot.
		if stack.lifecycleReducer != nil {
			snap := stack.lifecycleReducer.Snapshot()
			w, eligible := snap.VoteWeightByKey(voterID)
			if eligible && w > 0 {
				// Seat found and eligible — verify the key matches.
				// The voterID is the hex-encoded public key; compare directly.
				if hex.EncodeToString(publicKey) == string(voterID) {
					return nil // snapshot-validated
				}
				return fmt.Errorf("seat eligible but key mismatch for %s", voterID)
			}
			// Seat not in snapshot or not eligible — check if the voter's
			// operator key matches their claimed identity via the snapshot's
			// seat table (the voterID might be the operator key, not the seat ID).
		}
		// Fallback: identity registry for agents not yet in the lifecycle
		// snapshot (e.g., node agents that haven't joined via lifecycle events).
		fp, err := stack.reg.Get(voterID)
		if err != nil {
			return fmt.Errorf("voter %s not in snapshot or registry", voterID)
		}
		if !bytes.Equal(fp.PublicKey, publicKey) {
			return fmt.Errorf("public key mismatch for %s", voterID)
		}
		return nil
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
				// Refresh SecurityFloor per-category validator counts so PostTask
				// uses live coverage data for assured-lane enforcement.
				if stack.validatorReg != nil {
					for _, cat := range cfg.Assurance.StructuredCategories {
						count := stack.validatorReg.ActiveCountForCategory(cat)
						secFloor.SetState(assurance.CategorySecurityState{
							Category:       cat,
							ValidatorCount: float64(count),
						})
					}
				}
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
			seedGenesisMint(stack.transfer, stack.store)
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
	reg.CleanupStaleInstances()
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

	// Broadcast locally-created DAG events that were added before peers
	// connected. Genesis funding, registration, and other startup events are
	// created during startStack (before node.Start), so they never entered
	// the Fast Path pipeline. Broadcast them now so peers receive them.
	broadcastLocalEvents(stack.dag, node, agentID)

	runLoop(agentID, stack.dag, node, stack.engine, stack.supply, stack.bus)
	stopStack(node, stack)
	slog.Info("node stopped cleanly")
}

// broadcastLocalEvents sends all DAG events authored by this node to
// connected peers. Called once after peer connections are established to
// disseminate events created during startup before the node was networked.
func broadcastLocalEvents(d *dag.DAG, node *network.Node, localAgent crypto.AgentID) {
	events := d.All()
	sent := 0
	for _, ev := range events {
		if ev.AgentID != string(localAgent) {
			continue // only broadcast our own events
		}
		_ = node.SubmitLocalEvent(ev)
		_ = node.Broadcast(ev)
		sent++
	}
	if sent > 0 {
		slog.Info("broadcast: disseminated locally-created startup events",
			"count", sent, "agent_id", localAgent)
	}
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


// checkGenesisConsistency verifies that the loaded store was seeded with the
// current genesis allocation constants.
//
//  1. Total minted check: TransferLedger.TotalMinted() must equal
//     genesis.TotalSupply. TotalMinted tracks the cumulative µAET created by
//     FundAgent (the only token-creation path) and is persisted to the store.
//     Unlike summing genesis bucket balances — which decreases as tokens move
//     to agents, staking pools, and escrow — TotalMinted is monotonic and
//     reflects the original seeding regardless of subsequent token flows.
//
//  2. Zombie-agent check: if the identity registry has registered agents but the
//     ecosystem bucket balance equals exactly genesis.EcosystemAllocation (i.e.
//     no onboarding transfer has ever drawn from it), those agents were registered
//     before the TransferFromBucket onboarding fix and hold zero balance. The
//     store is stale and must be wiped so they re-register and receive funds.
//
// Returns true when the store is consistent, false when either check fails.
// A zero TotalMinted means genesis hasn't run yet; the auto-genesis block
// handles that case, so we return true here.
func checkGenesisConsistency(tl *ledger.TransferLedger, reg *identity.Registry) bool {
	minted := tl.TotalMinted()

	// Zero minted means genesis hasn't run yet; auto-genesis handles this.
	if minted == 0 {
		return true
	}

	if minted != genesis.TotalSupply {
		slog.Warn("genesis consistency check failed: TotalMinted does not match TotalSupply",
			"total_minted", minted,
			"expected", genesis.TotalSupply,
		)
		return false
	}

	// Zombie-agent check: agents registered before the TransferFromBucket
	// onboarding fix received no allocation (ecosystem balance was never drawn
	// down). Detect this by checking whether any agents are registered while the
	// ecosystem bucket still holds its full genesis allocation.
	//
	// Skip on testnet: the testnet-validator is registered via startStack and
	// funded from the rewards bucket, not the ecosystem bucket. Its presence
	// in the identity registry with an untouched ecosystem bucket is expected
	// testnet behavior, not a zombie-agent signal.
	if os.Getenv("AETHERNET_TESTNET") != "true" {
		ecosystemBal, _ := tl.Balance(crypto.AgentID(genesis.BucketEcosystem))
		if ecosystemBal == genesis.EcosystemAllocation && len(reg.All(1, 0)) > 0 {
			slog.Warn("genesis consistency check failed: agents registered but ecosystem bucket is untouched (zombie agents from pre-onboarding-fix binary)",
				"registered_agents", len(reg.All(0, 0)),
				"ecosystem_balance", ecosystemBal,
			)
			return false
		}
	}

	return true
}

// genesisStore is the subset of store.Store used by genesis idempotency checks.
type genesisStore interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

const genesisMarkerKey = "genesis_complete"

// seedGenesisMint creates the 6 genesis allocation buckets via FundAgent.
// This is the ONLY code path that mints tokens from nothing. The mint cap is
// set immediately after to prevent any future minting. Subsequent transfers
// (validator funding, faucet) are emitted as GenesisFunding DAG events by
// emitGenesisTransfers for auditability.
func seedGenesisMint(tl *ledger.TransferLedger, s genesisStore) {
	if s != nil {
		data, _ := s.GetMeta(genesisMarkerKey)
		if len(data) > 0 {
			treasuryBal, _ := tl.Balance(crypto.AgentID(genesis.BucketTreasury))
			ecosystemBal, _ := tl.Balance(crypto.AgentID(genesis.BucketEcosystem))
			if treasuryBal > 0 && ecosystemBal > 0 {
				slog.Info("auto-genesis: genesis mint already complete, skipping")
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

// emitGenesisTransfers creates canonical GenesisFunding DAG events for
// the post-mint transfers (validator bootstrap, faucet pool). These events
// are auditable — new nodes replay them from the DAG to reach the same
// ledger state. Called after the DAG, Publisher, and keypair are available.
func emitGenesisTransfers(d *dag.DAG, pub *localpub.Publisher, kp *crypto.KeyPair, agentID crypto.AgentID, tl *ledger.TransferLedger, s genesisStore) {
	// Skip if genesis transfers already emitted (idempotent via store marker).
	const genesisTransfersKey = "genesis_transfers_emitted"
	if s != nil {
		if data, _ := s.GetMeta(genesisTransfersKey); len(data) > 0 {
			return
		}
	}

	// Validator bootstrap from rewards bucket.
	validatorBal, _ := tl.Balance(crypto.AgentID(genesis.GenesisValidatorID))
	if validatorBal == 0 {
		emitGenesisFundingEvent(d, pub, kp, agentID,
			genesis.BucketRewards, genesis.GenesisValidatorID,
			genesis.GenesisValidatorFund, "validator-genesis")
	}

	// Testnet faucet from ecosystem bucket.
	if os.Getenv("AETHERNET_TESTNET") == "true" {
		faucetBal, _ := tl.Balance(crypto.AgentID(genesis.BucketFaucet))
		if faucetBal == 0 {
			emitGenesisFundingEvent(d, pub, kp, agentID,
				genesis.BucketEcosystem, genesis.BucketFaucet,
				genesis.FaucetAllocation, "faucet-pool")
		}
	}

	if s != nil {
		_ = s.PutMeta(genesisTransfersKey, []byte("1"))
	}
}

// emitGenesisFundingEvent creates and publishes a single GenesisFunding DAG event.
func emitGenesisFundingEvent(d *dag.DAG, pub *localpub.Publisher, kp *crypto.KeyPair, agentID crypto.AgentID,
	fromBucket, toAgent string, amount uint64, reason string) {
	payload := event.GenesisFundingPayload{
		Version:    1,
		FromBucket: fromBucket,
		ToAgent:    toAgent,
		Amount:     amount,
		Reason:     reason,
	}
	tips := d.Tips()
	priorTS := make(map[event.EventID]uint64, len(tips))
	for _, ref := range tips {
		if ev, err := d.Get(ref); err == nil {
			priorTS[ref] = ev.CausalTimestamp
		}
	}
	ev, err := event.New(event.EventTypeGenesisFunding, tips, payload, string(agentID), priorTS, 0)
	if err != nil {
		slog.Warn("genesis: failed to create funding event", "to", toAgent, "err", err)
		return
	}
	if kp != nil {
		_ = crypto.SignEvent(ev, kp)
	}
	if pub != nil {
		if err := pub.Publish(ev); err != nil {
			slog.Warn("genesis: failed to publish funding event", "to", toAgent, "err", err)
		}
	} else if err := d.Add(ev); err != nil {
		slog.Warn("genesis: failed to add funding event to DAG", "to", toAgent, "err", err)
	}
	slog.Info("genesis: emitted funding event", "to", toAgent, "amount", amount, "event_id", ev.ID)
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

// cmdValidatorSet shows or verifies the genesis validator set. Operators use
// this to confirm all nodes will produce the same snapshot before deploying.
//
// Usage:
//
//	aethernet validator-set                          show default testnet manifest
//	aethernet validator-set --manifest path.json     show custom manifest
//	aethernet validator-set --verify <digest>         verify digest matches
//	aethernet validator-set --export path.json       write manifest to file
func cmdValidatorSet() {
	fs := flag.NewFlagSet("validator-set", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "path to genesis validator manifest JSON")
	verifyDigest := fs.String("verify", "", "expected snapshot digest to verify")
	exportPath := fs.String("export", "", "write manifest JSON to this path")
	_ = fs.Parse(os.Args[2:])

	// Load manifest.
	var m *validatorlifecycle.GenesisManifest
	if *manifestPath != "" {
		var err error
		m, err = validatorlifecycle.LoadManifestFromFile(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	} else {
		m = validatorlifecycle.DefaultTestnetManifest()
	}

	// Export mode.
	if *exportPath != "" {
		if err := validatorlifecycle.WriteManifestFile(m, *exportPath); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: write manifest: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Manifest written to %s\n", *exportPath)
	}

	// Show summary.
	fmt.Print(validatorlifecycle.FormatManifestSummary(m))

	// Verify mode.
	if *verifyDigest != "" {
		actual, err := validatorlifecycle.ComputeManifestDigest(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if actual != *verifyDigest {
			fmt.Fprintf(os.Stderr, "\nDIGEST MISMATCH\n  expected: %s\n  actual:   %s\n", *verifyDigest, actual)
			os.Exit(1)
		}
		fmt.Printf("\nDigest verified: %s\n", actual)
	}
}
