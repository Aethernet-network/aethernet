package canary

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// canaryStore is the typed persistence interface used by this package.
// In tests, a simple in-memory implementation is used.
// In production, CanaryManager wraps *store.Store to satisfy this interface.
type canaryStore interface {
	PutCanary(c *CanaryTask) error
	GetCanary(id string) (*CanaryTask, error)
	GetCanaryByTaskID(taskID string) (*CanaryTask, error)
	AllCanaries() ([]*CanaryTask, error)
}

// calibrationStore is the typed persistence interface for CalibrationSignals.
// CanaryManager implements this interface, as does the in-memory test store.
type calibrationStore interface {
	PutSignal(sig *CalibrationSignal) error
	GetSignal(id string) (*CalibrationSignal, error)
	SignalsByActor(actorID string) ([]*CalibrationSignal, error)
}

// rawCanaryBackend is the raw-bytes interface satisfied by *store.Store without
// requiring store to import the canary package.
// *store.Store satisfies this via the PutCanary / GetCanary / AllCanaries /
// PutCanaryTaskIndex / GetCanaryByTaskID / PutCalibrationSignal /
// GetCalibrationSignal / AllCalibrationSignals / CalibrationSignalsByActor
// methods added to internal/store/store.go.
type rawCanaryBackend interface {
	// canary task methods
	PutCanary(id string, data []byte) error
	GetCanary(id string) ([]byte, error)
	AllCanaries() (map[string][]byte, error)
	PutCanaryTaskIndex(taskID, canaryID string) error
	GetCanaryByTaskID(taskID string) ([]byte, error)
	// calibration signal methods
	PutCalibrationSignal(id string, data []byte) error
	GetCalibrationSignal(id string) ([]byte, error)
	AllCalibrationSignals() (map[string][]byte, error)
	CalibrationSignalsByActor(actorID string) ([][]byte, error)
}

// CanaryManager wraps a rawCanaryBackend and implements canaryStore with typed
// *CanaryTask operations. Construct one with NewCanaryManager(*store.Store).
type CanaryManager struct {
	backend rawCanaryBackend
}

// NewCanaryManager returns a CanaryManager backed by the given raw store.
// Pass a *store.Store — it satisfies rawCanaryBackend.
func NewCanaryManager(backend rawCanaryBackend) *CanaryManager {
	return &CanaryManager{backend: backend}
}

// PutCanary marshals c and persists it. When c.TaskID is set, the secondary
// taskID→canaryID index is also written. Errors are logged at slog.Error per
// CLAUDE.md conventions.
func (m *CanaryManager) PutCanary(c *CanaryTask) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("canary: marshal %s: %w", c.ID, err)
	}
	if err := m.backend.PutCanary(c.ID, data); err != nil {
		slog.Error("canary: persist failed", "id", c.ID, "err", err)
		return fmt.Errorf("canary: put %s: %w", c.ID, err)
	}
	if c.TaskID != "" {
		if err := m.backend.PutCanaryTaskIndex(c.TaskID, c.ID); err != nil {
			slog.Error("canary: persist task index failed",
				"task_id", c.TaskID, "canary_id", c.ID, "err", err)
			return fmt.Errorf("canary: put task index %s: %w", c.TaskID, err)
		}
	}
	return nil
}

// GetCanary retrieves the CanaryTask with the given ID.
func (m *CanaryManager) GetCanary(id string) (*CanaryTask, error) {
	data, err := m.backend.GetCanary(id)
	if err != nil {
		return nil, err
	}
	var c CanaryTask
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("canary: unmarshal %s: %w", id, err)
	}
	return &c, nil
}

// GetCanaryByTaskID looks up the canary associated with the given protocol task ID.
func (m *CanaryManager) GetCanaryByTaskID(taskID string) (*CanaryTask, error) {
	data, err := m.backend.GetCanaryByTaskID(taskID)
	if err != nil {
		return nil, err
	}
	var c CanaryTask
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("canary: unmarshal for task %s: %w", taskID, err)
	}
	return &c, nil
}

// AllCanaries returns all persisted CanaryTasks in unspecified order.
func (m *CanaryManager) AllCanaries() ([]*CanaryTask, error) {
	blobs, err := m.backend.AllCanaries()
	if err != nil {
		return nil, err
	}
	result := make([]*CanaryTask, 0, len(blobs))
	for _, data := range blobs {
		var c CanaryTask
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("canary: unmarshal in AllCanaries: %w", err)
		}
		result = append(result, &c)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// CalibrationSignal persistence — CanaryManager implements calibrationStore
// ---------------------------------------------------------------------------

// PutSignal marshals sig and persists it. Errors are logged at slog.Error.
func (m *CanaryManager) PutSignal(sig *CalibrationSignal) error {
	data, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("canary: marshal signal %s: %w", sig.ID, err)
	}
	if err := m.backend.PutCalibrationSignal(sig.ID, data); err != nil {
		slog.Error("canary: persist signal failed", "id", sig.ID, "err", err)
		return fmt.Errorf("canary: put signal %s: %w", sig.ID, err)
	}
	return nil
}

// GetSignal retrieves the CalibrationSignal with the given ID.
func (m *CanaryManager) GetSignal(id string) (*CalibrationSignal, error) {
	data, err := m.backend.GetCalibrationSignal(id)
	if err != nil {
		return nil, err
	}
	var sig CalibrationSignal
	if err := json.Unmarshal(data, &sig); err != nil {
		return nil, fmt.Errorf("canary: unmarshal signal %s: %w", id, err)
	}
	return &sig, nil
}

// CategoryCalibrationForActor returns calibration metrics for a specific actor
// within a single task category. It fetches all signals for the actor and
// filters to the requested category using ComputeCategoryCalibration.
//
// Returns (nil, nil) when no signals exist for the actor or none match the
// category — the nil return is a safe "no data" signal, not an error.
func (m *CanaryManager) CategoryCalibrationForActor(actorID, category string) (*CategoryCalibration, error) {
	signals, err := m.SignalsByActor(actorID)
	if err != nil {
		return nil, err
	}
	return ComputeCategoryCalibration(signals, category), nil
}

// SignalsByActor returns all CalibrationSignals for the given actorID.
func (m *CanaryManager) SignalsByActor(actorID string) ([]*CalibrationSignal, error) {
	blobs, err := m.backend.CalibrationSignalsByActor(actorID)
	if err != nil {
		return nil, err
	}
	result := make([]*CalibrationSignal, 0, len(blobs))
	for _, data := range blobs {
		var sig CalibrationSignal
		if err := json.Unmarshal(data, &sig); err != nil {
			return nil, fmt.Errorf("canary: unmarshal signal in SignalsByActor: %w", err)
		}
		result = append(result, &sig)
	}
	return result, nil
}
