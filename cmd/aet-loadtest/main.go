// Command aet-loadtest is a production load-testing tool for AetherNet nodes.
//
// It connects to a running node via the Go SDK (no internal package imports)
// and runs four sequential benchmark phases, reporting structured JSON at the end.
//
// Usage:
//
//	go run cmd/aet-loadtest/main.go [flags]
//
// Flags:
//
//	--target    Node API URL (default: https://testnet.aethernet.network)
//	--agents    Number of concurrent registration calls  (default: 50)
//	--transfers Number of concurrent Transfer events     (default: 200)
//	--tasks     Number of concurrent Generation events   (default: 100)
//	--duration  Max stress-test duration                 (default: 120s)
//
// Phase 1: Agent Registration — concurrent Register() calls; measures HTTP
//
//	throughput for the registration endpoint. Responses that hit the
//	server-side rate limit (HTTP 429) are counted separately as
//	"rate_limited" and are not included in the error count.
//
// Phase 2: Transfer Throughput — calls ensureFunded() to obtain the node's
//
//	onboarding allocation, then submits concurrent Transfer events.
//	The transfer count is capped to what the available balance can cover
//	(amount + stake per transfer). Supply invariant is checked after.
//
// Phase 3: Generation / Task Lifecycle — calls ensureFunded() again, then
//
//	submits concurrent Generate() events (proof-of-work proxy) and
//	concurrent PostTask() calls. Both counts are capped to available balance.
//	Note: full post→claim→submit lifecycle requires two distinct agent
//	identities. Since the SDK operates as a single node identity,
//	self-claim is blocked, so Generate() is the correct proxy for the
//	"verified output that creates a ledger entry" lifecycle.
//
// Phase 4: Stress — calls ensureFunded() for a pre-funded baseline, then
//
//	runs Transfer and Generate concurrently for --duration; measures peak
//	TPS, total ops, error rate, and memory growth.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/Aethernet-network/aethernet/pkg/sdk"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type config struct {
	Target    string
	Agents    int
	Transfers int
	Tasks     int
	Duration  time.Duration
}

// ---------------------------------------------------------------------------
// Latency helpers
// ---------------------------------------------------------------------------

// safeLatencies is a thread-safe accumulator for millisecond latencies.
type safeLatencies struct {
	mu  sync.Mutex
	val []int64
}

func (s *safeLatencies) record(d time.Duration) {
	s.mu.Lock()
	s.val = append(s.val, d.Milliseconds())
	s.mu.Unlock()
}

func (s *safeLatencies) sorted() []int64 {
	s.mu.Lock()
	cp := make([]int64, len(s.val))
	copy(cp, s.val)
	s.mu.Unlock()
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp
}

func pct(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// ---------------------------------------------------------------------------
// Result types (match the JSON spec exactly)
// ---------------------------------------------------------------------------

type agentsResult struct {
	Registered  int     `json:"registered"`
	RateLimited int64   `json:"rate_limited"`
	RatePerSec  float64 `json:"rate_per_sec"`
	P50MS       int64   `json:"p50_ms"`
	P95MS       int64   `json:"p95_ms"`
	P99MS       int64   `json:"p99_ms"`
	Errors      int64   `json:"errors"`
}

type transfersResult struct {
	Submitted       int     `json:"submitted"`
	Settled         int64   `json:"settled"`
	Failed          int64   `json:"failed"`
	TPSSubmitted    float64 `json:"tps_submitted"`
	TPSSettled      float64 `json:"tps_settled"`
	P50SettlementMS int64   `json:"p50_settlement_ms"`
	P95SettlementMS int64   `json:"p95_settlement_ms"`
	P99SettlementMS int64   `json:"p99_settlement_ms"`
	SupplyInvariant bool    `json:"supply_invariant"`
}

type tasksResult struct {
	Posted          int     `json:"posted"`
	Settled         int64   `json:"settled"`
	Failed          int64   `json:"failed"`
	LifecyclePerSec float64 `json:"lifecycle_per_sec"`
	P50LifecycleMS  int64   `json:"p50_lifecycle_ms"`
	P95LifecycleMS  int64   `json:"p95_lifecycle_ms"`
	P99LifecycleMS  int64   `json:"p99_lifecycle_ms"`
	FeesCollected   uint64  `json:"fees_collected"`
	GenerationEntries int64 `json:"generation_entries"`
}

type stressResult struct {
	DurationSec     float64 `json:"duration_sec"`
	PeakTPS         float64 `json:"peak_tps"`
	TotalOperations int64   `json:"total_operations"`
	ErrorRate       float64 `json:"error_rate"`
	MemoryStartMB   uint64  `json:"memory_start_mb"`
	MemoryEndMB     uint64  `json:"memory_end_mb"`
}

type report struct {
	Target    string          `json:"target"`
	Timestamp string          `json:"timestamp"`
	Agents    agentsResult    `json:"agents"`
	Transfers transfersResult `json:"transfers"`
	Tasks     tasksResult     `json:"tasks"`
	Stress    stressResult    `json:"stress"`
}

// ---------------------------------------------------------------------------
// Settlement poller
// ---------------------------------------------------------------------------

// settleTimeout is the maximum time to wait for a single event to settle.
// The auto-validator runs every 50ms on testnet; 60s gives ample coverage even
// under load.
const settleTimeout = 60 * time.Second

// shortID returns the first 12 characters of an event ID for compact logging.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

// debugPrintRawEvent GETs /v1/events/{id} and pretty-prints the raw JSON body.
// Used to verify the exact field names and values returned by the node.
func debugPrintRawEvent(c *sdk.Client, eventID string) {
	url := c.BaseURL + "/v1/events/" + eventID
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		fmt.Printf("  [debug] raw GET %s: %v\n", url, err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("  [debug] raw GET read error: %v\n", err)
		return
	}
	var pretty bytes.Buffer
	if jerr := json.Indent(&pretty, body, "    ", "  "); jerr != nil {
		fmt.Printf("  [debug] raw GET /v1/events/%s → %s\n", shortID(eventID), string(body))
		return
	}
	fmt.Printf("  [debug] raw GET /v1/events/%s →\n    %s\n", shortID(eventID), pretty.String())
}

// pollSettled polls GetEvent until the event reaches Settled or Adjusted state
// or settleTimeout elapses. Returns (finalState, true) on settlement or
// (lastObservedState, false) on timeout.
//
// Settlement state constants from internal/event/event.go are capitalized:
//
//	"Optimistic" → initial state
//	"Settled"    → OCS confirmed (poller returns true)
//	"Adjusted"   → challenged and reversed (poller returns true)
//
// If verbose is true, each poll attempt is printed to stdout so the caller
// can trace exactly what the node returns while the event is pending.
func pollSettled(c *sdk.Client, eventID string, start time.Time, lat *safeLatencies, verbose bool) (string, bool) {
	deadline := time.Now().Add(settleTimeout)
	attempt := 0
	lastState := "unknown"
	for time.Now().Before(deadline) {
		attempt++
		ev, err := c.GetEvent(eventID)
		if err != nil {
			if verbose {
				fmt.Printf("  [poll] attempt %2d: %s → error: %v\n", attempt, shortID(eventID), err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		lastState = ev.SettlementState
		if verbose {
			fmt.Printf("  [poll] attempt %2d: %s → %s\n", attempt, shortID(eventID), lastState)
		}
		// NOTE: SettlementState values are capitalized ("Settled", "Adjusted"),
		// not lowercase. The original bug was checking "settled"/"adjusted" here.
		switch lastState {
		case "Settled", "Adjusted":
			lat.record(time.Since(start))
			return lastState, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return lastState, false
}

// ---------------------------------------------------------------------------
// Agent funding helpers
// ---------------------------------------------------------------------------

// agentFunds holds a snapshot of the node agent's funding state after calling
// Register() to claim any pending onboarding allocation.
type agentFunds struct {
	AgentID string
	Balance uint64 // spendable µAET
	Staked  uint64 // currently staked µAET
}

// ensureFunded registers the node's own agent (granting the onboarding
// allocation on first call) and reads back its balance and stake. If Register
// returns a 429 (rate-limited), the agent ID is resolved via Status instead
// and we proceed with the existing balance. Returns nil only if the agent ID
// cannot be determined at all.
func ensureFunded(c *sdk.Client) *agentFunds {
	agentID, err := c.Register(nil)
	if err != nil {
		if !strings.Contains(err.Error(), "429") {
			fmt.Printf("  ensureFunded: Register: %v\n", err)
		}
		// Fall back to Status to resolve the node's agent ID.
		st, serr := c.Status()
		if serr != nil {
			fmt.Printf("  ensureFunded: cannot resolve agent ID via Status: %v\n", serr)
			return nil
		}
		agentID = st.AgentID
	}

	af := &agentFunds{AgentID: agentID}
	if balResp, berr := c.Balance(agentID); berr == nil {
		af.Balance = balResp.Balance
	} else {
		fmt.Printf("  ensureFunded: cannot read balance for %s: %v\n", agentID, berr)
	}
	if stakeResp, sterr := c.StakeInfo(agentID); sterr == nil {
		af.Staked = stakeResp.StakedAmount
	}
	return af
}

// ---------------------------------------------------------------------------
// Memory helpers
// ---------------------------------------------------------------------------

func memMB() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Alloc / (1024 * 1024)
}

// ---------------------------------------------------------------------------
// Phase 1 — Agent Registration Benchmark
// ---------------------------------------------------------------------------

func runRegistration(c *sdk.Client, n int) agentsResult {
	fmt.Printf("  Phase 1: registering %d agents concurrently...\n", n)

	var (
		wg          sync.WaitGroup
		errors      atomic.Int64
		rateLimited atomic.Int64
		lats        safeLatencies
	)

	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			_, err := c.Register(nil)
			if err != nil {
				// HTTP 429 means the server-side rate limiter fired.
				// Report these separately — they are expected behaviour,
				// not protocol errors.
				if strings.Contains(err.Error(), "429") {
					rateLimited.Add(1)
				} else {
					errors.Add(1)
				}
				return
			}
			lats.record(time.Since(t0))
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	rl := rateLimited.Load()
	errs := errors.Load()
	succeeded := n - int(errs) - int(rl)
	sorted := lats.sorted()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(succeeded) / elapsed
	}

	res := agentsResult{
		Registered:  succeeded,
		RateLimited: rl,
		RatePerSec:  round2(rate),
		P50MS:       pct(sorted, 0.50),
		P95MS:       pct(sorted, 0.95),
		P99MS:       pct(sorted, 0.99),
		Errors:      errs,
	}
	fmt.Printf("  Phase 1 done: %d registered, %d rate_limited, %.1f/s, p50=%dms p95=%dms p99=%dms errors=%d\n",
		res.Registered, res.RateLimited, res.RatePerSec, res.P50MS, res.P95MS, res.P99MS, res.Errors)
	return res
}

// ---------------------------------------------------------------------------
// Phase 2 — Transfer Throughput
// ---------------------------------------------------------------------------

const (
	transferAmount    = uint64(100)  // 100 µAET per transfer
	transferStake     = uint64(1000) // OCS collateral per Transfer event
	settlePollWorkers = 20           // max concurrent settlement pollers
)

func runTransfers(c *sdk.Client, m int) transfersResult {
	// Ensure the node's agent is funded before attempting transfers.
	// Register() grants the onboarding allocation on first call and auto-stakes it.
	// Each transfer costs transferAmount (moved) + transferStake (OCS collateral).
	fmt.Printf("  Phase 2: funding agent...\n")
	funds := ensureFunded(c)
	if funds != nil {
		costPerTransfer := transferAmount + transferStake
		fmt.Printf("  Agent %s: balance=%d µAET, staked=%d µAET\n",
			funds.AgentID, funds.Balance, funds.Staked)
		if needed := uint64(m) * costPerTransfer; funds.Balance < needed {
			safeCount := int(funds.Balance / costPerTransfer)
			fmt.Printf("  Warning: balance %d µAET < needed %d µAET; capping transfers %d→%d\n",
				funds.Balance, needed, m, safeCount)
			m = safeCount
		}
	}
	if m == 0 {
		fmt.Printf("  Phase 2 skipped: insufficient balance (register and fund the node agent first)\n")
		return transfersResult{}
	}

	fmt.Printf("  Phase 2: submitting %d transfers concurrently...\n", m)

	// Snapshot economics before transfers.
	econBefore, err := c.Economics()
	if err != nil {
		fmt.Printf("  Warning: could not read economics before transfers: %v\n", err)
	}

	type submitResult struct {
		eventID string
		start   time.Time
		err     error
	}

	results := make(chan submitResult, m)
	var wg sync.WaitGroup

	submitStart := time.Now()
	for i := 0; i < m; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Fabricate a receiver agent ID; it doesn't need to be registered
			// for a Transfer event to be submitted and tracked in OCS.
			receiver := fmt.Sprintf("loadtest-recv-%d-%d", idx, rand.Int63n(1_000_000))
			t0 := time.Now()
			eventID, err := c.Transfer(sdk.TransferRequest{
				ToAgent:     receiver,
				Amount:      transferAmount,
				Currency:    "AET",
				StakeAmount: transferStake,
			})
			results <- submitResult{eventID: eventID, start: t0, err: err}
		}(i)
	}

	// Close channel once all submissions finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect submitted events.
	type pending struct {
		eventID string
		start   time.Time
	}
	var submitted []pending
	var submitErrors int64
	for r := range results {
		if r.err != nil {
			submitErrors++
		} else {
			submitted = append(submitted, pending{r.eventID, r.start})
		}
	}
	submitElapsed := time.Since(submitStart).Seconds()

	fmt.Printf("  Phase 2: %d submitted (%.1f/s), polling settlement...\n",
		len(submitted), float64(len(submitted))/submitElapsed)

	// Debug: print the raw HTTP response for the first submitted event so we
	// can verify the exact settlement_state field name and value the node returns.
	if len(submitted) > 0 {
		fmt.Printf("  [debug] fetching raw event response for first transfer:\n")
		debugPrintRawEvent(c, submitted[0].eventID)
	}

	// Poll for settlement with bounded concurrency.
	// The first event is polled verbosely to trace the full state progression.
	sem := make(chan struct{}, settlePollWorkers)
	var (
		settled   atomic.Int64
		unsettled atomic.Int64
		settleLats safeLatencies
		settleWG   sync.WaitGroup
		// stateMu guards stateCounts which tallies final observed states.
		stateMu    sync.Mutex
		stateCounts = map[string]int{}
		firstDone  atomic.Bool // true once the first verbose poller has started
	)
	settleStart := time.Now()
	for _, p := range submitted {
		settleWG.Add(1)
		sem <- struct{}{}
		verbose := firstDone.CompareAndSwap(false, true)
		go func(p pending, verbose bool) {
			defer settleWG.Done()
			defer func() { <-sem }()
			finalState, ok := pollSettled(c, p.eventID, p.start, &settleLats, verbose)
			stateMu.Lock()
			stateCounts[finalState]++
			stateMu.Unlock()
			if ok {
				settled.Add(1)
			} else {
				unsettled.Add(1)
			}
		}(p, verbose)
	}
	settleWG.Wait()
	settleElapsed := time.Since(settleStart).Seconds()

	// Print state breakdown so the user can see how many events are in each state.
	stateMu.Lock()
	if len(stateCounts) > 0 {
		fmt.Printf("  Phase 2 state breakdown:")
		for state, count := range stateCounts {
			fmt.Printf("  %s=%d", state, count)
		}
		fmt.Println()
	}
	stateMu.Unlock()

	// Supply invariant check.
	supplyOK := false
	if econBefore != nil {
		econAfter, err := c.Economics()
		if err == nil {
			supplyOK = econAfter.TotalSupply == econBefore.TotalSupply
		}
	}

	sortedSettle := settleLats.sorted()
	tpsSubmit := 0.0
	if submitElapsed > 0 {
		tpsSubmit = float64(len(submitted)) / submitElapsed
	}
	tpsSettle := 0.0
	if settleElapsed > 0 {
		tpsSettle = float64(settled.Load()) / settleElapsed
	}

	res := transfersResult{
		Submitted:       len(submitted) + int(submitErrors),
		Settled:         settled.Load(),
		Failed:          unsettled.Load() + submitErrors,
		TPSSubmitted:    round2(tpsSubmit),
		TPSSettled:      round2(tpsSettle),
		P50SettlementMS: pct(sortedSettle, 0.50),
		P95SettlementMS: pct(sortedSettle, 0.95),
		P99SettlementMS: pct(sortedSettle, 0.99),
		SupplyInvariant: supplyOK,
	}
	fmt.Printf("  Phase 2 done: submitted=%d settled=%d failed=%d tps_submit=%.1f tps_settle=%.1f p50=%dms supply_ok=%v\n",
		res.Submitted, res.Settled, res.Failed,
		res.TPSSubmitted, res.TPSSettled, res.P50SettlementMS, res.SupplyInvariant)
	return res
}

// ---------------------------------------------------------------------------
// Phase 3 — Task / Generation Lifecycle Throughput
// ---------------------------------------------------------------------------

const (
	genClaimedValue = uint64(10_000)  // 10,000 µAET claimed value
	genStake        = uint64(1000)    // OCS collateral per Generation event
	taskBudget      = uint64(100_000) // 0.1 AET per task (escrowed from node balance)
)

func runTasks(c *sdk.Client, t int) tasksResult {
	// Ensure the node's agent has funds before submitting generation events.
	// Each generation event locks genStake µAET as OCS collateral (returned on success).
	// Task posting escrows taskBudget µAET per task.
	fmt.Printf("  Phase 3: funding agent...\n")
	funds := ensureFunded(c)
	taskCount := t // number of tasks to post; may be reduced by balance
	if funds != nil {
		fmt.Printf("  Agent %s: balance=%d µAET, staked=%d µAET\n",
			funds.AgentID, funds.Balance, funds.Staked)

		// Cap generation events to available balance.
		if needed := uint64(t) * genStake; funds.Balance < needed {
			t = int(funds.Balance / genStake)
			fmt.Printf("  Warning: balance insufficient for %d generation events; capping to %d\n",
				taskCount, t)
		}

		// Calculate how many tasks can be posted given remaining balance.
		remainingAfterGen := funds.Balance - uint64(t)*genStake
		if remainingAfterGen < taskBudget {
			taskCount = 0
			fmt.Printf("  Warning: balance insufficient for task posting after generation events; skipping task posts\n")
		} else {
			safeTaskCount := int(remainingAfterGen / taskBudget)
			if safeTaskCount < taskCount {
				fmt.Printf("  Warning: balance insufficient for %d task posts; capping to %d\n",
					taskCount, safeTaskCount)
				taskCount = safeTaskCount
			}
		}
	}
	if t == 0 {
		fmt.Printf("  Phase 3 skipped: insufficient balance for generation events\n")
		return tasksResult{}
	}

	fmt.Printf("  Phase 3: submitting %d generation events concurrently...\n", t)

	// Snapshot fees before to calculate delta.
	econBefore, _ := c.Economics()
	var feesBefore uint64
	if econBefore != nil {
		feesBefore = econBefore.TotalCollected
	}

	type genResult struct {
		eventID string
		start   time.Time
		err     error
	}

	results := make(chan genResult, t)
	var wg sync.WaitGroup

	submitStart := time.Now()
	for i := 0; i < t; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			output := []byte(fmt.Sprintf(
				"load test generation output #%d: verified AI computation result with sufficient length to pass evidence threshold. "+
					"This represents real productive work output from an autonomous agent on the AetherNet network.",
				idx,
			))
			ev := sdk.NewEvidence(output, "text", fmt.Sprintf("load test output #%d", idx))
			t0 := time.Now()
			eventID, err := c.Generate(sdk.GenerationRequest{
				ClaimedValue:    genClaimedValue,
				EvidenceHash:    ev.Hash,
				TaskDescription: fmt.Sprintf("load-test task %d: proof of useful computation", idx),
				StakeAmount:     genStake,
			})
			results <- genResult{eventID: eventID, start: t0, err: err}
		}(i)
	}
	go func() { wg.Wait(); close(results) }()

	type pending struct {
		eventID string
		start   time.Time
	}
	var submitted []pending
	var submitErrors int64
	for r := range results {
		if r.err != nil {
			submitErrors++
		} else {
			submitted = append(submitted, pending{r.eventID, r.start})
		}
	}
	submitElapsed := time.Since(submitStart).Seconds()

	fmt.Printf("  Phase 3: %d submitted (%.1f/s), polling settlement...\n",
		len(submitted), float64(len(submitted))/submitElapsed)

	// Debug: print the raw HTTP response for the first generation event.
	if len(submitted) > 0 {
		fmt.Printf("  [debug] fetching raw event response for first generation event:\n")
		debugPrintRawEvent(c, submitted[0].eventID)
	}

	sem := make(chan struct{}, settlePollWorkers)
	var (
		settled       atomic.Int64
		unsettled     atomic.Int64
		lifecycleLats safeLatencies
		settleWG      sync.WaitGroup
		stateMu       sync.Mutex
		stateCounts   = map[string]int{}
		firstDone     atomic.Bool
	)
	lifecycleStart := time.Now()
	for _, p := range submitted {
		settleWG.Add(1)
		sem <- struct{}{}
		verbose := firstDone.CompareAndSwap(false, true)
		go func(p pending, verbose bool) {
			defer settleWG.Done()
			defer func() { <-sem }()
			finalState, ok := pollSettled(c, p.eventID, p.start, &lifecycleLats, verbose)
			stateMu.Lock()
			stateCounts[finalState]++
			stateMu.Unlock()
			if ok {
				settled.Add(1)
			} else {
				unsettled.Add(1)
			}
		}(p, verbose)
	}
	settleWG.Wait()
	// Total lifecycle elapsed covers both submission and settlement polling.
	lifecycleElapsed := time.Since(lifecycleStart) + time.Duration(float64(submitElapsed)*float64(time.Second))

	// Print state breakdown.
	stateMu.Lock()
	if len(stateCounts) > 0 {
		fmt.Printf("  Phase 3 state breakdown:")
		for state, count := range stateCounts {
			fmt.Printf("  %s=%d", state, count)
		}
		fmt.Println()
	}
	stateMu.Unlock()

	// Delta fees.
	var feesCollected uint64
	econAfter, _ := c.Economics()
	if econAfter != nil {
		feesCollected = econAfter.TotalCollected - feesBefore
	}

	sorted := lifecycleLats.sorted()
	lps := 0.0
	if lifecycleElapsed.Seconds() > 0 {
		lps = float64(settled.Load()) / lifecycleElapsed.Seconds()
	}

	// Also benchmark task posting (separate from generation lifecycle).
	posted := runTaskPosting(c, taskCount)

	res := tasksResult{
		Posted:          posted,
		Settled:         settled.Load(),
		Failed:          unsettled.Load() + submitErrors,
		LifecyclePerSec: round2(lps),
		P50LifecycleMS:  pct(sorted, 0.50),
		P95LifecycleMS:  pct(sorted, 0.95),
		P99LifecycleMS:  pct(sorted, 0.99),
		FeesCollected:   feesCollected,
		GenerationEntries: settled.Load(),
	}
	fmt.Printf("  Phase 3 done: gen_settled=%d posted=%d failed=%d lifecycle/s=%.1f p50=%dms fees_delta=%d\n",
		res.Settled, res.Posted, res.Failed,
		res.LifecyclePerSec, res.P50LifecycleMS, res.FeesCollected)
	return res
}

// runTaskPosting benchmarks task POST throughput concurrently with generation.
// Returns the count of successfully posted tasks. Tasks are posted but not
// claimed (self-claim is blocked since poster == node's agent ID).
func runTaskPosting(c *sdk.Client, n int) int {
	if n == 0 {
		return 0
	}
	fmt.Printf("  Phase 3b: posting %d tasks concurrently...\n", n)
	var (
		wg     sync.WaitGroup
		posted atomic.Int64
		errs   atomic.Int64
	)
	categories := []string{"research", "code", "data", "writing", "analysis"}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cat := categories[idx%len(categories)]
			_, err := c.PostTask(
				fmt.Sprintf("Load test task %d", idx),
				fmt.Sprintf("Automated load-test task #%d in category %s. "+
					"This task measures marketplace posting throughput under concurrent load. "+
					"Verifiable output expected within 60 seconds.", idx, cat),
				cat,
				taskBudget,
			)
			if err != nil {
				errs.Add(1)
			} else {
				posted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("  Phase 3b done: posted=%d errors=%d\n", posted.Load(), errs.Load())
	return int(posted.Load())
}

// ---------------------------------------------------------------------------
// Phase 4 — Stress Test
// ---------------------------------------------------------------------------

func runStress(c *sdk.Client, duration time.Duration) stressResult {
	fmt.Printf("  Phase 4: stress test for %v...\n", duration)

	// Ensure the agent is funded before the stress run and log the baseline.
	funds := ensureFunded(c)
	if funds != nil {
		fmt.Printf("  Agent %s: balance=%d µAET, staked=%d µAET (stress baseline)\n",
			funds.AgentID, funds.Balance, funds.Staked)
	}

	memStart := memMB()

	var (
		totalOps  atomic.Int64
		totalErrs atomic.Int64
		peakTPS   float64
		peakMu    sync.Mutex
	)

	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup

	// TPS window tracker: count ops per 1-second window.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var windowOps atomic.Int64
	go func() {
		for range tick.C {
			ops := windowOps.Swap(0)
			tps := float64(ops)
			peakMu.Lock()
			if tps > peakTPS {
				peakTPS = tps
			}
			peakMu.Unlock()
			if time.Now().After(deadline) {
				return
			}
		}
	}()

	// Continuous transfer worker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for time.Now().Before(deadline) {
			receiver := fmt.Sprintf("stress-recv-%d-%d", i, rand.Int63n(1_000_000))
			_, err := c.Transfer(sdk.TransferRequest{
				ToAgent:     receiver,
				Amount:      transferAmount,
				Currency:    "AET",
				StakeAmount: transferStake,
			})
			if err != nil {
				totalErrs.Add(1)
			} else {
				totalOps.Add(1)
				windowOps.Add(1)
			}
			i++
		}
	}()

	// Continuous generation worker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for time.Now().Before(deadline) {
			output := []byte(fmt.Sprintf("stress test generation output #%d: verified AI computation", i))
			ev := sdk.NewEvidence(output, "text", fmt.Sprintf("stress output #%d", i))
			_, err := c.Generate(sdk.GenerationRequest{
				ClaimedValue:    genClaimedValue,
				EvidenceHash:    ev.Hash,
				TaskDescription: fmt.Sprintf("stress-test task %d", i),
				StakeAmount:     genStake,
			})
			if err != nil {
				totalErrs.Add(1)
			} else {
				totalOps.Add(1)
				windowOps.Add(1)
			}
			i++
		}
	}()

	wg.Wait()
	memEnd := memMB()

	ops := totalOps.Load()
	errs := totalErrs.Load()
	total := ops + errs
	errRate := 0.0
	if total > 0 {
		errRate = float64(errs) / float64(total)
	}

	peakMu.Lock()
	peak := peakTPS
	peakMu.Unlock()

	res := stressResult{
		DurationSec:     duration.Seconds(),
		PeakTPS:         round2(peak),
		TotalOperations: ops,
		ErrorRate:       round4(errRate),
		MemoryStartMB:   memStart,
		MemoryEndMB:     memEnd,
	}
	fmt.Printf("  Phase 4 done: total_ops=%d peak_tps=%.1f error_rate=%.3f mem=%dMB→%dMB\n",
		res.TotalOperations, res.PeakTPS, res.ErrorRate, res.MemoryStartMB, res.MemoryEndMB)
	return res
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func round2(f float64) float64 { return float64(int(f*100)) / 100 }
func round4(f float64) float64 { return float64(int(f*10000)) / 10000 }

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := config{}
	flag.StringVar(&cfg.Target, "target", "https://testnet.aethernet.network", "AetherNet node API URL")
	flag.IntVar(&cfg.Agents, "agents", 50, "number of concurrent registration calls (Phase 1)")
	flag.IntVar(&cfg.Transfers, "transfers", 200, "number of concurrent Transfer events (Phase 2)")
	flag.IntVar(&cfg.Tasks, "tasks", 100, "number of concurrent Generation/task events (Phase 3)")
	flag.DurationVar(&cfg.Duration, "duration", 120*time.Second, "max stress-test duration (Phase 4)")
	flag.Parse()

	// Shared HTTP client with a generous timeout for load testing.
	httpClient := &http.Client{Timeout: 60 * time.Second}
	c := sdk.New(cfg.Target, httpClient)

	// Preflight: verify the node is reachable.
	fmt.Printf("AetherNet Load Test\n")
	fmt.Printf("Target  : %s\n", cfg.Target)
	fmt.Printf("Agents  : %d  Transfers: %d  Tasks: %d  Duration: %v\n\n",
		cfg.Agents, cfg.Transfers, cfg.Tasks, cfg.Duration)

	status, err := c.Status()
	if err != nil {
		log.Fatalf("preflight: cannot reach %s: %v\nRun with a live node or set --target.", cfg.Target, err)
	}
	fmt.Printf("Node    : %s  version=%s  peers=%d  dag=%d  ocs_pending=%d\n\n",
		status.AgentID, status.Version, status.Peers, status.DAGSize, status.OCSPending)

	r := report{
		Target:    cfg.Target,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// ── Phase 1 ──────────────────────────────────────────────────────────────
	fmt.Println("── Phase 1: Agent Registration ──────────────────────────────────────────")
	r.Agents = runRegistration(c, cfg.Agents)
	fmt.Println()

	// ── Phase 2 ──────────────────────────────────────────────────────────────
	fmt.Println("── Phase 2: Transfer Throughput ─────────────────────────────────────────")
	r.Transfers = runTransfers(c, cfg.Transfers)
	fmt.Println()

	// ── Phase 3 ──────────────────────────────────────────────────────────────
	fmt.Println("── Phase 3: Generation / Task Lifecycle ─────────────────────────────────")
	r.Tasks = runTasks(c, cfg.Tasks)
	fmt.Println()

	// ── Phase 4 ──────────────────────────────────────────────────────────────
	fmt.Println("── Phase 4: Stress Test ─────────────────────────────────────────────────")
	r.Stress = runStress(c, cfg.Duration)
	fmt.Println()

	// ── Final report ─────────────────────────────────────────────────────────
	fmt.Println("── Results ──────────────────────────────────────────────────────────────")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		log.Fatalf("encode report: %v", err)
	}
}
