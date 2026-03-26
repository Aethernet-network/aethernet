// Command aet is the AetherNet CLI. It manages wallets, signs requests with
// Ed25519, and interacts with the testnet API.
//
// Usage:
//
//	aet <command> [subcommand] [options]
//
// Wallet:
//
//	wallet create [--name NAME]     Generate keypair, register on testnet
//	wallet list                     List wallets
//	wallet import --file PATH       Import a wallet file
//	wallet export [--name NAME]     Export wallet to file
//
// Balance & Staking:
//
//	balance [--agent ID]            Show balance, staked, trust limit
//	faucet                          Request testnet AET
//	stake <amount>                  Stake AET (amount in AET)
//	unstake <amount>                Unstake AET (amount in AET)
//
// Tasks:
//
//	task list [--status S] [--limit N]
//	task status <task_id>
//	task post --title T --description D --category C --budget B
//	task claim <task_id>
//	task submit <task_id> --result "text" [--uri URL]
//
// Network:
//
//	status                          Node health and economics
//	agents [--limit N]              List registered agents
//
// Global flags:
//
//	--url URL     API URL (env: AETHERNET_URL, default: https://testnet.aethernet.network)
//	--json        Machine-readable JSON output
//	--full        Show full IDs (no truncation)
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

const cliVersion = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	// Wallet
	case "wallet":
		if len(args) == 0 {
			fatal("usage: aet wallet <create|list|import|export>")
		}
		switch args[0] {
		case "create":
			runWalletCreate(args[1:])
		case "list":
			runWalletList(args[1:])
		case "import":
			runWalletImport(args[1:])
		case "export":
			runWalletExport(args[1:])
		default:
			fatal("unknown wallet subcommand: %s", args[0])
		}

	// Balance & Staking
	case "balance":
		runBalance(args)
	case "faucet":
		runFaucet(args)
	case "stake":
		runStake(args)
	case "unstake":
		runUnstake(args)

	// Tasks
	case "task":
		if len(args) == 0 {
			fatal("usage: aet task <list|status|post|claim|submit>")
		}
		switch args[0] {
		case "list":
			runTaskList(args[1:])
		case "status":
			runTaskStatus(args[1:])
		case "post":
			runTaskPost(args[1:])
		case "claim":
			runTaskClaim(args[1:])
		case "submit":
			runTaskSubmit(args[1:])
		default:
			fatal("unknown task subcommand: %s", args[0])
		}

	// Network
	case "status":
		runStatus(args)
	case "agents":
		runAgents(args)

	case "version", "--version":
		fmt.Printf("aet %s\n", cliVersion)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "aet: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`aet %s — AetherNet CLI

Usage: aet <command> [options]

Wallet:
  wallet create [--name NAME]     Generate keypair and register
  wallet list                     List local wallets
  wallet import --file PATH       Import wallet file
  wallet export [--name NAME]     Export wallet file

Balance & Staking:
  balance [--agent ID]            Balance, staked, trust limit
  faucet                          Request 5,000 testnet AET
  stake <amount>                  Stake AET (amount in AET)
  unstake <amount>                Unstake AET (amount in AET)

Tasks:
  task list [--status S]          List tasks
  task status <task_id>           Task details
  task post --title T ...         Post a new task
  task claim <task_id>            Claim a task
  task submit <task_id> ...       Submit result

Network:
  status                          Node health
  agents [--limit N]              List agents

Global flags:
  --url URL     API URL (default: https://testnet.aethernet.network)
  --json        JSON output
  --full        Show full IDs

Environment:
  AETHERNET_URL            Override --url
  AETHERNET_KEY_PASSWORD   Skip password prompt
`, cliVersion)
}

// ── Globals ──────────────────────────────────────────────────────────────────

func defaultURL() string {
	if v := os.Getenv("AETHERNET_URL"); v != "" {
		return v
	}
	if v := os.Getenv("AETHERNET_NODE"); v != "" {
		return v
	}
	return "https://testnet.aethernet.network"
}

func newFlags(name string) (*flag.FlagSet, *string, *bool, *bool) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	url := fs.String("url", defaultURL(), "API URL")
	jsonOut := fs.Bool("json", false, "JSON output")
	full := fs.Bool("full", false, "Full IDs")
	return fs, url, jsonOut, full
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "aet: "+format+"\n", args...)
	os.Exit(1)
}

func printJSON(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(data))
}

func tid(id string, full bool) string {
	if full {
		return id
	}
	return truncateID(id, 16)
}

// aetToMicro converts AET (as string) to micro-AET.
func aetToMicro(s string) uint64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		fatal("invalid amount: %s", s)
	}
	return uint64(math.Round(f * 1_000_000))
}

// ── Wallet Commands ──────────────────────────────────────────────────────────

func runWalletCreate(args []string) {
	fs, url, jsonOut, _ := newFlags("wallet create")
	name := fs.String("name", "default", "Wallet name")
	_ = fs.Parse(args)

	wf, _, err := createWallet(*name)
	if err != nil {
		fatal("create wallet: %v", err)
	}

	// Register on testnet.
	result, regErr := registerAgentOnTestnet(*url, wf)

	if *jsonOut {
		out := map[string]any{"agent_id": wf.AgentID, "name": wf.Name, "public_key": wf.PublicKey}
		if regErr == nil {
			out["registration"] = result
		}
		printJSON(out)
		return
	}

	printHeader("Wallet Created")
	printRow("Name", wf.Name)
	if wf.Address != "" {
		printRow("Address", wf.Address)
	}
	printRow("Agent ID", wf.AgentID)
	printRow("Stored", walletPath(wf.Name))
	fmt.Println()
	if regErr == nil {
		alloc, _ := result["onboarding_allocation"].(float64)
		if alloc > 0 {
			printRow("Onboarding Grant", formatAET(uint64(alloc)))
		}
		fmt.Println("\n  Registered on testnet successfully.")
	} else {
		fmt.Fprintf(os.Stderr, "  Registration: %v\n", regErr)
	}
}

func runWalletList(args []string) {
	_, _, jsonOut, full := newFlags("wallet list")
	_ = full
	wallets, err := listWallets()
	if err != nil {
		fatal("list wallets: %v", err)
	}
	active := activeWalletName()

	if *jsonOut {
		printJSON(wallets)
		return
	}

	if len(wallets) == 0 {
		fmt.Println("No wallets found. Run: aet wallet create")
		return
	}

	printHeader("Wallets")
	headers := []string{"", "Name", "Address", "Created"}
	var rows [][]string
	for _, w := range wallets {
		marker := " "
		if w.Name == active {
			marker = "*"
		}
		addrDisplay := truncateID(w.AgentID, 16)
		if w.Address != "" {
			addrDisplay = truncateID(w.Address, 20)
		}
		rows = append(rows, []string{marker, w.Name, addrDisplay, w.CreatedAt[:10]})
	}
	printTable(headers, rows)
	fmt.Printf("\n  Active wallet: %s\n", active)
}

func runWalletImport(args []string) {
	fs := flag.NewFlagSet("wallet import", flag.ExitOnError)
	file := fs.String("file", "", "Wallet file to import")
	_ = fs.Parse(args)
	if *file == "" {
		fatal("--file is required")
	}
	if err := importWallet(*file); err != nil {
		fatal("import: %v", err)
	}
	fmt.Println("Wallet imported successfully.")
}

func runWalletExport(args []string) {
	fs := flag.NewFlagSet("wallet export", flag.ExitOnError)
	name := fs.String("name", activeWalletName(), "Wallet name")
	out := fs.String("out", "", "Output path (default: <name>.json)")
	_ = fs.Parse(args)
	if *out == "" {
		*out = *name + ".json"
	}
	if err := exportWallet(*name, *out); err != nil {
		fatal("export: %v", err)
	}
	fmt.Printf("Exported to %s\n", *out)
}

// ── Balance & Staking ────────────────────────────────────────────────────────

func runBalance(args []string) {
	fs, url, jsonOut, full := newFlags("balance")
	agent := fs.String("agent", "", "Agent ID (default: active wallet)")
	_ = fs.Parse(args)

	agentID := *agent
	if agentID == "" {
		wf, err := getActiveWallet()
		if err != nil {
			fatal("no --agent specified and no active wallet (run: aet wallet create)")
		}
		agentID = wf.AgentID
	}

	var bal struct {
		Balance  uint64 `json:"balance"`
		Currency string `json:"currency"`
	}
	if err := apiGet(*url, "/v1/agents/"+agentID+"/balance", &bal); err != nil {
		fatal("balance: %v", err)
	}
	var stake struct {
		StakedAmount    uint64 `json:"staked_amount"`
		TrustLimit      uint64 `json:"trust_limit"`
		TrustMultiplier uint64 `json:"trust_multiplier"`
	}
	_ = apiGet(*url, "/v1/agents/"+agentID+"/stake", &stake)

	if *jsonOut {
		printJSON(map[string]any{"agent_id": agentID, "balance": bal.Balance, "staked": stake.StakedAmount, "trust_limit": stake.TrustLimit})
		return
	}

	printHeader("Balance")
	printRow("Agent", tid(agentID, *full))
	printRow("Balance", formatAET(bal.Balance))
	printRow("Staked", formatAET(stake.StakedAmount))
	if bal.Balance > stake.StakedAmount {
		printRow("Available", formatAET(bal.Balance-stake.StakedAmount))
	}
	printRow("Trust Limit", formatAET(stake.TrustLimit))
}

func runFaucet(args []string) {
	fs, url, jsonOut, _ := newFlags("faucet")
	_ = fs.Parse(args)

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/faucet", map[string]any{"agent_id": wf.AgentID}, wf.AgentID, pk, &result); err != nil {
		fatal("faucet: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	amt, _ := result["amount"].(float64)
	evID, _ := result["event_id"].(string)
	printHeader("Faucet Grant")
	printRow("Amount", formatAET(uint64(amt)))
	printRow("Event", truncateID(evID, 24))
	fmt.Println("\n  Grant settles through consensus (~15s).")
}

func runStake(args []string) {
	fs, url, jsonOut, _ := newFlags("stake")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("usage: aet stake <amount_in_AET>")
	}
	amount := aetToMicro(fs.Arg(0))

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/stake", map[string]any{"agent_id": wf.AgentID, "amount": amount}, wf.AgentID, pk, &result); err != nil {
		fatal("stake: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	staked, _ := result["staked_amount"].(float64)
	evID, _ := result["event_id"].(string)
	printHeader("Staked")
	printRow("Amount", formatAET(amount))
	printRow("Total Staked", formatAET(uint64(staked)))
	if evID != "" {
		printRow("Event", truncateID(evID, 24))
	}
}

func runUnstake(args []string) {
	fs, url, jsonOut, _ := newFlags("unstake")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("usage: aet unstake <amount_in_AET>")
	}
	amount := aetToMicro(fs.Arg(0))

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/unstake", map[string]any{"agent_id": wf.AgentID, "amount": amount}, wf.AgentID, pk, &result); err != nil {
		fatal("unstake: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	staked, _ := result["staked_amount"].(float64)
	evID, _ := result["event_id"].(string)
	printHeader("Unstaked")
	printRow("Amount", formatAET(amount))
	printRow("Remaining Stake", formatAET(uint64(staked)))
	if evID != "" {
		printRow("Event", truncateID(evID, 24))
	}
}

// ── Task Commands ────────────────────────────────────────────────────────────

func runTaskList(args []string) {
	fs, url, jsonOut, full := newFlags("task list")
	status := fs.String("status", "", "Filter by status (open, claimed, submitted, completed)")
	limit := fs.Int("limit", 20, "Max results")
	_ = fs.Parse(args)

	path := fmt.Sprintf("/v1/tasks?limit=%d", *limit)
	if *status != "" {
		path += "&status=" + *status
	}

	var tasks []map[string]any
	if err := apiGet(*url, path, &tasks); err != nil {
		fatal("task list: %v", err)
	}

	if *jsonOut {
		printJSON(tasks)
		return
	}

	fmt.Printf("Tasks (%d)\n\n", len(tasks))
	headers := []string{"ID", "Title", "Category", "Budget", "Status"}
	var rows [][]string
	for _, t := range tasks {
		id, _ := t["id"].(string)
		title, _ := t["title"].(string)
		cat, _ := t["category"].(string)
		budget, _ := t["budget"].(float64)
		st, _ := t["status"].(string)
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		rows = append(rows, []string{tid(id, *full), title, cat, formatAET(uint64(budget)), st})
	}
	printTable(headers, rows)
}

func runTaskStatus(args []string) {
	fs, url, jsonOut, full := newFlags("task status")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("usage: aet task status <task_id>")
	}
	taskID := fs.Arg(0)

	var task map[string]any
	if err := apiGet(*url, "/v1/tasks/"+taskID, &task); err != nil {
		fatal("task status: %v", err)
	}

	if *jsonOut {
		printJSON(task)
		return
	}

	title, _ := task["title"].(string)
	st, _ := task["status"].(string)
	cat, _ := task["category"].(string)
	budget, _ := task["budget"].(float64)
	poster, _ := task["poster_id"].(string)
	claimer, _ := task["claimer_id"].(string)
	desc, _ := task["description"].(string)

	printHeader("Task Details")
	printRow("ID", tid(taskID, *full))
	printRow("Title", title)
	printRow("Status", strings.ToUpper(st))
	printRow("Category", cat)
	printRow("Budget", formatAET(uint64(budget)))
	printRow("Poster", tid(poster, *full))
	if claimer != "" {
		printRow("Claimer", tid(claimer, *full))
	}
	if desc != "" {
		fmt.Println()
		printRow("Description", "")
		fmt.Printf("  %s\n", desc)
	}
}

func runTaskPost(args []string) {
	fs, url, jsonOut, _ := newFlags("task post")
	title := fs.String("title", "", "Task title (required)")
	desc := fs.String("description", "", "Task description")
	cat := fs.String("category", "general", "Category")
	budgetStr := fs.String("budget", "", "Budget in AET (required)")
	_ = fs.Parse(args)

	if *title == "" {
		fatal("--title is required")
	}
	if *budgetStr == "" {
		fatal("--budget is required (in AET)")
	}
	budget := aetToMicro(*budgetStr)

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/tasks", map[string]any{
		"poster_id":   wf.AgentID,
		"title":       *title,
		"description": *desc,
		"category":    *cat,
		"budget":      budget,
	}, wf.AgentID, pk, &result); err != nil {
		fatal("post task: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	id, _ := result["id"].(string)
	st, _ := result["status"].(string)
	printHeader("Task Posted")
	printRow("ID", id)
	printRow("Title", *title)
	printRow("Budget", formatAET(budget))
	printRow("Status", st)
}

func runTaskClaim(args []string) {
	fs, url, jsonOut, _ := newFlags("task claim")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("usage: aet task claim <task_id>")
	}
	taskID := fs.Arg(0)

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/tasks/"+taskID+"/claim", map[string]any{
		"agent_id": wf.AgentID,
	}, wf.AgentID, pk, &result); err != nil {
		fatal("claim task: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	st, _ := result["status"].(string)
	printHeader("Task Claimed")
	printRow("Task", taskID)
	printRow("Status", st)
}

func runTaskSubmit(args []string) {
	fs, url, jsonOut, _ := newFlags("task submit")
	result := fs.String("result", "", "Result text (required)")
	uri := fs.String("uri", "", "Result URI (optional)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("usage: aet task submit <task_id> --result \"text\"")
	}
	if *result == "" {
		fatal("--result is required")
	}
	taskID := fs.Arg(0)

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	hash := sha256.Sum256([]byte(*result))
	hashHex := hex.EncodeToString(hash[:])

	var resp map[string]any
	if err := signedPost(*url, "/v1/tasks/"+taskID+"/submit", map[string]any{
		"claimer_id":  wf.AgentID,
		"result_hash": hashHex,
		"result_note": *result,
		"result_uri":  *uri,
	}, wf.AgentID, pk, &resp); err != nil {
		fatal("submit: %v", err)
	}

	if *jsonOut {
		printJSON(resp)
		return
	}

	st, _ := resp["status"].(string)
	printHeader("Result Submitted")
	printRow("Task", taskID)
	printRow("Hash", truncateID(hashHex, 24))
	printRow("Status", st)
}

// ── Network Commands ─────────────────────────────────────────────────────────

func runStatus(args []string) {
	fs, url, jsonOut, _ := newFlags("status")
	_ = fs.Parse(args)

	var st map[string]any
	if err := apiGet(*url, "/v1/status", &st); err != nil {
		fatal("status: %v", err)
	}

	if *jsonOut {
		printJSON(st)
		return
	}

	printHeader("AetherNet Node")
	printRow("URL", *url)
	if v, ok := st["agent_id"].(string); ok {
		printRow("Agent", truncateID(v, 24))
	}
	if v, ok := st["version"].(string); ok {
		printRow("Version", v)
	}
	if v, ok := st["dag_size"].(float64); ok {
		printRow("DAG Size", formatNumber(uint64(v))+" events")
	}
	if v, ok := st["peers"].(float64); ok {
		printRow("Peers", strconv.Itoa(int(v)))
	}
	if v, ok := st["supply_ratio"].(float64); ok {
		printRow("Supply Ratio", fmt.Sprintf("%.6f", v))
	}
}

func runAgents(args []string) {
	fs, url, jsonOut, full := newFlags("agents")
	limit := fs.Int("limit", 20, "Max results")
	_ = fs.Parse(args)

	path := fmt.Sprintf("/v1/agents/leaderboard?sort=reputation&limit=%d", *limit)
	var agents []map[string]any
	if err := apiGet(*url, path, &agents); err != nil {
		fatal("agents: %v", err)
	}

	if *jsonOut {
		printJSON(agents)
		return
	}

	fmt.Printf("Agents (%d)\n\n", len(agents))
	headers := []string{"Agent ID", "Balance", "Staked", "Reputation", "Tasks"}
	var rows [][]string
	for _, a := range agents {
		id, _ := a["agent_id"].(string)
		bal, _ := a["balance"].(float64)
		stk, _ := a["staked_amount"].(float64)
		rep, _ := a["reputation_score"].(float64)
		tasks, _ := a["tasks_completed"].(float64)
		rows = append(rows, []string{
			tid(id, *full),
			formatAET(uint64(bal)),
			formatAET(uint64(stk)),
			formatNumber(uint64(rep)),
			formatNumber(uint64(tasks)),
		})
	}
	printTable(headers, rows)
}
