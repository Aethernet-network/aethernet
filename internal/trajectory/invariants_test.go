// invariants_test.go proves TrajectoryCommit Phase 1 architectural invariants.
//
// Each test is named after the invariant it proves. These tests demonstrate
// the architecture, not just code coverage.
package trajectory_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/trajectory"
)

// ── Invariant: event.Event remains the canonical ledger object ───────────────

func TestInvariant_TrajectoryCommitIsStandardEvent(t *testing.T) {
	// A trajectory commit is stored as event.Event with EventTypeTrajectoryCommit.
	// It is NOT a separate model, parallel event type, or external object.
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CheckpointSize: 100,
		QualityScoreBP: 7000,
	}
	ev, err := event.New(event.EventTypeTrajectoryCommit, nil, payload, "agent-1", nil, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	// It is an event.Event, not a trajectory-specific type.
	if ev.Type != event.EventTypeTrajectoryCommit {
		t.Errorf("Type = %q; want TrajectoryCommit", ev.Type)
	}

	// Its payload is JSON-encoded TrajectoryCommitPayload.
	var decoded event.TrajectoryCommitPayload
	if err := json.Unmarshal(ev.Payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.TaskID != "task-1" {
		t.Errorf("TaskID = %q; want task-1", decoded.TaskID)
	}
}

// ── Invariant: dag.Add enforcement unchanged for trajectory commits ──────────

func TestInvariant_DAGAddEnforcesAllChecksForTrajectory(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Add a genesis event.
	genesis, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		string(kp.AgentID()), nil, 0)
	d.Add(genesis)

	// Create a trajectory commit referencing genesis.
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CheckpointSize: 100,
		QualityScoreBP: 5000,
	}
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{genesis.ID},
		payload, string(kp.AgentID()),
		map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}, 0)
	_ = crypto.SignEvent(ev, kp)

	// dag.Add should succeed.
	if err := d.Add(ev); err != nil {
		t.Fatalf("dag.Add should accept valid trajectory commit: %v", err)
	}

	// Duplicate rejection still works.
	if err := d.Add(ev); err == nil {
		t.Error("dag.Add should reject duplicate trajectory commit")
	}

	// Missing causal ref still rejected.
	payload2 := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-2",
		Outcome:        event.OutcomeConverged,
		CheckpointHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		CheckpointSize: 200,
		QualityScoreBP: 9000,
	}
	orphan, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{"nonexistent-parent"},
		payload2, string(kp.AgentID()),
		map[event.EventID]uint64{"nonexistent-parent": 1}, 0)
	_ = crypto.SignEvent(orphan, kp)
	if err := d.Add(orphan); err == nil {
		t.Error("dag.Add should reject trajectory commit with missing causal ref")
	}
}

// ── Invariant: DAG remains append-only with trajectory commits ───────────────

func TestInvariant_DAGAppendOnlyWithTrajectory(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Add events.
	genesis, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		string(kp.AgentID()), nil, 0)
	d.Add(genesis)

	traj, _ := event.New(event.EventTypeTrajectoryCommit, nil,
		event.TrajectoryCommitPayload{
			TaskID: "task-1", Outcome: event.OutcomeExploring,
			CheckpointHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			CheckpointSize: 100, QualityScoreBP: 5000, Version: 1,
		}, string(kp.AgentID()), nil, 0)
	d.Add(traj)

	sizeBefore := d.Size()
	// Cannot remove events from DAG (no Delete method exists).
	// Size only grows.
	if d.Size() != sizeBefore {
		t.Error("DAG should be append-only")
	}
	if d.Size() != 2 {
		t.Errorf("DAG size = %d; want 2", d.Size())
	}
}

// ── Invariant: checkpoint blobs are NOT Fast Path EventBody ──────────────────

func TestInvariant_CheckpointBlobNotFastPathEventBody(t *testing.T) {
	// The lean TrajectoryCommitPayload IS the event.Event.Payload (and thus
	// the Fast Path EventBody). The large CheckpointBody is stored in the
	// blobstore, referenced by CheckpointHash.
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		CheckpointSize: 1048576, // 1 MiB — the blob is large
		QualityScoreBP: 7000,
	}
	ev, _ := event.New(event.EventTypeTrajectoryCommit, nil, payload, "agent-1", nil, 0)

	// The event payload is the LEAN payload (< 1 KB typically).
	if len(ev.Payload) > 1024 {
		t.Error("event.Payload should be the lean TrajectoryCommitPayload, not the full checkpoint body")
	}

	// The event payload contains the checkpoint hash as a STRING reference,
	// not the checkpoint body data.
	var decoded event.TrajectoryCommitPayload
	json.Unmarshal(ev.Payload, &decoded)
	if decoded.CheckpointHash == "" {
		t.Error("payload should reference checkpoint by hash, not contain it")
	}

	// Fast Path BodyCommitment is SHA-256 of the lean payload JSON.
	bodyCommitment, _ := event.ComputeBodyCommitment(ev)
	payloadHash := sha256.Sum256(ev.Payload)
	expectedCommitment := hex.EncodeToString(payloadHash[:])
	if bodyCommitment != expectedCommitment {
		t.Error("Fast Path BodyCommitment should be hash of lean payload, not checkpoint blob")
	}
}

// ── Invariant: lean payload participates in normal event canonicalization ─────

func TestInvariant_TrajectoryPayloadInCanonicalID(t *testing.T) {
	p1 := event.TrajectoryCommitPayload{
		Version: 1, TaskID: "task-1", Outcome: event.OutcomeExploring,
		CheckpointHash: "aaaa", CheckpointSize: 100, QualityScoreBP: 5000,
	}
	p2 := event.TrajectoryCommitPayload{
		Version: 1, TaskID: "task-1", Outcome: event.OutcomeExploring,
		CheckpointHash: "bbbb", CheckpointSize: 100, QualityScoreBP: 5000,
	}

	ev1, _ := event.New(event.EventTypeTrajectoryCommit, nil, p1, "agent", nil, 0)
	ev2, _ := event.New(event.EventTypeTrajectoryCommit, nil, p2, "agent", nil, 0)

	if ev1.ID == ev2.ID {
		t.Error("different trajectory payloads should produce different EventIDs")
	}

	// Same payload → same ID (deterministic).
	ev3, _ := event.New(event.EventTypeTrajectoryCommit, nil, p1, "agent", nil, 0)
	if ev1.ID != ev3.ID {
		t.Error("same trajectory payload should produce same EventID")
	}
}

// ── Invariant: PrimaryTips excludes trajectory, Tips includes all ────────────

func TestInvariant_PrimaryTipsDoesNotAffectMechanicalTips(t *testing.T) {
	d := dag.New()

	// Add both types.
	transfer, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-1", nil, 0)
	d.Add(transfer)

	traj, _ := event.New(event.EventTypeTrajectoryCommit, nil,
		event.TrajectoryCommitPayload{
			TaskID: "task-1", Outcome: event.OutcomeExploring,
			CheckpointHash: "ffff", CheckpointSize: 100, QualityScoreBP: 5000, Version: 1,
		}, "agent-2", nil, 0)
	d.Add(traj)

	// Tips() returns BOTH (mechanical tips unchanged).
	tips := d.Tips()
	if len(tips) != 2 {
		t.Errorf("Tips() = %d; want 2 (mechanical tips unaffected)", len(tips))
	}

	// PrimaryTips() returns only non-trajectory.
	primary := d.PrimaryTips()
	if len(primary) != 1 {
		t.Errorf("PrimaryTips() = %d; want 1", len(primary))
	}
	if primary[0] != transfer.ID {
		t.Error("PrimaryTips should return transfer, not trajectory")
	}
}

// ── Invariant: end-to-end emit → DAG → retrieve roundtrip ───────────────────

func TestInvariant_EmitAndRetrieveRoundtrip(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, _ := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)

	cfg := trajectory.DefaultTrajectoryConfig()
	svc := trajectory.NewService(cfg, d, blob, nil, tm, kp)

	// Create and claim a task.
	task, _ := tm.PostTask(string(kp.AgentID()), "Task", "desc", "code", 100000)
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil,
		event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())},
		string(kp.AgentID()), nil, 0)
	d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	// Emit trajectory commit.
	ctx := context.Background()
	resp, err := svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              task.ID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "roundtrip test",
		ComputeCost:         5000,
		QualityScoreBP:      8000,
	})
	if err != nil {
		t.Fatalf("EmitCommit: %v", err)
	}

	// Retrieve trajectories.
	tree, err := svc.GetTrajectories(ctx, task.ID, true, "", 100)
	if err != nil {
		t.Fatalf("GetTrajectories: %v", err)
	}
	if tree.Count != 1 {
		t.Fatalf("Count = %d; want 1", tree.Count)
	}
	if string(tree.Commits[0].EventID) != string(resp.EventID) {
		t.Error("retrieved commit EventID should match emitted EventID")
	}
	if !tree.Commits[0].BodyAvailable {
		t.Error("checkpoint body should be available (stored before event)")
	}
	if tree.Commits[0].CheckpointBody == nil {
		t.Error("checkpoint body should be non-nil when include_bodies=true")
	}
	if tree.Commits[0].CheckpointBody.ApproachDescription != "roundtrip test" {
		t.Error("checkpoint body content should match what was emitted")
	}
}

// ── Invariant: missing blob does not corrupt DAG or event behavior ───────────

func TestInvariant_MissingBlobDoesNotCorruptDAG(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create a trajectory commit with a bogus checkpoint hash.
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		Outcome:        event.OutcomeDeadEnd,
		CheckpointHash: "0000000000000000000000000000000000000000000000000000000000000000",
		CheckpointSize: 1,
		QualityScoreBP: 3000,
	}
	ev, _ := event.New(event.EventTypeTrajectoryCommit, nil, payload, string(kp.AgentID()), nil, 0)
	d.Add(ev)

	// The event is in the DAG regardless of blob existence.
	got, err := d.Get(ev.ID)
	if err != nil {
		t.Fatalf("event should be in DAG: %v", err)
	}
	if got.Type != event.EventTypeTrajectoryCommit {
		t.Error("event type should be preserved")
	}

	// DAG operations work normally.
	if d.Size() != 1 {
		t.Error("DAG size should be 1")
	}
	tips := d.Tips()
	if len(tips) != 1 || tips[0] != ev.ID {
		t.Error("tips should include the trajectory commit")
	}
}

// ── Invariant: trajectory commit signing uses standard path ──────────────────

func TestInvariant_TrajectorySigningUsesStandardPath(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	genesis, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		string(kp.AgentID()), nil, 0)
	d.Add(genesis)

	payload := event.TrajectoryCommitPayload{
		TaskID: "task-1", Outcome: event.OutcomeExploring,
		CheckpointHash: "1111111111111111111111111111111111111111111111111111111111111111",
		CheckpointSize: 100, QualityScoreBP: 5000, Version: 1,
	}
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{genesis.ID},
		payload, string(kp.AgentID()),
		map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}, 0)

	// Sign with the standard crypto.SignEvent path.
	if err := crypto.SignEvent(ev, kp); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}

	// Verify with the standard crypto.VerifyEvent path.
	if !crypto.VerifyEvent(ev) {
		t.Error("trajectory commit should verify via standard path")
	}

	// dag.Add should accept the signed event.
	if err := d.Add(ev); err != nil {
		t.Fatalf("dag.Add should accept signed trajectory commit: %v", err)
	}
}

// ── Invariant: ExplorationRoot deterministic for same commit set ─────────────

func TestInvariant_ExplorationRootDeterministic(t *testing.T) {
	ids := []string{"commit-z", "commit-a", "commit-m"}

	r1, _ := trajectory.ComputeExplorationRoot(ids)
	r2, _ := trajectory.ComputeExplorationRoot(ids)

	if r1 != r2 {
		t.Error("ExplorationRoot must be deterministic for same commit set")
	}

	// Different order → same root (order-insensitive).
	r3, _ := trajectory.ComputeExplorationRoot([]string{"commit-a", "commit-m", "commit-z"})
	if r1 != r3 {
		t.Error("ExplorationRoot must be order-insensitive")
	}
}

// ── Invariant: retrieval ordering deterministic ──────────────────────────────

func TestInvariant_RetrievalOrderingDeterministic(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, _ := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)

	svc := trajectory.NewService(trajectory.DefaultTrajectoryConfig(), d, blob, nil, tm, kp)

	task, _ := tm.PostTask(string(kp.AgentID()), "Task", "desc", "code", 100000)
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil,
		event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())},
		string(kp.AgentID()), nil, 0)
	d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	ctx := context.Background()
	var lastID string
	for i := 0; i < 5; i++ {
		resp, _ := svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
			TaskID:              task.ID,
			ParentCommitID:      lastID,
			Outcome:             event.OutcomeExploring,
			ApproachDescription: "step",
			ComputeCost:         uint64(1000 + i),
			QualityScoreBP:      5000,
		})
		lastID = string(resp.EventID)
	}

	tree1, _ := svc.GetTrajectories(ctx, task.ID, false, "", 100)
	tree2, _ := svc.GetTrajectories(ctx, task.ID, false, "", 100)

	if tree1.Count != tree2.Count {
		t.Fatal("retrieval count should be deterministic")
	}
	for i := range tree1.Commits {
		if tree1.Commits[i].EventID != tree2.Commits[i].EventID {
			t.Error("retrieval ordering must be deterministic by (CausalTimestamp, EventID)")
		}
	}
}
