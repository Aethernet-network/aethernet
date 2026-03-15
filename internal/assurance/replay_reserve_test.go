package assurance

import (
	"errors"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// In-memory store stub
// ---------------------------------------------------------------------------

type memReserveStore struct {
	mu   sync.Mutex
	data map[string]uint64
	// putErr and getErr inject errors for failure-path tests.
	putErr error
}

func newMemReserveStore() *memReserveStore {
	return &memReserveStore{data: make(map[string]uint64)}
}

func (m *memReserveStore) PutReplayReserve(category string, balance uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.data[category] = balance
	return nil
}

func (m *memReserveStore) GetReplayReserve(category string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[category], nil
}

// reserveCfg returns a minimal AssuranceConfig for reserve tests.
func reserveCfg() *config.AssuranceConfig {
	return &config.AssuranceConfig{
		MinReplayPayout:             5_000_000,
		ReplayReserveCircuitBreaker: 0.20,
	}
}

// ---------------------------------------------------------------------------
// Test Accrue increases balance
// ---------------------------------------------------------------------------

func TestReplayReserve_Accrue_IncreasesBalance(t *testing.T) {
	r := NewReplayReserve(reserveCfg(), nil)
	if err := r.Accrue("code", 1_000_000); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	if err := r.Accrue("code", 2_000_000); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	if bal := r.Balance("code"); bal != 3_000_000 {
		t.Errorf("Balance: got %d, want 3_000_000", bal)
	}
}

// ---------------------------------------------------------------------------
// Test Draw decreases balance
// ---------------------------------------------------------------------------

func TestReplayReserve_Draw_DecreasesBalance(t *testing.T) {
	r := NewReplayReserve(reserveCfg(), nil)
	_ = r.Accrue("code", 10_000_000)
	drawn, err := r.Draw("code", 3_000_000)
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if drawn != 3_000_000 {
		t.Errorf("drawn: got %d, want 3_000_000", drawn)
	}
	if bal := r.Balance("code"); bal != 7_000_000 {
		t.Errorf("Balance after draw: got %d, want 7_000_000", bal)
	}
}

// ---------------------------------------------------------------------------
// Test Draw with insufficient balance → returns available amount
// ---------------------------------------------------------------------------

func TestReplayReserve_Draw_InsufficientBalance(t *testing.T) {
	r := NewReplayReserve(reserveCfg(), nil)
	_ = r.Accrue("code", 2_000_000)

	drawn, err := r.Draw("code", 5_000_000)
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if drawn != 2_000_000 {
		t.Errorf("drawn: got %d, want 2_000_000 (all available)", drawn)
	}
	if bal := r.Balance("code"); bal != 0 {
		t.Errorf("Balance should be 0 after exhaustion, got %d", bal)
	}
}

// ---------------------------------------------------------------------------
// Test Balance returns correct value
// ---------------------------------------------------------------------------

func TestReplayReserve_Balance(t *testing.T) {
	r := NewReplayReserve(reserveCfg(), nil)
	// Unknown category → 0.
	if bal := r.Balance("unknown"); bal != 0 {
		t.Errorf("expected 0 for unknown category, got %d", bal)
	}
	_ = r.Accrue("data", 7_777_777)
	if bal := r.Balance("data"); bal != 7_777_777 {
		t.Errorf("Balance: got %d, want 7_777_777", bal)
	}
}

// ---------------------------------------------------------------------------
// Test CategoryHealthy: above threshold → true
// ---------------------------------------------------------------------------

func TestReplayReserve_CategoryHealthy_AboveThreshold(t *testing.T) {
	cfg := reserveCfg() // MinReplayPayout = 5_000_000; target = 50_000_000; threshold = 10_000_000
	r := NewReplayReserve(cfg, nil)
	_ = r.Accrue("code", 50_000_000)
	if !r.CategoryHealthy("code") {
		t.Error("expected CategoryHealthy=true when balance ≥ threshold")
	}
}

// ---------------------------------------------------------------------------
// Test CategoryHealthy: below threshold → false
// ---------------------------------------------------------------------------

func TestReplayReserve_CategoryHealthy_BelowThreshold(t *testing.T) {
	cfg := reserveCfg() // target = 50_000_000; threshold = 50_000_000 * 0.20 = 10_000_000
	r := NewReplayReserve(cfg, nil)
	_ = r.Accrue("code", 5_000_000) // below threshold
	if r.CategoryHealthy("code") {
		t.Error("expected CategoryHealthy=false when balance < threshold")
	}
}

// ---------------------------------------------------------------------------
// Test TopUp increases balance (bootstrap path)
// ---------------------------------------------------------------------------

func TestReplayReserve_TopUp(t *testing.T) {
	r := NewReplayReserve(reserveCfg(), nil)
	if err := r.TopUp("code", 20_000_000); err != nil {
		t.Fatalf("TopUp: %v", err)
	}
	if bal := r.Balance("code"); bal != 20_000_000 {
		t.Errorf("Balance after TopUp: got %d, want 20_000_000", bal)
	}
	// Second TopUp adds to existing balance.
	_ = r.TopUp("code", 5_000_000)
	if bal := r.Balance("code"); bal != 25_000_000 {
		t.Errorf("Balance after second TopUp: got %d, want 25_000_000", bal)
	}
}

// ---------------------------------------------------------------------------
// Test store round-trip: persist and reload reserve balance
// ---------------------------------------------------------------------------

func TestReplayReserve_StoreRoundTrip(t *testing.T) {
	s := newMemReserveStore()
	r := NewReplayReserve(reserveCfg(), s)

	_ = r.Accrue("code", 12_345_678)

	// Verify store received the value.
	s.mu.Lock()
	stored := s.data["code"]
	s.mu.Unlock()
	if stored != 12_345_678 {
		t.Errorf("store balance: got %d, want 12_345_678", stored)
	}
}

// ---------------------------------------------------------------------------
// Test LoadFromStore restores balances across restart
// ---------------------------------------------------------------------------

func TestReplayReserve_LoadFromStore(t *testing.T) {
	s := newMemReserveStore()
	// Seed the store with known balances.
	s.data["code"] = 11_000_000
	s.data["data"] = 22_000_000

	r, err := LoadFromStore(reserveCfg(), s, []string{"code", "data", "writing"})
	if err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}
	if bal := r.Balance("code"); bal != 11_000_000 {
		t.Errorf("code balance: got %d, want 11_000_000", bal)
	}
	if bal := r.Balance("data"); bal != 22_000_000 {
		t.Errorf("data balance: got %d, want 22_000_000", bal)
	}
	// Unknown category starts at zero even if listed.
	if bal := r.Balance("writing"); bal != 0 {
		t.Errorf("writing balance: got %d, want 0", bal)
	}
}

// ---------------------------------------------------------------------------
// Test Draw rolls back in-memory state on store failure
// ---------------------------------------------------------------------------

func TestReplayReserve_Draw_RollsBackOnStoreFailure(t *testing.T) {
	s := newMemReserveStore()
	r := NewReplayReserve(reserveCfg(), s)
	_ = r.Accrue("code", 10_000_000) // stored successfully

	// Inject store error.
	s.mu.Lock()
	s.putErr = errors.New("disk full")
	s.mu.Unlock()

	drawn, err := r.Draw("code", 3_000_000)
	if err == nil {
		t.Fatal("expected error from Draw when store fails")
	}
	if drawn != 0 {
		t.Errorf("drawn should be 0 on failure, got %d", drawn)
	}
	// In-memory balance should be unchanged.
	s.mu.Lock()
	s.putErr = nil // clear the error so Balance can persist
	s.mu.Unlock()
	if bal := r.Balance("code"); bal != 10_000_000 {
		t.Errorf("balance should be unchanged after failed draw, got %d", bal)
	}
}

// ---------------------------------------------------------------------------
// Test IsHealthy: at exactly threshold → true
// ---------------------------------------------------------------------------

func TestReplayReserve_IsHealthy_AtThreshold(t *testing.T) {
	cfg := reserveCfg()
	r := NewReplayReserve(cfg, nil)
	targetBalance := uint64(100_000_000)
	threshold := uint64(cfg.ReplayReserveCircuitBreaker * float64(targetBalance)) // 20_000_000
	_ = r.Accrue("code", threshold)
	if !r.IsHealthy("code", targetBalance) {
		t.Error("expected IsHealthy=true at exactly threshold")
	}
}
