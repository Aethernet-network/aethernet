package reputation

import (
	"testing"
)

func TestRecordCompletion(t *testing.T) {
	rm := NewReputationManager()
	rm.RecordCompletion("alice", "writing", 50_000, 0.9, 30.0)

	rep := rm.GetReputation("alice")
	if rep.TotalCompleted != 1 {
		t.Errorf("TotalCompleted = %d; want 1", rep.TotalCompleted)
	}
	if rep.TotalEarned != 50_000 {
		t.Errorf("TotalEarned = %d; want 50000", rep.TotalEarned)
	}
	cat := rep.Categories["writing"]
	if cat == nil {
		t.Fatal("expected writing category to exist")
	}
	if cat.TasksCompleted != 1 {
		t.Errorf("cat.TasksCompleted = %d; want 1", cat.TasksCompleted)
	}
	if cat.TotalValueEarned != 50_000 {
		t.Errorf("cat.TotalValueEarned = %d; want 50000", cat.TotalValueEarned)
	}
	if cat.AvgScore < 0.89 || cat.AvgScore > 0.91 {
		t.Errorf("cat.AvgScore = %.3f; want ~0.9", cat.AvgScore)
	}
	if cat.AvgDeliveryTime < 29.9 || cat.AvgDeliveryTime > 30.1 {
		t.Errorf("cat.AvgDeliveryTime = %.3f; want ~30.0", cat.AvgDeliveryTime)
	}
	if rep.TopCategory != "writing" {
		t.Errorf("TopCategory = %q; want writing", rep.TopCategory)
	}
}

func TestRecordCompletion_MultipleCategories(t *testing.T) {
	rm := NewReputationManager()
	rm.RecordCompletion("bob", "writing", 30_000, 0.8, 20.0)
	rm.RecordCompletion("bob", "code", 60_000, 0.95, 90.0)
	rm.RecordCompletion("bob", "writing", 35_000, 0.85, 25.0)

	rep := rm.GetReputation("bob")
	if rep.TotalCompleted != 3 {
		t.Errorf("TotalCompleted = %d; want 3", rep.TotalCompleted)
	}

	writing := rep.Categories["writing"]
	if writing == nil {
		t.Fatal("expected writing category")
	}
	if writing.TasksCompleted != 2 {
		t.Errorf("writing.TasksCompleted = %d; want 2", writing.TasksCompleted)
	}

	code := rep.Categories["code"]
	if code == nil {
		t.Fatal("expected code category")
	}
	if code.TasksCompleted != 1 {
		t.Errorf("code.TasksCompleted = %d; want 1", code.TasksCompleted)
	}

	// writing has 2 completions vs code's 1 → TopCategory = writing
	if rep.TopCategory != "writing" {
		t.Errorf("TopCategory = %q; want writing", rep.TopCategory)
	}
}

func TestRecordFailure(t *testing.T) {
	rm := NewReputationManager()
	rm.RecordCompletion("charlie", "ml", 40_000, 0.7, 60.0)
	rm.RecordFailure("charlie", "ml")

	rep := rm.GetReputation("charlie")
	if rep.TotalFailed != 1 {
		t.Errorf("TotalFailed = %d; want 1", rep.TotalFailed)
	}
	if rep.TotalCompleted != 1 {
		t.Errorf("TotalCompleted = %d; want 1", rep.TotalCompleted)
	}

	cat := rep.Categories["ml"]
	if cat.TasksFailed != 1 {
		t.Errorf("cat.TasksFailed = %d; want 1", cat.TasksFailed)
	}
}

func TestCompletionRate(t *testing.T) {
	cr := &CategoryRecord{TasksCompleted: 8, TasksFailed: 2}
	rate := cr.CompletionRate()
	if rate < 0.79 || rate > 0.81 {
		t.Errorf("CompletionRate = %.3f; want 0.8", rate)
	}

	// Zero total
	empty := &CategoryRecord{}
	if empty.CompletionRate() != 0 {
		t.Errorf("empty CompletionRate = %f; want 0", empty.CompletionRate())
	}
}

func TestOverallScore(t *testing.T) {
	rm := NewReputationManager()

	// 1 completion, 0 failures → completionRate=1.0, volumeWeight=0.01
	// OverallScore = 1.0 * 0.01 * 100 = 1.0
	// Use verificationScore=0.5 to avoid triggering the first-task boost (>0.8 threshold).
	rm.RecordCompletion("agent-a", "writing", 1000, 0.5, 10.0)
	rep := rm.GetReputation("agent-a")
	if rep.OverallScore < 0.9 || rep.OverallScore > 1.1 {
		t.Errorf("OverallScore = %.3f; want ~1.0 for 1 completion", rep.OverallScore)
	}

	// 100 completions, 0 failures → completionRate=1.0, volumeWeight=1.0
	// OverallScore = 100.0
	rm2 := NewReputationManager()
	for i := 0; i < 100; i++ {
		rm2.RecordCompletion("agent-b", "code", 1000, 1.0, 5.0)
	}
	rep2 := rm2.GetReputation("agent-b")
	if rep2.OverallScore < 99.9 || rep2.OverallScore > 100.1 {
		t.Errorf("OverallScore = %.3f; want 100.0 for 100 completions", rep2.OverallScore)
	}
}

func TestRankByCategory(t *testing.T) {
	rm := NewReputationManager()

	// Agent a: 10 completions × 0.9 = 9.0
	for i := 0; i < 10; i++ {
		rm.RecordCompletion("agent-a", "writing", 1000, 0.9, 10.0)
	}
	// Agent b: 5 completions × 0.8 = 4.0
	for i := 0; i < 5; i++ {
		rm.RecordCompletion("agent-b", "writing", 1000, 0.8, 10.0)
	}
	// Agent c: 20 completions × 0.3 = 6.0
	for i := 0; i < 20; i++ {
		rm.RecordCompletion("agent-c", "writing", 1000, 0.3, 10.0)
	}
	// Agent d has different category — should not appear
	rm.RecordCompletion("agent-d", "code", 1000, 1.0, 5.0)

	rankings := rm.RankByCategory("writing", 10)
	if len(rankings) != 3 {
		t.Fatalf("expected 3 ranked agents, got %d", len(rankings))
	}
	// Expected order: a(9.0) > c(6.0) > b(4.0)
	if string(rankings[0].AgentID) != "agent-a" {
		t.Errorf("rank 1 = %s; want agent-a", rankings[0].AgentID)
	}
	if string(rankings[1].AgentID) != "agent-c" {
		t.Errorf("rank 2 = %s; want agent-c", rankings[1].AgentID)
	}
	if string(rankings[2].AgentID) != "agent-b" {
		t.Errorf("rank 3 = %s; want agent-b", rankings[2].AgentID)
	}
}

func TestGetCategoryRecord_Empty(t *testing.T) {
	rm := NewReputationManager()

	// Unknown agent
	cr := rm.GetCategoryRecord("unknown-agent", "writing")
	if cr.TasksCompleted != 0 {
		t.Errorf("expected zero TasksCompleted, got %d", cr.TasksCompleted)
	}
	if cr.Category != "writing" {
		t.Errorf("Category = %q; want writing", cr.Category)
	}

	// Known agent, unknown category
	rm.RecordCompletion("alice", "code", 1000, 0.9, 10.0)
	cr2 := rm.GetCategoryRecord("alice", "writing")
	if cr2.TasksCompleted != 0 {
		t.Errorf("expected zero TasksCompleted for unknown category, got %d", cr2.TasksCompleted)
	}
}

func TestAvgScore_Rolling(t *testing.T) {
	rm := NewReputationManager()

	// First completion: avg = 0.8
	rm.RecordCompletion("dave", "nlp", 1000, 0.8, 10.0)
	cat := rm.GetCategoryRecord("dave", "nlp")
	if cat.AvgScore < 0.79 || cat.AvgScore > 0.81 {
		t.Errorf("after 1st: AvgScore = %.4f; want 0.8", cat.AvgScore)
	}

	// Second completion: avg = (0.8 + 1.0) / 2 = 0.9
	rm.RecordCompletion("dave", "nlp", 1000, 1.0, 10.0)
	cat2 := rm.GetCategoryRecord("dave", "nlp")
	if cat2.AvgScore < 0.89 || cat2.AvgScore > 0.91 {
		t.Errorf("after 2nd: AvgScore = %.4f; want 0.9", cat2.AvgScore)
	}

	// Third completion: avg = (0.8*2/3 + 1.0/3) = 0.8667 for running total... wait
	// Running avg: after 2 completions, avg = 0.9
	// Third score = 0.6: new avg = (0.9 * 2 + 0.6) / 3 = 2.4/3 = 0.8
	rm.RecordCompletion("dave", "nlp", 1000, 0.6, 10.0)
	cat3 := rm.GetCategoryRecord("dave", "nlp")
	if cat3.AvgScore < 0.79 || cat3.AvgScore > 0.81 {
		t.Errorf("after 3rd: AvgScore = %.4f; want ~0.8", cat3.AvgScore)
	}
}
