// Package sdk provides a Go client for the AetherNet node HTTP API.
//
// The SDK communicates exclusively over HTTP and depends only on the Go
// standard library, making it safe to embed in any agent implementation
// without pulling in AetherNet internals.
//
// Quick start:
//
//	client := sdk.New("http://localhost:8338", nil)
//	agentID, err := client.Register(nil)
//	eventID, err := client.Generate(sdk.GenerationRequest{
//	    ClaimedValue:    5000,
//	    EvidenceHash:    "sha256:...",
//	    TaskDescription: "inference run",
//	    StakeAmount:     1000,
//	})
package sdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an AetherNet API client bound to a single node endpoint.
type Client struct {
	// BaseURL is the scheme+host of the target node (e.g. "http://localhost:8338").
	// No trailing slash.
	BaseURL string

	// HTTPClient is the underlying transport. http.DefaultClient is used when nil.
	HTTPClient *http.Client
}

// New creates a Client that communicates with the node at baseURL.
// Pass nil for httpClient to use http.DefaultClient.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{BaseURL: baseURL, HTTPClient: httpClient}
}

// ---------------------------------------------------------------------------
// SDK-local types (no dependency on internal packages)
// ---------------------------------------------------------------------------

// Capability represents a demonstrated skill domain in an agent's fingerprint.
type Capability struct {
	Domain        string    `json:"domain"`
	Confidence    uint64    `json:"confidence"`
	EvidenceCount uint64    `json:"evidence_count"`
	LastVerified  time.Time `json:"last_verified"`
}

// AgentProfile is the full capability fingerprint returned by the API.
type AgentProfile struct {
	AgentID              string       `json:"agent_id"`
	Capabilities         []Capability `json:"capabilities"`
	TasksCompleted       uint64       `json:"tasks_completed"`
	TasksFailed          uint64       `json:"tasks_failed"`
	TotalValueGenerated  uint64       `json:"total_value_generated"`
	OptimisticTrustLimit uint64       `json:"optimistic_trust_limit"`
	ReputationScore      uint64       `json:"reputation_score"`
	StakedAmount         uint64       `json:"staked_amount"`
	FingerprintVersion   uint64       `json:"fingerprint_version"`
	FingerprintHash      string       `json:"fingerprint_hash"`
}

// BalanceResponse is the spendable balance for an agent.
type BalanceResponse struct {
	AgentID  string `json:"agent_id"`
	Balance  uint64 `json:"balance"`
	Currency string `json:"currency"`
}

// StatusResponse is a point-in-time snapshot of the node's health.
type StatusResponse struct {
	AgentID     string  `json:"agent_id"`
	Version     string  `json:"version"`
	Peers       int     `json:"peers"`
	DAGSize     int     `json:"dag_size"`
	OCSPending  int     `json:"ocs_pending"`
	SupplyRatio float64 `json:"supply_ratio"`
}

// TipsResponse holds the current DAG frontier event IDs.
type TipsResponse struct {
	Tips []string `json:"tips"`
}

// EventResponse is a DAG event returned by the API.
type EventResponse struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	AgentID         string `json:"agent_id"`
	CausalTimestamp uint64 `json:"causal_timestamp"`
	StakeAmount     uint64 `json:"stake_amount"`
	SettlementState string `json:"settlement_state"`
}

// TransferRequest is the input to Client.Transfer.
type TransferRequest struct {
	ToAgent     string   `json:"to_agent"`
	Amount      uint64   `json:"amount"`
	Currency    string   `json:"currency,omitempty"`
	Memo        string   `json:"memo,omitempty"`
	StakeAmount uint64   `json:"stake_amount"`
	CausalRefs  []string `json:"causal_refs,omitempty"`
}

// GenerationRequest is the input to Client.Generate.
type GenerationRequest struct {
	BeneficiaryAgent string   `json:"beneficiary_agent,omitempty"`
	ClaimedValue     uint64   `json:"claimed_value"`
	EvidenceHash     string   `json:"evidence_hash"`
	TaskDescription  string   `json:"task_description,omitempty"`
	StakeAmount      uint64   `json:"stake_amount"`
	CausalRefs       []string `json:"causal_refs,omitempty"`
}

// VerifyRequest is the input to Client.Verify.
type VerifyRequest struct {
	EventID       string `json:"event_id"`
	Verdict       bool   `json:"verdict"`
	VerifiedValue uint64 `json:"verified_value,omitempty"`
}

// VerifyResponse is the result returned by Client.Verify.
type VerifyResponse struct {
	EventID string `json:"event_id"`
	Verdict bool   `json:"verdict"`
	Status  string `json:"status"` // "settled" or "adjusted"
}

// PendingItem is an event awaiting OCS verification, as returned by Client.Pending.
// JSON field names match the Go default (capitalized) used by ocs.PendingItem.
type PendingItem struct {
	EventID      string    `json:"EventID"`
	EventType    string    `json:"EventType"`
	AgentID      string    `json:"AgentID"`
	Amount       uint64    `json:"Amount"`
	OptimisticAt time.Time `json:"OptimisticAt"`
	Deadline     int64     `json:"Deadline"` // nanoseconds
}

// StakeRequest is the input to Client.Stake and Client.Unstake.
type StakeRequest struct {
	AgentID string `json:"agent_id"`
	Amount  uint64 `json:"amount"`
}

// ServiceListing describes a service an agent advertises via the registry.
type ServiceListing struct {
	AgentID     string   `json:"agent_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	PriceAET    uint64   `json:"price_aet"`
	Endpoint    string   `json:"endpoint,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	Active      bool     `json:"active"`
}

// StakeInfoResponse is the staking state for a single agent.
type StakeInfoResponse struct {
	AgentID         string `json:"agent_id"`
	StakedAmount    uint64 `json:"staked_amount"`
	StakedSince     int64  `json:"staked_since"`
	TrustMultiplier uint64 `json:"trust_multiplier"`
	TrustLimit      uint64 `json:"trust_limit"`
	TasksCompleted  uint64 `json:"tasks_completed"`
	EffectiveTasks  uint64 `json:"effective_tasks"`
	LastActivity    int64  `json:"last_activity"`
	DaysStaked      uint64 `json:"days_staked"`
}

// EconomicsResponse is a snapshot of the network's token economics.
type EconomicsResponse struct {
	TotalSupply         uint64 `json:"total_supply"`
	CirculatingSupply   uint64 `json:"circulating_supply"`
	OnboardingPoolTotal uint64 `json:"onboarding_pool_total"`
	OnboardingMaxAgents uint64 `json:"onboarding_max_agents"`
	OnboardingAllocated uint64 `json:"onboarding_allocated"`
	TotalCollected      uint64 `json:"total_collected"`
	TotalBurned         uint64 `json:"total_burned"`
	TreasuryAccrued     uint64 `json:"treasury_accrued"`
	FeeBasisPoints      uint64 `json:"fee_basis_points"`
}

// ---------------------------------------------------------------------------
// Internal transport helpers
// ---------------------------------------------------------------------------

// do executes an HTTP request. body is JSON-encoded when non-nil.
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("sdk: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("sdk: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sdk: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// checkAndDecode verifies that resp has an expected status code and decodes
// the JSON body into result. On unexpected status it extracts the error message.
func checkAndDecode[T any](resp *http.Response, successCodes ...int) (T, error) {
	var result T
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, fmt.Errorf("sdk: read response body: %w", err)
	}

	for _, code := range successCodes {
		if resp.StatusCode == code {
			if err := json.Unmarshal(body, &result); err != nil {
				return result, fmt.Errorf("sdk: decode response: %w", err)
			}
			return result, nil
		}
	}

	// Error path — try to extract the "error" field.
	var apiErr struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &apiErr)
	msg := apiErr.Error
	if msg == "" {
		msg = string(body)
	}
	return result, fmt.Errorf("sdk: api error %d: %s", resp.StatusCode, msg)
}

// ---------------------------------------------------------------------------
// API methods
// ---------------------------------------------------------------------------

// Register registers the node's own agent in the identity registry.
// Returns the agent's ID. Idempotent — safe to call multiple times.
func (c *Client) Register(capabilities []Capability) (string, error) {
	resp, err := c.do(http.MethodPost, "/v1/agents", map[string]any{
		"capabilities": capabilities,
	})
	if err != nil {
		return "", err
	}

	result, err := checkAndDecode[struct {
		AgentID string `json:"agent_id"`
	}](resp, http.StatusCreated, http.StatusOK)
	if err != nil {
		return "", err
	}
	return result.AgentID, nil
}

// Transfer submits a Transfer event and returns the event ID.
func (c *Client) Transfer(req TransferRequest) (string, error) {
	resp, err := c.do(http.MethodPost, "/v1/transfer", req)
	if err != nil {
		return "", err
	}

	result, err := checkAndDecode[struct {
		EventID string `json:"event_id"`
	}](resp, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return result.EventID, nil
}

// Generate submits a Generation event and returns the event ID.
func (c *Client) Generate(req GenerationRequest) (string, error) {
	resp, err := c.do(http.MethodPost, "/v1/generation", req)
	if err != nil {
		return "", err
	}

	result, err := checkAndDecode[struct {
		EventID string `json:"event_id"`
	}](resp, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return result.EventID, nil
}

// Balance returns the spendable balance for agentID.
func (c *Client) Balance(agentID string) (*BalanceResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/agents/"+agentID+"/balance", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[BalanceResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Profile returns the capability fingerprint for agentID.
func (c *Client) Profile(agentID string) (*AgentProfile, error) {
	resp, err := c.do(http.MethodGet, "/v1/agents/"+agentID, nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[AgentProfile](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetEvent returns the DAG event for eventID.
func (c *Client) GetEvent(eventID string) (*EventResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/events/"+eventID, nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[EventResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Status returns a health snapshot of the node.
func (c *Client) Status() (*StatusResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/status", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[StatusResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Tips returns the current DAG tip event IDs.
func (c *Client) Tips() (*TipsResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/dag/tips", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[TipsResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Agents returns all registered agent profiles.
func (c *Client) Agents() ([]AgentProfile, error) {
	resp, err := c.do(http.MethodGet, "/v1/agents", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[[]AgentProfile](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Verify submits a verification verdict for a pending OCS event.
// Returns an error (wrapping the API error message) if the event is not pending.
func (c *Client) Verify(req VerifyRequest) (*VerifyResponse, error) {
	resp, err := c.do(http.MethodPost, "/v1/verify", req)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[VerifyResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Pending returns all events currently awaiting OCS verification.
func (c *Client) Pending() ([]PendingItem, error) {
	resp, err := c.do(http.MethodGet, "/v1/pending", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[[]PendingItem](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Stake stakes tokens for the given agent. Returns the updated staking state.
func (c *Client) Stake(req StakeRequest) (*StakeInfoResponse, error) {
	resp, err := c.do(http.MethodPost, "/v1/stake", req)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[StakeInfoResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Unstake removes staked tokens for the given agent.
// Returns an error (wrapping the API error message) if the agent has
// insufficient staked balance.
func (c *Client) Unstake(req StakeRequest) (*StakeInfoResponse, error) {
	resp, err := c.do(http.MethodPost, "/v1/unstake", req)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[StakeInfoResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// StakeInfo returns the staking state for agentID.
func (c *Client) StakeInfo(agentID string) (*StakeInfoResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/agents/"+agentID+"/stake", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[StakeInfoResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Economics returns a snapshot of the network's token economics.
func (c *Client) Economics() (*EconomicsResponse, error) {
	resp, err := c.do(http.MethodGet, "/v1/economics", nil)
	if err != nil {
		return nil, err
	}

	result, err := checkAndDecode[EconomicsResponse](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// RegisterService publishes or updates a service listing for the node's agent.
// The node API populates agent_id automatically from its own identity.
func (c *Client) RegisterService(listing ServiceListing) error {
	resp, err := c.do(http.MethodPost, "/v1/registry", listing)
	if err != nil {
		return err
	}
	_, err = checkAndDecode[ServiceListing](resp, http.StatusCreated)
	return err
}

// SearchServices finds active service listings matching query and/or category.
// Pass empty strings to list all; limit=0 uses the server default (20).
func (c *Client) SearchServices(query string, category string, limit int) ([]ServiceListing, error) {
	path := "/v1/registry/search"
	sep := "?"
	if query != "" {
		path += sep + "q=" + query
		sep = "&"
	}
	if category != "" {
		path += sep + "category=" + category
		sep = "&"
	}
	if limit > 0 {
		path += sep + "limit=" + fmt.Sprintf("%d", limit)
	}

	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[[]ServiceListing](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetService returns the service listing for the given agentID.
func (c *Client) GetService(agentID string) (*ServiceListing, error) {
	resp, err := c.do(http.MethodGet, "/v1/registry/"+agentID, nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[ServiceListing](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ListCategories returns a map of category name to active listing count.
func (c *Client) ListCategories() (map[string]int, error) {
	resp, err := c.do(http.MethodGet, "/v1/registry/categories", nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[map[string]int](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Task marketplace
// ---------------------------------------------------------------------------

// EvidenceScore mirrors evidence.Score without importing internal packages.
type EvidenceScore struct {
	Relevance    float64 `json:"relevance"`
	Completeness float64 `json:"completeness"`
	Quality      float64 `json:"quality"`
	Overall      float64 `json:"overall"`
}

// Evidence captures the structured output of AI work submitted for a task.
// Construct with NewEvidence for automatic hash computation.
type Evidence struct {
	Hash          string            `json:"hash"`
	OutputType    string            `json:"output_type"`
	OutputSize    uint64            `json:"output_size"`
	Summary       string            `json:"summary"`
	InputHash     string            `json:"input_hash,omitempty"`
	Metrics       map[string]string `json:"metrics,omitempty"`
	OutputPreview string            `json:"output_preview,omitempty"`
	OutputURL     string            `json:"output_url,omitempty"`
}

// NewEvidence creates Evidence for output bytes, computing the hash automatically.
// outputType is one of: "text", "json", "code", "data", "image".
func NewEvidence(output []byte, outputType, summary string) *Evidence {
	h := sha256.Sum256(output)
	preview := ""
	if len(output) > 500 {
		preview = string(output[:500])
	} else {
		preview = string(output)
	}
	return &Evidence{
		Hash:          "sha256:" + hex.EncodeToString(h[:]),
		OutputType:    outputType,
		OutputSize:    uint64(len(output)),
		Summary:       summary,
		OutputPreview: preview,
	}
}

// Task is a task marketplace posting as returned by the API.
type Task struct {
	ID                string         `json:"id"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	Category          string         `json:"category"`
	PosterID          string         `json:"poster_id"`
	ClaimerID         string         `json:"claimer_id,omitempty"`
	Budget            uint64         `json:"budget"`
	Status            string         `json:"status"`
	ResultHash        string         `json:"result_hash,omitempty"`
	ResultNote        string         `json:"result_note,omitempty"`
	ResultURI         string         `json:"result_uri,omitempty"`
	VerificationScore *EvidenceScore `json:"verification_score,omitempty"`
	PostedAt          int64          `json:"posted_at"`
	ClaimedAt         int64          `json:"claimed_at,omitempty"`
	SubmittedAt       int64          `json:"submitted_at,omitempty"`
	CompletedAt       int64          `json:"completed_at,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ParentTaskID string   `json:"parent_task_id,omitempty"`
	SubtaskIDs   []string `json:"subtask_ids,omitempty"`
	IsSubtask    bool     `json:"is_subtask,omitempty"`
}

// Match is a ranked discovery result from GET /v1/discover.
type Match struct {
	AgentID         string  `json:"agent_id"`
	ServiceName     string  `json:"service_name"`
	Category        string  `json:"category"`
	Price           uint64  `json:"price_aet"`
	RelevanceScore  float64 `json:"relevance_score"`
	ReputationScore float64 `json:"reputation_score"`
	CompletionRate  float64 `json:"completion_rate"`
	TasksCompleted  uint64  `json:"tasks_completed"`
	AvgDelivery     float64 `json:"avg_delivery_secs"`
	OverallRank     float64 `json:"overall_rank"`
}

// TaskStats holds aggregate marketplace statistics.
type TaskStats struct {
	TotalTasks     int    `json:"total_tasks"`
	OpenTasks      int    `json:"open_tasks"`
	ClaimedTasks   int    `json:"claimed_tasks"`
	SubmittedTasks int    `json:"submitted_tasks"`
	CompletedTasks int    `json:"completed_tasks"`
	DisputedTasks  int    `json:"disputed_tasks"`
	CancelledTasks int    `json:"cancelled_tasks"`
	TotalBudget    uint64 `json:"total_budget"`
}

// PostTask posts a new task and returns it. The budget is escrowed from
// the poster's balance immediately.
func (c *Client) PostTask(title, description, category string, budget uint64) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks", map[string]any{
		"title":       title,
		"description": description,
		"category":    category,
		"budget":      budget,
	})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// BrowseTasks lists tasks with optional status and category filters.
// Pass empty strings to match all. limit=0 returns all results.
func (c *Client) BrowseTasks(status, category string, limit int) ([]Task, error) {
	path := "/v1/tasks"
	sep := "?"
	if status != "" {
		path += sep + "status=" + status
		sep = "&"
	}
	if category != "" {
		path += sep + "category=" + category
		sep = "&"
	}
	if limit > 0 {
		path += sep + fmt.Sprintf("limit=%d", limit)
	}
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return checkAndDecode[[]Task](resp, http.StatusOK)
}

// GetTask returns a single task by ID.
func (c *Client) GetTask(taskID string) (*Task, error) {
	resp, err := c.do(http.MethodGet, "/v1/tasks/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ClaimTask assigns a task to the node's own agent. Returns the updated task.
func (c *Client) ClaimTask(taskID string) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+taskID+"/claim", map[string]any{})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// SubmitTaskResult records the worker's result for a claimed task.
// Pass an optional Evidence value for quality scoring; omit for legacy hash-only
// submission (backward compatible — existing callers need no changes).
// Returns the updated task.
func (c *Client) SubmitTaskResult(taskID, resultHash string, ev ...*Evidence) (*Task, error) {
	body := map[string]any{
		"result_hash": resultHash,
	}
	if len(ev) > 0 && ev[0] != nil {
		body["evidence"] = ev[0]
	}
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+taskID+"/submit", body)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ApproveTask approves a submitted task, releasing the budget to the worker.
// Returns the updated task.
func (c *Client) ApproveTask(taskID string) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+taskID+"/approve", map[string]any{})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// DisputeTask moves a submitted task into Disputed state.
// Returns the updated task.
func (c *Client) DisputeTask(taskID string) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+taskID+"/dispute", map[string]any{})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// CancelTask cancels an open task, refunding the budget to the poster.
// Returns the updated task.
func (c *Client) CancelTask(taskID string) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+taskID+"/cancel", map[string]any{})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// MyTasks returns all tasks where the given agentID is poster or claimer.
func (c *Client) MyTasks(agentID string) ([]Task, error) {
	resp, err := c.do(http.MethodGet, "/v1/tasks/agent/"+agentID, nil)
	if err != nil {
		return nil, err
	}
	return checkAndDecode[[]Task](resp, http.StatusOK)
}

// TaskStats returns aggregate marketplace statistics.
func (c *Client) TaskStats() (*TaskStats, error) {
	resp, err := c.do(http.MethodGet, "/v1/tasks/stats", nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[TaskStats](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// Reputation
// ---------------------------------------------------------------------------

// CategoryRecord mirrors reputation.CategoryRecord without importing internals.
type CategoryRecord struct {
	Category         string  `json:"category"`
	TasksCompleted   uint64  `json:"tasks_completed"`
	TasksFailed      uint64  `json:"tasks_failed"`
	TotalValueEarned uint64  `json:"total_value_earned"`
	AvgScore         float64 `json:"avg_score"`
	AvgDeliveryTime  float64 `json:"avg_delivery_secs"`
	LastActive       int64   `json:"last_active"`
}

// AgentReputation mirrors reputation.AgentReputation without importing internals.
type AgentReputation struct {
	AgentID        string                     `json:"agent_id"`
	OverallScore   float64                    `json:"overall_score"`
	TotalCompleted uint64                     `json:"total_completed"`
	TotalFailed    uint64                     `json:"total_failed"`
	TotalEarned    uint64                     `json:"total_earned"`
	Categories     map[string]*CategoryRecord `json:"categories"`
	TopCategory    string                     `json:"top_category"`
	MemberSince    int64                      `json:"member_since"`
}

// GetReputation returns the full category-level reputation profile for agentID.
func (c *Client) GetReputation(agentID string) (*AgentReputation, error) {
	resp, err := c.do(http.MethodGet, "/v1/agents/"+agentID+"/reputation", nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[AgentReputation](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetCategoryRankings returns agents ranked by performance in the given category.
// limit=0 uses the server default (10).
func (c *Client) GetCategoryRankings(category string, limit int) ([]*AgentReputation, error) {
	path := "/v1/reputation/rankings?category=" + category
	if limit > 0 {
		path += fmt.Sprintf("&limit=%d", limit)
	}
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[[]*AgentReputation](resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Discover queries the capability-aware discovery engine. Returns agents ranked
// by a composite score of relevance, reputation, completion rate, and price.
// Pass empty strings/zeros for unset filters. limit=0 returns all results.
func (c *Client) Discover(query string, category string, maxBudget uint64, minReputation float64, limit int) ([]*Match, error) {
	path := "/v1/discover"
	sep := "?"
	if query != "" {
		path += sep + "q=" + query
		sep = "&"
	}
	if category != "" {
		path += sep + "category=" + category
		sep = "&"
	}
	if maxBudget > 0 {
		path += sep + fmt.Sprintf("max_budget=%d", maxBudget)
		sep = "&"
	}
	if minReputation > 0 {
		path += sep + fmt.Sprintf("min_reputation=%g", minReputation)
		sep = "&"
	}
	if limit > 0 {
		path += sep + fmt.Sprintf("limit=%d", limit)
	}
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return checkAndDecode[[]*Match](resp, http.StatusOK)
}

// CreateSubtask creates a child task under the given parent task. Only the
// current claimer of the parent task may call this. The subtask budget is
// deducted from the parent's remaining budget. Returns the created subtask.
func (c *Client) CreateSubtask(parentTaskID, title, description, category string, budget uint64) (*Task, error) {
	resp, err := c.do(http.MethodPost, "/v1/tasks/"+parentTaskID+"/subtask", map[string]any{
		"title":       title,
		"description": description,
		"category":    category,
		"budget":      budget,
	})
	if err != nil {
		return nil, err
	}
	result, err := checkAndDecode[Task](resp, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSubtasks returns all child tasks of the given parent task.
func (c *Client) GetSubtasks(taskID string) ([]Task, error) {
	resp, err := c.do(http.MethodGet, "/v1/tasks/subtasks/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	return checkAndDecode[[]Task](resp, http.StatusOK)
}

// DeactivateService marks the node's own service listing as inactive.
// It first calls Status() to resolve the node's agentID, then issues DELETE.
func (c *Client) DeactivateService() error {
	status, err := c.Status()
	if err != nil {
		return fmt.Errorf("sdk: resolve agent id: %w", err)
	}
	resp, err := c.do(http.MethodDelete, "/v1/registry/"+status.AgentID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		msg := apiErr.Error
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return fmt.Errorf("sdk: api error %d: %s", resp.StatusCode, msg)
	}
	return nil
}
