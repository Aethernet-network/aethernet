package taskverification

import (
	"context"
	"sync"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

func newTestCalibrationStore(t *testing.T, threshold int) *CalibrationStore {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCalibrationStore(db, CalibrationConfig{DefaultThreshold: threshold})
}

func TestCalibrationStore_IncrementPersists(t *testing.T) {
	s := newTestCalibrationStore(t, 100)
	ctx := context.Background()
	n, _ := s.Increment(ctx, "research", verification.FamilyDeterministicHeuristic)
	if n != 1 {
		t.Errorf("first increment = %d; want 1", n)
	}
	n, _ = s.Increment(ctx, "research", verification.FamilyDeterministicHeuristic)
	if n != 2 {
		t.Errorf("second increment = %d; want 2", n)
	}
	got, _ := s.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	if got != 2 {
		t.Errorf("Get = %d; want 2", got)
	}
}

func TestCalibrationStore_IsCalibrated_BelowThreshold(t *testing.T) {
	s := newTestCalibrationStore(t, 5)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		s.Increment(ctx, "code", verification.FamilyStatisticalStructural)
	}
	ok, _ := s.IsCalibrated(ctx, "code", verification.FamilyStatisticalStructural)
	if ok {
		t.Error("should not be calibrated at 4 < 5")
	}
}

func TestCalibrationStore_IsCalibrated_AtThreshold(t *testing.T) {
	s := newTestCalibrationStore(t, 3)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		s.Increment(ctx, "code", verification.FamilyStatisticalStructural)
	}
	ok, _ := s.IsCalibrated(ctx, "code", verification.FamilyStatisticalStructural)
	if !ok {
		t.Error("should be calibrated at count == threshold")
	}
}

func TestCalibrationStore_CategoryOverride(t *testing.T) {
	db, _ := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	defer db.Close()
	s := NewCalibrationStore(db, CalibrationConfig{
		DefaultThreshold:  100,
		CategoryOverrides: map[string]int{"code": 5},
	})
	if s.GetThreshold("code", verification.FamilyLLMSemantic) != 5 {
		t.Error("category override should take precedence")
	}
	if s.GetThreshold("research", verification.FamilyLLMSemantic) != 100 {
		t.Error("non-overridden category should use default")
	}
}

func TestCalibrationStore_FamilyOverride(t *testing.T) {
	db, _ := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	defer db.Close()
	s := NewCalibrationStore(db, CalibrationConfig{
		DefaultThreshold: 100,
		FamilyOverrides:  map[verification.FamilyID]int{verification.FamilyLLMSemantic: 10},
	})
	if s.GetThreshold("research", verification.FamilyLLMSemantic) != 10 {
		t.Error("family override should take precedence")
	}
}

func TestCalibrationStore_ConcurrentIncrements(t *testing.T) {
	s := newTestCalibrationStore(t, 1000)
	ctx := context.Background()
	var wg sync.WaitGroup
	n := 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.Increment(ctx, "research", verification.FamilyDeterministicHeuristic)
		}()
	}
	wg.Wait()
	got, _ := s.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	if got != n {
		t.Errorf("concurrent increments: got %d; want %d", got, n)
	}
}
