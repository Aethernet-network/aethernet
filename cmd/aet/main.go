// Command aet is a lightweight CLI wallet for AetherNet nodes.
//
// It communicates with a running node's REST API and supports 13 subcommands
// for managing AET tokens and interacting with the network.
//
// Usage:
//
//	aet <command> [options]
//
// Commands:
//
//	status      Show node status and economics overview
//	balance     Check an agent's spendable balance
//	transfer    Send AET to another agent
//	stake       Stake AET tokens
//	unstake     Unstake AET tokens
//	info        Show agent profile and trust info
//	register    Register a new agent on the node
//	pending     List pending OCS verifications
//	verify      Submit a verification verdict
//	economics   Show detailed network economics
//	agents      List registered agents
//	search      Search the service registry
//	history     Show recent DAG events
//
// Global flags (honoured by every subcommand):
//
//	--node URL    Node API URL          (env: AETHERNET_NODE, default: http://localhost:8338)
//	--agent ID    Agent ID              (env: AETHERNET_AGENT)
//	--json        Output raw JSON
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aethernet/core/pkg/sdk"
)

const cliVersion = "0.1.0-testnet"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "status":
		runStatus(os.Args[2:])
	case "balance":
		runBalance(os.Args[2:])
	case "transfer":
		runTransfer(os.Args[2:])
	case "stake":
		runStake(os.Args[2:])
	case "unstake":
		runUnstake(os.Args[2:])
	case "info":
		runInfo(os.Args[2:])
	case "register":
		runRegister(os.Args[2:])
	case "pending":
		runPending(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "economics":
		runEconomics(os.Args[2:])
	case "agents":
		runAgents(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "history":
		runHistory(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Printf("aet %s\n", cliVersion)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "aet: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`aet %s — AetherNet wallet CLI

Usage: aet <command> [options]

Commands:
  status      Show node status and network economics
  balance     Check an agent's spendable balance
  transfer    Send AET to another agent
  stake       Stake AET tokens
  unstake     Unstake AET tokens
  info        Show agent profile and trust info
  register    Register a new agent on the node
  pending     List pending OCS verifications
  verify      Submit a verification verdict
  economics   Show detailed network economics
  agents      List registered agents
  search      Search the service registry
  history     Show recent DAG events

Global flags (accepted by every subcommand):
  --node URL    Node API URL (default: http://localhost:8338)
  --agent ID    Agent ID for commands that require one
  --json        Output raw JSON instead of formatted text

Environment variables:
  AETHERNET_NODE    Overrides --node default
  AETHERNET_AGENT   Overrides --agent default

Examples:
  aet status
  aet balance --agent <agent-id>
  aet transfer --to <recipient> --amount 5000 --memo "payment"
  aet stake --agent <agent-id> --amount 50000
  aet verify --event <event-id> --verdict approve
`, cliVersion)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func defaultNode() string {
	if v := os.Getenv("AETHERNET_NODE"); v != "" {
		return v
	}
	return "http://localhost:8338"
}

func defaultAgent() string {
	return os.Getenv("AETHERNET_AGENT")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "aet: "+format+"\n", args...)
	os.Exit(1)
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fatal("marshal json: %v", err)
	}
	fmt.Println(string(data))
}

// newFlags returns a FlagSet pre-loaded with the three global flags.
// Returns pointers for node, agent, and jsonOut.
func newFlags(name string) (*flag.FlagSet, *string, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	node := fs.String("node", defaultNode(), "Node API URL")
	agent := fs.String("agent", defaultAgent(), "Agent ID")
	jsonOut := fs.Bool("json", false, "Output raw JSON")
	return fs, node, agent, jsonOut
}

// requireAgent exits with an error message if agentID is empty.
func requireAgent(agentID, flag string) {
	if agentID == "" {
		fatal("-%s is required (or set AETHERNET_AGENT)", flag)
	}
}

// ---------------------------------------------------------------------------
// Raw HTTP helpers for endpoints not covered by pkg/sdk
// ---------------------------------------------------------------------------

// apiGet issues a GET request to nodeURL+path and decodes the JSON body into out.
func apiGet(nodeURL, path string, out any) error {
	resp, err := http.Get(nodeURL + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// apiPost issues a POST request with a JSON body and decodes the response into out.
func apiPost(nodeURL, path string, reqBody any, out any) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	resp, err := http.Post(nodeURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, body)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func apiError(code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error != "" {
		return fmt.Errorf("api error %d: %s", code, e.Error)
	}
	return fmt.Errorf("api error %d: %s", code, strings.TrimSpace(string(body)))
}

// ---------------------------------------------------------------------------
// Extra response types not covered by pkg/sdk
// ---------------------------------------------------------------------------

type registerResponse struct {
	AgentID              string `json:"agent_id"`
	FingerprintHash      string `json:"fingerprint_hash"`
	DepositAddress       string `json:"deposit_address,omitempty"`
	OnboardingAllocation uint64 `json:"onboarding_allocation,omitempty"`
	TrustLimit           uint64 `json:"trust_limit,omitempty"`
}

type recentEventItem struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	AgentID         string `json:"agent_id"`
	CausalTimestamp uint64 `json:"causal_timestamp"`
	StakeAmount     uint64 `json:"stake_amount"`
	SettlementState string `json:"settlement_state"`
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func runStatus(args []string) {
	fs, node, _, jsonOut := newFlags("status")
	_ = fs.Parse(args)

	c := sdk.New(*node, nil)

	st, err := c.Status()
	if err != nil {
		fatal("status: %v", err)
	}
	ec, err := c.Economics()
	if err != nil {
		fatal("economics: %v", err)
	}

	if *jsonOut {
		printJSON(map[string]any{"status": st, "economics": ec})
		return
	}

	printHeader("AetherNet Node Status")
	printRow("Node", *node)
	printRow("Agent", truncateID(st.AgentID, 24))
	printRow("Version", st.Version)
	printRow("DAG Size", formatNumber(uint64(st.DAGSize))+" events")
	printRow("Peers", strconv.Itoa(st.Peers))
	printRow("Pending", fmt.Sprintf("%d verifications", st.OCSPending))
	fmt.Println()
	printHeader("Economics")
	printRow("Total Supply", formatAET(ec.TotalSupply))
	printRow("Circulating", formatAET(ec.CirculatingSupply))
	printRow("Total Burned", formatAET(ec.TotalBurned))
	printRow("Fees Collected", formatAET(ec.TotalCollected))
	printRow("Treasury", formatAET(ec.TreasuryAccrued))
	printRow("Onboarding Allocated", formatAET(ec.OnboardingAllocated))
}

// ---------------------------------------------------------------------------
// balance
// ---------------------------------------------------------------------------

func runBalance(args []string) {
	fs, node, agent, jsonOut := newFlags("balance")
	_ = fs.Parse(args)
	requireAgent(*agent, "agent")

	c := sdk.New(*node, nil)
	bal, err := c.Balance(*agent)
	if err != nil {
		fatal("balance: %v", err)
	}

	if *jsonOut {
		printJSON(bal)
		return
	}

	printHeader("Balance")
	printRow("Agent", truncateID(*agent, 24))
	printRow("Balance", formatAET(bal.Balance))
	printRow("Currency", bal.Currency)
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

func runInfo(args []string) {
	fs, node, agent, jsonOut := newFlags("info")
	_ = fs.Parse(args)
	requireAgent(*agent, "agent")

	c := sdk.New(*node, nil)

	profile, err := c.Profile(*agent)
	if err != nil {
		fatal("profile: %v", err)
	}
	stakeInfo, err := c.StakeInfo(*agent)
	if err != nil {
		fatal("stake info: %v", err)
	}

	// Best-effort address lookup (returns "" when wallet not enabled).
	var addrResp struct {
		Address string `json:"address"`
	}
	_ = apiGet(*node, "/v1/agents/"+*agent+"/address", &addrResp)

	if *jsonOut {
		printJSON(map[string]any{"profile": profile, "stake": stakeInfo})
		return
	}

	printHeader("Agent Profile")
	printRow("Agent ID", truncateID(profile.AgentID, 32))
	if addrResp.Address != "" {
		printRow("Address", truncateID(addrResp.Address, 32))
	}
	printRow("Reputation", formatNumber(profile.ReputationScore))
	printRow("Tasks Completed", formatNumber(profile.TasksCompleted))
	printRow("Tasks Failed", formatNumber(profile.TasksFailed))
	printRow("Value Generated", formatAET(profile.TotalValueGenerated))
	printRow("Trust Limit", formatAET(profile.OptimisticTrustLimit))
	fmt.Println()
	printHeader("Staking")
	printRow("Staked", formatAET(stakeInfo.StakedAmount))
	printRow("Trust Multiplier", fmt.Sprintf("%dx", stakeInfo.TrustMultiplier))
	printRow("Trust Limit", formatAET(stakeInfo.TrustLimit))
	printRow("Days Staked", formatNumber(stakeInfo.DaysStaked))
	printRow("Effective Tasks", formatNumber(stakeInfo.EffectiveTasks))
	if stakeInfo.LastActivity > 0 {
		printRow("Last Activity", formatTimeAgo(stakeInfo.LastActivity))
	}
}

// ---------------------------------------------------------------------------
// transfer
// ---------------------------------------------------------------------------

func runTransfer(args []string) {
	fs, node, agent, jsonOut := newFlags("transfer")
	to := fs.String("to", "", "Recipient agent ID")
	amount := fs.Uint64("amount", 0, "Amount in micro-AET")
	memo := fs.String("memo", "", "Optional memo")
	stakeAmt := fs.Uint64("stake", 1000, "Stake amount (min 1000)")
	_ = fs.Parse(args)

	if *to == "" {
		fatal("-to is required")
	}
	if *amount == 0 {
		fatal("-amount must be > 0")
	}
	fromAgent := *agent
	if fromAgent == "" {
		fromAgent = "(node default)"
	}

	c := sdk.New(*node, nil)
	eventID, err := c.Transfer(sdk.TransferRequest{
		ToAgent:     *to,
		Amount:      *amount,
		Memo:        *memo,
		StakeAmount: *stakeAmt,
	})
	if err != nil {
		fatal("transfer: %v", err)
	}

	if *jsonOut {
		printJSON(map[string]any{
			"from":     fromAgent,
			"to":       *to,
			"amount":   *amount,
			"memo":     *memo,
			"event_id": eventID,
			"status":   "optimistic",
		})
		return
	}

	printHeader("Transfer Submitted")
	printRow("From", truncateID(fromAgent, 24))
	printRow("To", truncateID(*to, 24))
	printRow("Amount", formatAET(*amount))
	if *memo != "" {
		printRow("Memo", *memo)
	}
	printRow("Event", truncateID(eventID, 32))
	fmt.Println()
	fmt.Println("  Status: OPTIMISTIC (settled against trust limit)")
}

// ---------------------------------------------------------------------------
// stake
// ---------------------------------------------------------------------------

func runStake(args []string) {
	fs, node, agent, jsonOut := newFlags("stake")
	amount := fs.Uint64("amount", 0, "Amount to stake in micro-AET")
	_ = fs.Parse(args)
	requireAgent(*agent, "agent")
	if *amount == 0 {
		fatal("-amount must be > 0")
	}

	c := sdk.New(*node, nil)
	result, err := c.Stake(sdk.StakeRequest{AgentID: *agent, Amount: *amount})
	if err != nil {
		fatal("stake: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	printHeader("Staked")
	printRow("Staked", formatAET(*amount)+" added")
	printRow("Total Stake", formatAET(result.StakedAmount))
	printRow("Trust Limit", formatAET(result.TrustLimit))
}

// ---------------------------------------------------------------------------
// unstake
// ---------------------------------------------------------------------------

func runUnstake(args []string) {
	fs, node, agent, jsonOut := newFlags("unstake")
	amount := fs.Uint64("amount", 0, "Amount to unstake in micro-AET")
	_ = fs.Parse(args)
	requireAgent(*agent, "agent")
	if *amount == 0 {
		fatal("-amount must be > 0")
	}

	c := sdk.New(*node, nil)
	result, err := c.Unstake(sdk.StakeRequest{AgentID: *agent, Amount: *amount})
	if err != nil {
		fatal("unstake: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	printHeader("Unstaked")
	printRow("Unstaked", formatAET(*amount)+" removed")
	printRow("Total Stake", formatAET(result.StakedAmount))
	printRow("Trust Limit", formatAET(result.TrustLimit))
}

// ---------------------------------------------------------------------------
// register
// ---------------------------------------------------------------------------

func runRegister(args []string) {
	fs, node, _, jsonOut := newFlags("register")
	capabilitiesStr := fs.String("capabilities", "", "Comma-separated capability types (e.g. inference,embedding)")
	_ = fs.Parse(args)

	var caps []map[string]any
	if *capabilitiesStr != "" {
		for _, c := range strings.Split(*capabilitiesStr, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				caps = append(caps, map[string]any{"type": c})
			}
		}
	}

	var result registerResponse
	if err := apiPost(*node, "/v1/agents", map[string]any{"capabilities": caps}, &result); err != nil {
		fatal("register: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	printHeader("Agent Registered")
	printRow("Agent ID", truncateID(result.AgentID, 32))
	printRow("Fingerprint", truncateID(result.FingerprintHash, 24))
	if result.DepositAddress != "" {
		printRow("Deposit Address", truncateID(result.DepositAddress, 32))
	}
	if result.OnboardingAllocation > 0 {
		printRow("Onboarding Grant", formatAET(result.OnboardingAllocation))
	}
	if result.TrustLimit > 0 {
		printRow("Initial Trust Limit", formatAET(result.TrustLimit))
	}
}

// ---------------------------------------------------------------------------
// pending
// ---------------------------------------------------------------------------

func runPending(args []string) {
	fs, node, _, jsonOut := newFlags("pending")
	_ = fs.Parse(args)

	c := sdk.New(*node, nil)
	items, err := c.Pending()
	if err != nil {
		fatal("pending: %v", err)
	}

	if *jsonOut {
		printJSON(items)
		return
	}

	fmt.Printf("Pending verifications: %d\n\n", len(items))
	if len(items) == 0 {
		return
	}

	headers := []string{"Event ID", "Type", "Agent", "Amount", "Submitted"}
	rows := make([][]string, len(items))
	for i, item := range items {
		rows[i] = []string{
			truncateID(item.EventID, 20),
			item.EventType,
			truncateID(item.AgentID, 16),
			formatAET(item.Amount),
			item.OptimisticAt.Format(time.RFC3339),
		}
	}
	printTable(headers, rows)
}

// ---------------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------------

func runVerify(args []string) {
	fs, node, _, jsonOut := newFlags("verify")
	eventID := fs.String("event", "", "Event ID to verify")
	verdictStr := fs.String("verdict", "", "approve or reject")
	verifiedValue := fs.Uint64("value", 0, "Verified value (for generation events)")
	_ = fs.Parse(args)

	if *eventID == "" {
		fatal("-event is required")
	}
	if *verdictStr == "" {
		fatal("-verdict is required (approve or reject)")
	}
	var verdict bool
	switch strings.ToLower(*verdictStr) {
	case "approve", "true", "yes":
		verdict = true
	case "reject", "false", "no":
		verdict = false
	default:
		fatal("-verdict must be approve or reject")
	}

	c := sdk.New(*node, nil)
	result, err := c.Verify(sdk.VerifyRequest{
		EventID:       *eventID,
		Verdict:       verdict,
		VerifiedValue: *verifiedValue,
	})
	if err != nil {
		fatal("verify: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	printHeader("Verification Submitted")
	printRow("Event", truncateID(result.EventID, 32))
	printRow("Verdict", *verdictStr)
	printRow("Status", strings.ToUpper(result.Status))
}

// ---------------------------------------------------------------------------
// economics
// ---------------------------------------------------------------------------

func runEconomics(args []string) {
	fs, node, _, jsonOut := newFlags("economics")
	_ = fs.Parse(args)

	c := sdk.New(*node, nil)
	ec, err := c.Economics()
	if err != nil {
		fatal("economics: %v", err)
	}

	if *jsonOut {
		printJSON(ec)
		return
	}

	printHeader("Network Economics")
	printRow("Total Supply", formatAET(ec.TotalSupply))
	printRow("Circulating Supply", formatAET(ec.CirculatingSupply))
	printRow("Total Burned", formatAET(ec.TotalBurned))
	printRow("Fees Collected", formatAET(ec.TotalCollected))
	printRow("Treasury Accrued", formatAET(ec.TreasuryAccrued))
	fmt.Println()
	printRow("Onboarding Pool", formatAET(ec.OnboardingPoolTotal))
	printRow("Onboarding Allocated", formatAET(ec.OnboardingAllocated))
	printRow("Max Onboardable", formatNumber(ec.OnboardingMaxAgents)+" agents")
	printRow("Fee Basis Points", fmt.Sprintf("%d bps (%.2f%%)", ec.FeeBasisPoints, float64(ec.FeeBasisPoints)/100))
}

// ---------------------------------------------------------------------------
// agents
// ---------------------------------------------------------------------------

func runAgents(args []string) {
	fs, node, _, jsonOut := newFlags("agents")
	limit := fs.Int("limit", 20, "Maximum number of agents to display")
	sortBy := fs.String("sort", "reputation", "Sort by: reputation, balance, tasks")
	_ = fs.Parse(args)

	// Use leaderboard endpoint which includes balance + stake info and is pre-sorted.
	var entries []struct {
		Rank            int    `json:"rank"`
		AgentID         string `json:"agent_id"`
		ReputationScore uint64 `json:"reputation_score"`
		TasksCompleted  uint64 `json:"tasks_completed"`
		Balance         uint64 `json:"balance"`
		TrustLimit      uint64 `json:"trust_limit"`
		StakedAmount    uint64 `json:"staked_amount"`
	}
	path := fmt.Sprintf("/v1/agents/leaderboard?sort=%s&limit=%d", *sortBy, *limit)
	if err := apiGet(*node, path, &entries); err != nil {
		// Fall back to plain /v1/agents listing if leaderboard isn't available.
		c := sdk.New(*node, nil)
		agents, err2 := c.Agents()
		if err2 != nil {
			fatal("agents: %v", err2)
		}
		if *jsonOut {
			printJSON(agents)
			return
		}
		headers := []string{"#", "Agent ID", "Reputation", "Tasks", "Trust Limit"}
		rows := make([][]string, len(agents))
		for i, a := range agents {
			rows[i] = []string{
				strconv.Itoa(i + 1),
				truncateID(a.AgentID, 24),
				formatNumber(a.ReputationScore),
				formatNumber(a.TasksCompleted),
				formatAET(a.OptimisticTrustLimit),
			}
		}
		printTable(headers, rows)
		return
	}

	if *jsonOut {
		printJSON(entries)
		return
	}

	fmt.Printf("Agents (sorted by %s, limit %d)\n\n", *sortBy, *limit)
	headers := []string{"Rank", "Agent ID", "Reputation", "Tasks", "Balance", "Staked"}
	rows := make([][]string, len(entries))
	for i, e := range entries {
		rows[i] = []string{
			strconv.Itoa(e.Rank),
			truncateID(e.AgentID, 20),
			formatNumber(e.ReputationScore),
			formatNumber(e.TasksCompleted),
			formatAET(e.Balance),
			formatAET(e.StakedAmount),
		}
	}
	printTable(headers, rows)
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func runSearch(args []string) {
	fs, node, _, jsonOut := newFlags("search")
	query := fs.String("query", "", "Search keyword")
	category := fs.String("category", "", "Filter by category")
	limit := fs.Int("limit", 20, "Maximum results")
	_ = fs.Parse(args)

	c := sdk.New(*node, nil)
	listings, err := c.SearchServices(*query, *category, *limit)
	if err != nil {
		fatal("search: %v", err)
	}

	if *jsonOut {
		printJSON(listings)
		return
	}

	if *query != "" || *category != "" {
		fmt.Printf("Search results for ")
		if *query != "" {
			fmt.Printf("query=%q ", *query)
		}
		if *category != "" {
			fmt.Printf("category=%q ", *category)
		}
		fmt.Printf("(%d results)\n\n", len(listings))
	} else {
		fmt.Printf("Service listings (%d results)\n\n", len(listings))
	}

	if len(listings) == 0 {
		fmt.Println("  (no listings found)")
		return
	}

	headers := []string{"Agent", "Name", "Category", "Price", "Tags"}
	rows := make([][]string, len(listings))
	for i, l := range listings {
		rows[i] = []string{
			truncateID(l.AgentID, 16),
			l.Name,
			l.Category,
			formatAET(l.PriceAET),
			strings.Join(l.Tags, ", "),
		}
	}
	printTable(headers, rows)
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

func runHistory(args []string) {
	fs, node, agent, jsonOut := newFlags("history")
	limit := fs.Int("limit", 20, "Number of recent events to show")
	_ = fs.Parse(args)

	var events []recentEventItem
	path := fmt.Sprintf("/v1/events/recent?limit=%d", *limit)
	if err := apiGet(*node, path, &events); err != nil {
		fatal("history: %v", err)
	}

	// Filter by agent if specified.
	if *agent != "" {
		var filtered []recentEventItem
		for _, e := range events {
			if e.AgentID == *agent {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	if *jsonOut {
		printJSON(events)
		return
	}

	label := "Recent events"
	if *agent != "" {
		label = fmt.Sprintf("Recent events for %s", truncateID(*agent, 16))
	}
	fmt.Printf("%s (%d)\n\n", label, len(events))

	if len(events) == 0 {
		fmt.Println("  (no events)")
		return
	}

	headers := []string{"Event ID", "Type", "Agent", "Timestamp", "State"}
	rows := make([][]string, len(events))
	for i, e := range events {
		rows[i] = []string{
			truncateID(e.ID, 18),
			e.Type,
			truncateID(e.AgentID, 16),
			strconv.FormatUint(e.CausalTimestamp, 10),
			e.SettlementState,
		}
	}
	printTable(headers, rows)
}
