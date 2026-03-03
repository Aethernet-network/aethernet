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
