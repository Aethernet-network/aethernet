package replay

import "encoding/json"

// replayStore is the minimal persistence interface required by this package.
// *store.Store satisfies this interface via its PutReplayJob / GetReplayJob /
// AllReplayJobs / PutReplayOutcome / GetReplayOutcome / AllReplayOutcomes methods.
type replayStore interface {
	PutReplayJob(id string, data []byte) error
	GetReplayJob(id string) ([]byte, error)
	AllReplayJobs() (map[string][]byte, error)

	PutReplayOutcome(id string, data []byte) error
	GetReplayOutcome(id string) ([]byte, error)
	AllReplayOutcomes() (map[string][]byte, error)
}

// Manager provides put/get/list operations for ReplayJob and ReplayOutcome,
// layering JSON serialisation over the raw-bytes replayStore.
type Manager struct {
	store replayStore
}

// NewManager returns a Manager backed by store.
func NewManager(store replayStore) *Manager {
	return &Manager{store: store}
}

// PutJob serialises job to JSON and persists it under its ID.
func (m *Manager) PutJob(job *ReplayJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return m.store.PutReplayJob(job.ID, data)
}

// GetJob retrieves and deserialises the ReplayJob identified by id.
func (m *Manager) GetJob(id string) (*ReplayJob, error) {
	data, err := m.store.GetReplayJob(id)
	if err != nil {
		return nil, err
	}
	var job ReplayJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// AllJobs returns all persisted ReplayJobs in unspecified order.
func (m *Manager) AllJobs() ([]*ReplayJob, error) {
	blobs, err := m.store.AllReplayJobs()
	if err != nil {
		return nil, err
	}
	jobs := make([]*ReplayJob, 0, len(blobs))
	for _, data := range blobs {
		var job ReplayJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, &job)
	}
	return jobs, nil
}

// PutOutcome serialises outcome to JSON and persists it under its JobID.
func (m *Manager) PutOutcome(outcome *ReplayOutcome) error {
	data, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	return m.store.PutReplayOutcome(outcome.JobID, data)
}

// GetOutcome retrieves and deserialises the ReplayOutcome for jobID.
func (m *Manager) GetOutcome(jobID string) (*ReplayOutcome, error) {
	data, err := m.store.GetReplayOutcome(jobID)
	if err != nil {
		return nil, err
	}
	var outcome ReplayOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		return nil, err
	}
	return &outcome, nil
}

// AllOutcomes returns all persisted ReplayOutcomes in unspecified order.
func (m *Manager) AllOutcomes() ([]*ReplayOutcome, error) {
	blobs, err := m.store.AllReplayOutcomes()
	if err != nil {
		return nil, err
	}
	outcomes := make([]*ReplayOutcome, 0, len(blobs))
	for _, data := range blobs {
		var outcome ReplayOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, &outcome)
	}
	return outcomes, nil
}
