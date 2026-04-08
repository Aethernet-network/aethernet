package taskverification

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

const prefixCalibration = "cal:"

// CalibrationConfig holds thresholds for calibration mode.
type CalibrationConfig struct {
	// DefaultThreshold is the number of finalized rounds per (category, family)
	// before calibration completes and slashing activates.
	DefaultThreshold  int
	CategoryOverrides map[string]int
	FamilyOverrides   map[verification.FamilyID]int
}

// CalibrationStore tracks per (category × family) calibration progress.
// During calibration, votes count normally toward consensus but slashing
// is disabled. After calibration, slashing rules activate.
type CalibrationStore struct {
	db     *badger.DB
	config CalibrationConfig
	mu     sync.Mutex
}

// NewCalibrationStore creates a calibration store backed by BadgerDB.
func NewCalibrationStore(db *badger.DB, config CalibrationConfig) *CalibrationStore {
	if config.DefaultThreshold <= 0 {
		config.DefaultThreshold = 100
	}
	return &CalibrationStore{db: db, config: config}
}

func calibrationKey(category string, family verification.FamilyID) []byte {
	return []byte(prefixCalibration + category + ":" + string(family))
}

// Increment bumps the counter for (category, family) and returns the new count.
func (s *CalibrationStore) Increment(_ context.Context, category string, family verification.FamilyID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := calibrationKey(category, family)
	var newCount uint64

	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err == nil {
			_ = item.Value(func(val []byte) error {
				if len(val) == 8 {
					newCount = binary.LittleEndian.Uint64(val)
				}
				return nil
			})
		}
		newCount++
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, newCount)
		return txn.Set(key, buf)
	})
	if err != nil {
		return 0, fmt.Errorf("calibration: increment: %w", err)
	}
	return int(newCount), nil
}

// Get returns the current count for (category, family).
func (s *CalibrationStore) Get(_ context.Context, category string, family verification.FamilyID) (int, error) {
	var count uint64
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(calibrationKey(category, family))
		if err != nil {
			return nil // not found = 0
		}
		return item.Value(func(val []byte) error {
			if len(val) == 8 {
				count = binary.LittleEndian.Uint64(val)
			}
			return nil
		})
	})
	return int(count), err
}

// IsCalibrated returns true if the count exceeds the threshold.
func (s *CalibrationStore) IsCalibrated(_ context.Context, category string, family verification.FamilyID) (bool, error) {
	count, err := s.Get(context.Background(), category, family)
	if err != nil {
		return false, err
	}
	return count >= s.GetThreshold(category, family), nil
}

// GetThreshold returns the calibration threshold, checking per-category
// and per-family overrides before falling back to the default.
func (s *CalibrationStore) GetThreshold(category string, family verification.FamilyID) int {
	if v, ok := s.config.CategoryOverrides[category]; ok {
		return v
	}
	if v, ok := s.config.FamilyOverrides[family]; ok {
		return v
	}
	return s.config.DefaultThreshold
}
