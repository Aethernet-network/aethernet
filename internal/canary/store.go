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

// rawCanaryBackend is the raw-bytes interface satisfied by *store.Store without
// requiring store to import the canary package.
// *store.Store satisfies this via the PutCanary / GetCanary / AllCanaries /
// PutCanaryTaskIndex / GetCanaryByTaskID methods added to internal/store/store.go.
type rawCanaryBackend interface {
	PutCanary(id string, data []byte) error
	GetCanary(id string) ([]byte, error)
	AllCanaries() (map[string][]byte, error)
	PutCanaryTaskIndex(taskID, canaryID string) error
	GetCanaryByTaskID(taskID string) ([]byte, error)
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
