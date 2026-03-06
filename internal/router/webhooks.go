package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// WebhookConfig holds the optional outbound webhook settings for an agent.
// When URL is non-empty the Router POSTs a signed task-assignment notification
// to the agent's endpoint whenever it is routed a new task.
type WebhookConfig struct {
	URL    string `json:"webhook_url"`
	Secret string `json:"webhook_secret,omitempty"` // HMAC-SHA256 signing secret
}

// webhookPayload is the JSON body sent to the agent's webhook endpoint.
type webhookPayload struct {
	Event    string     `json:"event"`    // "task_assigned"
	TaskID   string     `json:"task_id"`
	AgentID  string     `json:"agent_id"`
	Task     *tasks.Task `json:"task"`
	IssuedAt int64      `json:"issued_at"` // unix nano
}

// NotifyAgent sends a signed webhook notification to agentID's registered
// endpoint informing it that task has been routed to it.
//
// The method is a no-op when the agent has no webhook configured.
// A 5-second HTTP timeout is applied to each request.
// The notification is best-effort: any error is returned but the router
// continues regardless.
func (r *Router) NotifyAgent(agentID crypto.AgentID, task *tasks.Task) error {
	r.mu.RLock()
	cap, ok := r.capabilities[agentID]
	if !ok || cap.Webhook == nil || cap.Webhook.URL == "" {
		r.mu.RUnlock()
		return nil
	}
	wh := *cap.Webhook // copy under lock
	r.mu.RUnlock()

	payload := webhookPayload{
		Event:    "task_assigned",
		TaskID:   task.ID,
		AgentID:  string(agentID),
		Task:     task,
		IssuedAt: time.Now().UnixNano(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("router: webhook marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("router: webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AetherNet-Event", "task_assigned")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		req.Header.Set("X-AetherNet-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("router: webhook delivery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("router: webhook returned %d for agent %s", resp.StatusCode, agentID)
	}
	return nil
}
