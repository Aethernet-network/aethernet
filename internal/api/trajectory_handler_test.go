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

	claimPayload := event.TaskClaimedPayload{TaskID: task.ID, ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
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
		QualityScore:        0.7,
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
		QualityScore:        0.5,
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
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
	_ = d.Add(claimEv)
	tm.ApplyDAGEvent(claimEv)

	_, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              task.ID,
		Outcome:             event.OutcomeExploring,
		ApproachDescription: "This approach description is intentionally very long to exceed the 100-byte limit for testing purposes and should trigger rejection.",
		ComputeCost:         1000,
		QualityScore:        0.5,
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
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
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
			QualityScore:        0.5,
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
		QualityScore:        0.5,
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
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, event.TaskClaimedPayload{TaskID: task.ID, ClaimerID: string(kp.AgentID())}, string(kp.AgentID()), nil, 0)
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
			QualityScore:        0.5,
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
		QualityScore:        0.5,
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
		QualityScore:        0.5,
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
		QualityScore:        0.5,
	})

	childResp, err := svc.EmitCommit(context.Background(), kp.AgentID(), trajectory.CommitRequest{
		TaskID:              taskID,
		ParentCommitID:      string(rootResp.EventID),
		Outcome:             event.OutcomeConverged,
		ApproachDescription: "child",
		ComputeCost:         2000,
		QualityScore:        0.9,
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
		QualityScore:        0.5,
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
