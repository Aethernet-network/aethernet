// Phase 3.5 criterion 11a harness: drive a TaskVerificationConsensus
// settlement on the F5 5B testnet, designed to fire a multi-record
// PayoutRecord output (worker + validators + treasury).
//
// Usage:
//
//	cd /tmp/aet-11a-harness && go mod init harness 2>/dev/null
//	go run . [--target URL] [--budget BUDGET]
//
// Sequence:
//
//  1. Generate two TX-V1 signing keypairs (poster + worker).
//  2. POST /v1/agents (signed) for each → onboarding allocation.
//  3. Wait until poster's balance >= budget on the API node.
//  4. POST /v1/tasks (signed by poster) with --budget, escrowed from poster's balance.
//  5. POST /v1/tasks/{id}/claim (signed by worker).
//  6. POST /v1/tasks/{id}/submit (signed by worker) with synthetic evidence.
//  7. Poll /v1/tasks/{id} until status terminal (Completed / Rejected / DisputedResolved).
//  8. Print task ID + final status + worker balance + poster balance.
//
// Auto-validators on each node vote on the submission; once consensus
// reaches accept/reject, the verification_consensus_settler fires
// DeriveSettlement → ApplySettlementRecords. The settlement produces:
//
//   - 1 worker payout
//   - N validator payouts (3-5 depending on which validators voted)
//   - 1 treasury record
//   - 0+ gen-ledger ancestor records (depending on DAG topology)
//
// Total = ≥ 5 records typically. Sufficient to exercise
// AETHERNET_CRASH_AFTER_NTH_RECORD=N for N in {0..len(records)-1}.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

type signer struct {
	priv     ed25519.PrivateKey
	actor    string // hex pubkey
	chainID  string
	transport http.RoundTripper
}

func newSigner() *signer {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return &signer{priv: priv, actor: hex.EncodeToString(pub), chainID: "aethernet-testnet-1", transport: http.DefaultTransport}
}

func (s *signer) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	canon, err := auth.CanonicalizeJSON(body)
	if err != nil {
		canon = body
	}
	bh := sha256.Sum256(canon)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	now := time.Now().Unix()
	tx := &auth.Transaction{
		Version:    auth.TxVersion,
		ChainID:    s.chainID,
		Actor:      s.actor,
		Method:     req.Method,
		Path:       req.URL.Path,
		BodySHA256: hex.EncodeToString(bh[:]),
		CreatedAt:  now,
		ExpiresAt:  now + 120,
		Nonce:      hex.EncodeToString(nonce),
	}
	sig, err := tx.Sign(s.priv)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-AetherNet-Version", auth.TxVersion)
	req.Header.Set("X-AetherNet-Chain-ID", s.chainID)
	req.Header.Set("X-AetherNet-Actor", s.actor)
	req.Header.Set("X-AetherNet-Created", strconv.FormatInt(now, 10))
	req.Header.Set("X-AetherNet-Expires", strconv.FormatInt(now+120, 10))
	req.Header.Set("X-AetherNet-Nonce", tx.Nonce)
	req.Header.Set("X-AetherNet-Signature", sig)
	return s.transport.RoundTrip(req)
}

func (s *signer) doJSON(method, url string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: s}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status=%d body=%s", method, url, resp.StatusCode, string(rb))
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("decode: %w body=%s", err, string(rb))
		}
	}
	return nil
}

func main() {
	target := flag.String("target", "https://testnet.aethernet.network", "API URL")
	budget := flag.Uint64("budget", 100_000_000, "task budget in µAET (default 100K µAET = 0.1 AET)")
	flag.Parse()

	fmt.Printf("Phase 3.5 criterion 11a harness\n")
	fmt.Printf("Target: %s  Budget: %d µAET\n\n", *target, *budget)

	poster := newSigner()
	worker := newSigner()
	fmt.Printf("Poster: %s\n", poster.actor[:16]+"...")
	fmt.Printf("Worker: %s\n", worker.actor[:16]+"...")

	// 1. Register both agents
	fmt.Println("\n[1] Register poster")
	var regResp struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
	}
	if err := poster.doJSON("POST", *target+"/v1/agents", map[string]any{}, &regResp); err != nil {
		fail("register poster: %v", err)
	}
	posterID := regResp.AgentID
	fmt.Printf("  poster_id=%s alloc=%d\n", posterID, regResp.OnboardingAllocation)

	fmt.Println("\n[2] Register worker")
	if err := worker.doJSON("POST", *target+"/v1/agents", map[string]any{}, &regResp); err != nil {
		fail("register worker: %v", err)
	}
	workerID := regResp.AgentID
	fmt.Printf("  worker_id=%s alloc=%d\n", workerID, regResp.OnboardingAllocation)

	// Wait for onboarding allocation to settle.
	fmt.Println("\n[3] Wait for poster balance >= budget")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var bal struct {
			Balance uint64 `json:"balance"`
		}
		if err := poster.doJSON("GET", *target+"/v1/agents/"+posterID+"/balance", nil, &bal); err == nil {
			fmt.Printf("  poster balance: %d µAET\n", bal.Balance)
			if bal.Balance >= *budget {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	// 4. Post task
	fmt.Println("\n[4] Post task")
	var task struct {
		ID string `json:"id"`
	}
	if err := poster.doJSON("POST", *target+"/v1/tasks", map[string]any{
		"title":       "11a-harness-task-" + poster.actor[:8],
		"description": "F5 5B Phase 3.5 criterion 11a multi-record settlement test",
		"category":    "research",
		"budget":      *budget,
	}, &task); err != nil {
		fail("post task: %v", err)
	}
	taskID := task.ID
	fmt.Printf("  task_id=%s\n", taskID)

	// 5. Claim (with retry — task may be in "pending router assignment"
	// for up to ~60s after post).
	fmt.Println("\n[5] Worker claims task (retry until eligible, 120s deadline)")
	deadline = time.Now().Add(120 * time.Second)
	var claimErr error
	for time.Now().Before(deadline) {
		claimErr = worker.doJSON("POST", *target+"/v1/tasks/"+taskID+"/claim", map[string]any{}, nil)
		if claimErr == nil {
			break
		}
		if !strings.Contains(claimErr.Error(), "pending router assignment") &&
			!strings.Contains(claimErr.Error(), "409") {
			fail("claim (non-retryable): %v", claimErr)
		}
		fmt.Printf("  retry: %v\n", claimErr)
		time.Sleep(15 * time.Second)
	}
	if claimErr != nil {
		fail("claim: %v", claimErr)
	}
	fmt.Println("  claimed")

	// 6. Submit
	fmt.Println("\n[6] Worker submits result")
	evidence := "F5 5B criterion 11a synthetic evidence for multi-record settlement test. " +
		"This evidence is intentionally long enough to pass the evidence-threshold validation " +
		"and produce a representative TaskVerificationConsensus settlement with worker payout " +
		"plus validator-pool plus treasury records."
	evidenceHash := sha256.Sum256([]byte(evidence))
	// Use canonical API field names per internal/api/server.go
	// submitTaskRequest: result_hash + result_content. Auto-validator's
	// processSubmittedTaskMultiVoter gates on `content != ""` sourced from
	// task.SubmittedEvidence.ResolveContent() OR task.ResultContent — so
	// without result_content, the gate trips forever and the round
	// abstains.
	if err := worker.doJSON("POST", *target+"/v1/tasks/"+taskID+"/submit", map[string]any{
		"result_hash":    "sha256:" + hex.EncodeToString(evidenceHash[:]),
		"result_content": evidence,
		"result_note":    "harness submission for 11a verification",
	}, nil); err != nil {
		fail("submit: %v", err)
	}
	fmt.Println("  submitted")

	// 7. Poll for terminal — auto-validator voting + consensus + settlement
	// can take 2-5 minutes on testnet under default cadence.
	pollDeadline := flag.Duration("poll", 5*time.Minute, "max polling deadline for terminal task status")
	_ = pollDeadline // silence unused (already defined)
	fmt.Println("\n[7] Poll task status (5m deadline)")
	deadline = time.Now().Add(5 * time.Minute)
	var lastStatus string
	for time.Now().Before(deadline) {
		var t struct {
			Status string `json:"status"`
		}
		if err := poster.doJSON("GET", *target+"/v1/tasks/"+taskID, nil, &t); err == nil {
			if t.Status != lastStatus {
				fmt.Printf("  [%s] status: %s\n", time.Now().Format("15:04:05"), t.Status)
				lastStatus = t.Status
			}
			if isTerminal(t.Status) {
				fmt.Printf("\n[8] Terminal: %s\n", t.Status)
				fmt.Printf("\nTASK_ID=%s\nPOSTER_ID=%s\nWORKER_ID=%s\nFINAL_STATUS=%s\n",
					taskID, posterID, workerID, t.Status)
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
	fail("timed out waiting for terminal status; last=%s\nTASK_ID=%s POSTER_ID=%s WORKER_ID=%s",
		lastStatus, taskID, posterID, workerID)
}

func isTerminal(s string) bool {
	return strings.EqualFold(s, "Completed") || strings.EqualFold(s, "Rejected") ||
		strings.EqualFold(s, "DisputedResolved") || strings.EqualFold(s, "Cancelled")
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "FAIL: "+fmt.Sprintf(format, args...))
	os.Exit(1)
}
