package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/trajectory"
)

func setupTrajectoryTest(t *testing.T) (*trajectory.Service, *dag.DAG, *tasks.TaskManager, *crypto.KeyPair, string) {
	t.Helper()
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, err := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}

	cfg := trajectory.DefaultTrajectoryConfig()
	svc := trajectory.NewService(cfg, d, blob, nil, tm, kp)

	task, err := tm.PostTask(string(kp.AgentID()), "Test trajectory task", "desc", "research", 100000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	return svc, d, tm, kp, task.ID
}

func TestTrajectoryCommit_SuccessfulEmission(t *testing.T) {
	svc, d, _, kp, taskID := setupTrajectoryTest(t)

	resp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "Testing recursive approach",
		ComputeCost:         10000,
		QualityScoreBP:      7000,
	})
	if err != nil {
		t.Fatalf("EmitCommit: %v", err)
	}
	if resp.EventID == "" {
		t.Error("EventID should be non-empty")
	}
	if resp.CheckpointHash == "" {
		t.Error("CheckpointHash should be non-empty")
	}
	if resp.CheckpointSize <= 0 {
		t.Error("CheckpointSize should be > 0")
	}

	ev, err := d.Get(resp.EventID)
	if err != nil {
		t.Fatalf("event not in DAG: %v", err)
	}
	if ev.Type != event.EventTypeTrajectoryCommit {
		t.Errorf("Type = %q; want TrajectoryCommit", ev.Type)
	}
}

func TestTrajectoryCommit_NonClaimerRejected(t *testing.T) {
	svc, _, _, _, taskID := setupTrajectoryTest(t)
	otherKP, _ := crypto.GenerateKeyPair()

	_, err := svc.EmitCommit(context.Background(), otherKP.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "not the claimer",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})
	if err == nil {
		t.Fatal("should reject non-claimer")
	}
}

func TestTrajectoryCommit_OversizedCheckpointRejected(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, _ := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)

	cfg := trajectory.DefaultTrajectoryConfig()
	cfg.MaxCheckpointBodySize = 100
	svc := trajectory.NewService(cfg, d, blob, nil, tm, kp)

	task, _ := tm.PostTask(string(kp.AgentID()), "Task", "desc", "code", 100000)
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	_, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              task.ID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "This approach description is intentionally very long to exceed the 100-byte limit for testing purposes and should trigger rejection.",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})
	if err == nil {
		t.Fatal("should reject oversized checkpoint body")
	}
}

func TestTrajectoryCommit_PerTaskLimitEnforced(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, _ := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)

	cfg := trajectory.DefaultTrajectoryConfig()
	cfg.MaxCommitsPerTask = 2
	svc := trajectory.NewService(cfg, d, blob, nil, tm, kp)

	task, _ := tm.PostTask(string(kp.AgentID()), "Task", "desc", "code", 100000)
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	var lastID string
	for i := 0; i < 2; i++ {
		resp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
			TaskID:              task.ID,
			ParentCommitID:      lastID,
			Outcome:             event.OutcomeExploring,
			ApproachDescription: fmt.Sprintf("attempt %d", i),
			ComputeCost:         uint64(1000 + i),
			QualityScoreBP:      5000,
		})
		if err != nil {
			t.Fatalf("commit %d: %v", i+1, err)
		}
		lastID = string(resp.EventID)
	}

	_, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              task.ID,
		ParentCommitID:      lastID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "too many",
		ComputeCost:         9999,
		QualityScoreBP:      5000,
	})
	if err == nil {
		t.Fatal("3rd commit should be rejected")
	}
}

func TestTrajectoryCommit_RateLimitEnforced(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	tm := tasks.NewTaskManager()
	blob, _ := blobstore.NewFSStore(t.TempDir(), blobstore.DefaultMaxBlobSize)

	cfg := trajectory.DefaultTrajectoryConfig()
	cfg.MaxCommitsPerMinute = 2
	cfg.MaxCommitsPerTask = 100
	svc := trajectory.NewService(cfg, d, blob, nil, tm, kp)

	task, _ := tm.PostTask(string(kp.AgentID()), "Task", "desc", "code", 100000)
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{Version: 1, TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	var lastID string
	for i := 0; i < 2; i++ {
		resp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
			TaskID:              task.ID,
			ParentCommitID:      lastID,
			Outcome:             event.OutcomeExploring,
			ApproachDescription: fmt.Sprintf("fast %d", i),
			ComputeCost:         uint64(2000 + i),
			QualityScoreBP:      5000,
		})
		if err != nil {
			t.Fatalf("commit %d: %v", i+1, err)
		}
		lastID = string(resp.EventID)
	}

	_, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              task.ID,
		ParentCommitID:      lastID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "too fast",
		ComputeCost:         9999,
		QualityScoreBP:      5000,
	})
	if err == nil {
		t.Fatal("3rd in same minute should be rejected")
	}
}

func TestTrajectoryCommit_RootCausalRefs(t *testing.T) {
	svc, d, tm, kp, taskID := setupTrajectoryTest(t)
	task, _ := tm.Get(taskID)
	claimEventID := event.EventID(task.ClaimEventID)

	resp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "root commit",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})
	if err != nil {
		t.Fatalf("EmitCommit: %v", err)
	}

	ev, _ := d.Get(resp.EventID)
	if len(ev.CausalRefs) != 1 {
		t.Fatalf("root CausalRefs = %d; want 1", len(ev.CausalRefs))
	}
	if ev.CausalRefs[0] != claimEventID {
		t.Errorf("CausalRefs[0] = %q; want %q", ev.CausalRefs[0], claimEventID)
	}
}

func TestTrajectoryCommit_ChildCausalRefs(t *testing.T) {
	svc, d, tm, kp, taskID := setupTrajectoryTest(t)
	task, _ := tm.Get(taskID)
	claimEventID := event.EventID(task.ClaimEventID)

	rootResp, _ := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "root",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})

	childResp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		ParentCommitID:      string(rootResp.EventID),
		Outcome:             event.OutcomeConverged,
		ApproachDescription: "child",
		ComputeCost:         2000,
		QualityScoreBP:      9000,
	})
	if err != nil {
		t.Fatalf("child: %v", err)
	}

	ev, _ := d.Get(childResp.EventID)
	if len(ev.CausalRefs) != 2 {
		t.Fatalf("child CausalRefs = %d; want 2", len(ev.CausalRefs))
	}
	if ev.CausalRefs[0] != claimEventID {
		t.Errorf("CausalRefs[0] = %q; want %q", ev.CausalRefs[0], claimEventID)
	}
	if ev.CausalRefs[1] != rootResp.EventID {
		t.Errorf("CausalRefs[1] = %q; want %q", ev.CausalRefs[1], rootResp.EventID)
	}
}

func TestTrajectoryCommit_BlobStoredBeforeEvent(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)

	resp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "blob stored test",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})
	if err != nil {
		t.Fatalf("EmitCommit: %v", err)
	}
	if resp.CheckpointHash == "" {
		t.Error("checkpoint hash should be non-empty")
	}
	if resp.CheckpointSize <= 0 {
		t.Error("checkpoint size should be > 0")
	}
}

// ── Retrieval Tests ──────────────────────────────────────────────────────────

func TestGetTrajectories_DeterministicOrdering(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	// Emit 3 commits forming a chain.
	var lastID string
	for i := 0; i < 3; i++ {
		resp, err := svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
			TaskID:              taskID,
			ParentCommitID:      lastID,
			Outcome:             event.OutcomeExploring,
			ApproachDescription: fmt.Sprintf("step %d", i),
			ComputeCost:         uint64(1000 * (i + 1)),
			QualityScoreBP:      uint32((i + 1) * 2000),
		})
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		lastID = string(resp.EventID)
	}

	tree, err := svc.GetTrajectories(ctx, taskID, false, "", 100)
	if err != nil {
		t.Fatalf("GetTrajectories: %v", err)
	}
	if tree.Count != 3 {
		t.Fatalf("Count = %d; want 3", tree.Count)
	}

	// Verify ascending CausalTimestamp order.
	for i := 1; i < len(tree.Commits); i++ {
		prev := tree.Commits[i-1].CausalTimestamp
		curr := tree.Commits[i].CausalTimestamp
		if curr < prev {
			t.Errorf("commits not sorted: [%d].ts=%d > [%d].ts=%d", i-1, prev, i, curr)
		}
	}
}

func TestGetTrajectories_WithoutBodies(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "no bodies test",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})

	tree, err := svc.GetTrajectories(ctx, taskID, false, "", 100)
	if err != nil {
		t.Fatalf("GetTrajectories: %v", err)
	}
	if tree.Count != 1 {
		t.Fatalf("Count = %d; want 1", tree.Count)
	}
	if tree.Commits[0].CheckpointBody != nil {
		t.Error("include_bodies=false should not include checkpoint body")
	}
}

func TestGetTrajectories_WithBodies(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "body included test",
		Parameters:          map[string]string{"lr": "0.01"},
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})

	tree, err := svc.GetTrajectories(ctx, taskID, true, "", 100)
	if err != nil {
		t.Fatalf("GetTrajectories: %v", err)
	}
	if tree.Count != 1 {
		t.Fatalf("Count = %d; want 1", tree.Count)
	}
	if !tree.Commits[0].BodyAvailable {
		t.Error("body should be available when include_bodies=true")
	}
	if tree.Commits[0].CheckpointBody == nil {
		t.Error("checkpoint body should be non-nil")
	}
	if tree.Commits[0].CheckpointBody.ApproachDescription != "body included test" {
		t.Errorf("ApproachDescription = %q; want 'body included test'",
			tree.Commits[0].CheckpointBody.ApproachDescription)
	}
}

func TestGetTrajectories_MissingBlobGraceful(t *testing.T) {
	svc, d, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	// Emit a commit (blob stored normally).
	resp, _ := svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "will delete blob",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})

	// Manually fabricate a trajectory event with a bogus checkpoint hash
	// to simulate a missing blob. Create directly via event.New.
	payload := event.TrajectoryCommitPayload{
		TaskID:         taskID,
		Outcome:        event.OutcomeDeadEnd,
		CheckpointHash: "0000000000000000000000000000000000000000000000000000000000000000",
		CheckpointSize: 1,
		QualityScoreBP: 3000,
	}
	task, _ := svc.TaskMgr().Get(taskID)
	claimEv := event.EventID(task.ClaimEventID)
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{claimEv, resp.EventID}, payload, string(kp.AgentID()),
		map[event.EventID]uint64{claimEv: 1, resp.EventID: 2}, 0)
	_ = crypto.SignEvent(ev, kp)
	_ = d.Add(ev)

	tree, err := svc.GetTrajectories(ctx, taskID, true, "", 100)
	if err != nil {
		t.Fatalf("GetTrajectories should not fail: %v", err)
	}
	// Both commits returned — the missing blob one has BodyAvailable=false.
	if tree.Count < 2 {
		t.Fatalf("Count = %d; want >= 2", tree.Count)
	}

	var foundMissing bool
	for _, c := range tree.Commits {
		if c.CheckpointHash == "0000000000000000000000000000000000000000000000000000000000000000" {
			foundMissing = true
			if c.BodyAvailable {
				t.Error("missing blob should have BodyAvailable=false")
			}
			if c.CheckpointBody != nil {
				t.Error("missing blob should have nil CheckpointBody")
			}
		}
	}
	if !foundMissing {
		t.Error("should include the commit with missing blob")
	}
}

func TestGetTrajectories_BranchFilter(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	// Emit commits on different branches.
	svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "main branch",
		BranchID:            "main",
		ComputeCost:         1000,
		QualityScoreBP:      5000,
	})
	svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		Outcome:             event.OutcomePivot,
		ApproachDescription: "alt branch",
		BranchID:            "alt",
		ComputeCost:         2000,
		QualityScoreBP:      6000,
	})

	// Filter by branch.
	tree, _ := svc.GetTrajectories(ctx, taskID, false, "main", 100)
	if tree.Count != 1 {
		t.Fatalf("filtered Count = %d; want 1", tree.Count)
	}
	if tree.Commits[0].BranchID != "main" {
		t.Errorf("BranchID = %q; want main", tree.Commits[0].BranchID)
	}

	// No filter — both branches.
	allTree, _ := svc.GetTrajectories(ctx, taskID, false, "", 100)
	if allTree.Count != 2 {
		t.Errorf("unfiltered Count = %d; want 2", allTree.Count)
	}
}

func TestGetTrajectories_LimitBounded(t *testing.T) {
	svc, _, _, kp, taskID := setupTrajectoryTest(t)
	ctx := context.Background()

	var lastID string
	for i := 0; i < 5; i++ {
		resp, _ := svc.EmitCommit(ctx, kp.AgentID(), trajectory.CommitRequest{
			TaskID:              taskID,
			ParentCommitID:      lastID,
			Outcome:             event.OutcomeExploring,
			ApproachDescription: fmt.Sprintf("step %d", i),
			ComputeCost:         uint64(1000 + i),
			QualityScoreBP:      5000,
		})
		lastID = string(resp.EventID)
	}

	// Limit to 3.
	tree, _ := svc.GetTrajectories(ctx, taskID, false, "", 3)
	if tree.Count != 3 {
		t.Fatalf("limited Count = %d; want 3", tree.Count)
	}

	// Full retrieval.
	fullTree, _ := svc.GetTrajectories(ctx, taskID, false, "", 100)
	if fullTree.Count != 5 {
		t.Errorf("full Count = %d; want 5", fullTree.Count)
	}
}
