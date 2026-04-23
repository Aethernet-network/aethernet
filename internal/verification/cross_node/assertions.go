// Per-node ledger snapshots and cross-node byte-equality assertions.
//
// A LedgerSnapshot is the projected state the harness compares across
// nodes. It deliberately covers every state surface that the F4 selection
// race can perturb:
//
//   - Per-agent token balances (treasury, poster, worker, every
//     validator). The headline metric — divergent settlements show up
//     here as different validators getting paid on different nodes.
//   - Escrow bucket balances per task. A node that settled "pass" drains
//     the bucket via the worker/validator/treasury path; a node that
//     settled "fail" drains via the poster-refund/treasury path. Either
//     way the bucket reaches zero, but conservation of who-got-what is
//     visible only at the agent-balance level.
//   - Per-task settlement state (status + which-paid flags). Direct
//     evidence of which event each node admitted first.
//
// Snapshots are deterministic — every map iteration is replaced by a
// sorted enumeration before serialization. Comparison is byte-equal on
// the canonical JSON form, matching the §2.2 invariant A-1 ("any
// per-node ledger-state deviation for any corpus fails the build").

package cross_node

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// AgentBalance is a single agent's spendable balance. Used as the
// element type of LedgerSnapshot.Balances so map iteration can be
// replaced by a sorted slice (deterministic serialization).
type AgentBalance struct {
	AgentID string `json:"agent_id"`
	Amount  uint64 `json:"amount"`
}

// EscrowBucketSnapshot captures the residual balance of one task's
// escrow bucket. Production accounting drains every bucket to zero
// regardless of verdict; non-zero residuals would themselves be a
// regression.
type EscrowBucketSnapshot struct {
	TaskID  string `json:"task_id"`
	Balance uint64 `json:"balance"`
	// SettlementStarted reflects escrow.HasSettlementStarted at snapshot
	// time. True if any per-recipient paid flag is set on the entry, or
	// the entry has been deleted (post-full-settlement).
	SettlementStarted bool `json:"settlement_started"`
}

// TaskStatusSnapshot captures the terminal status of a settled task.
// Cross-node divergence in settled verdict is most concisely visible
// here (Completed vs Rejected on the same TaskID).
type TaskStatusSnapshot struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// LedgerSnapshot is the canonical projected state of one node at one
// moment in time. Deterministically serializable (all maps converted to
// sorted slices). Cross-node byte-equality on the JSON form is the §2.2
// invariant A-1.
type LedgerSnapshot struct {
	NodeIndex   int                    `json:"node_index"`
	Balances    []AgentBalance         `json:"balances"`
	Escrows     []EscrowBucketSnapshot `json:"escrows"`
	TaskStatus  []TaskStatusSnapshot   `json:"task_status"`
	TotalSupply uint64                 `json:"total_supply"`
}

// SnapshotInputs identifies the agent IDs and task IDs the snapshot
// should cover. The harness passes the cluster's well-known set:
// treasury, poster, worker, every validator, plus every task in the
// corpus and its escrow bucket.
//
// We do not iterate the ledger's full set of agents because the harness
// has full visibility into who could possibly hold a balance, and this
// avoids any non-determinism from map iteration over the ledger's
// internal storage.
type SnapshotInputs struct {
	Agents []crypto.AgentID
	Tasks  []string
}

// Snapshot captures node's projected state into a deterministic
// LedgerSnapshot suitable for cross-node comparison.
func Snapshot(node *Node, inputs SnapshotInputs) LedgerSnapshot {
	snap := LedgerSnapshot{NodeIndex: node.Index}

	// Balances. Sort by AgentID to remove map-iteration order.
	agents := make([]string, 0, len(inputs.Agents))
	for _, a := range inputs.Agents {
		agents = append(agents, string(a))
	}
	sort.Strings(agents)
	snap.Balances = make([]AgentBalance, 0, len(agents))
	for _, a := range agents {
		bal, _ := node.Ledger.Balance(crypto.AgentID(a))
		snap.Balances = append(snap.Balances, AgentBalance{
			AgentID: a,
			Amount:  bal,
		})
		snap.TotalSupply += bal
	}

	// Escrow buckets and task statuses. Sort by TaskID.
	taskIDs := append([]string(nil), inputs.Tasks...)
	sort.Strings(taskIDs)
	snap.Escrows = make([]EscrowBucketSnapshot, 0, len(taskIDs))
	snap.TaskStatus = make([]TaskStatusSnapshot, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		bucket := crypto.AgentID("escrow:" + taskID)
		bucketBal, _ := node.Ledger.Balance(bucket)
		snap.Escrows = append(snap.Escrows, EscrowBucketSnapshot{
			TaskID:            taskID,
			Balance:           bucketBal,
			SettlementStarted: node.Escrow.HasSettlementStarted(taskID),
		})
		// Add the bucket balance to total supply too — a divergent
		// settlement could leave funds stranded in the bucket.
		snap.TotalSupply += bucketBal

		status := "missing"
		if task, err := node.TaskManager.Get(taskID); err == nil {
			status = string(task.Status)
		}
		snap.TaskStatus = append(snap.TaskStatus, TaskStatusSnapshot{
			TaskID: taskID,
			Status: status,
		})
	}

	return snap
}

// SnapshotCluster captures every node's snapshot in index order.
func SnapshotCluster(c *Cluster, inputs SnapshotInputs) []LedgerSnapshot {
	out := make([]LedgerSnapshot, len(c.Nodes))
	for i, node := range c.Nodes {
		out[i] = Snapshot(node, inputs)
	}
	return out
}

// AssertByteEquality fails the test if the snapshots are not byte-equal
// on their canonical JSON form (NodeIndex excluded so the comparison is
// purely on projected state). On mismatch, the failure message includes
// a per-node diff highlighting which agents and tasks differ — the
// signal F4B's fix needs to make zero.
func AssertByteEquality(t *testing.T, snaps []LedgerSnapshot) {
	t.Helper()
	if len(snaps) < 2 {
		t.Fatalf("cross_node: need at least 2 snapshots to compare (got %d)", len(snaps))
	}

	// Canonicalize each snapshot for comparison: use the
	// projected-state subset (everything except NodeIndex).
	canonical := make([][]byte, len(snaps))
	for i, s := range snaps {
		body := projectedStateOnly(s)
		bytes, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("cross_node: marshal snapshot %d: %v", i, err)
		}
		canonical[i] = bytes
	}

	mismatches := []int{}
	for i := 1; i < len(canonical); i++ {
		if string(canonical[i]) != string(canonical[0]) {
			mismatches = append(mismatches, i)
		}
	}
	if len(mismatches) == 0 {
		return
	}

	// Build a structured diff for the failure message.
	t.Fatalf("cross_node: ledger byte-equality failed — node 0 vs nodes %v differ\n%s",
		mismatches, formatDivergenceDiff(snaps))
}

// projectedStateOnly returns the LedgerSnapshot fields that participate
// in cross-node equality, excluding NodeIndex.
func projectedStateOnly(s LedgerSnapshot) map[string]interface{} {
	return map[string]interface{}{
		"balances":     s.Balances,
		"escrows":      s.Escrows,
		"task_status":  s.TaskStatus,
		"total_supply": s.TotalSupply,
	}
}

// formatDivergenceDiff renders a human-readable diff of cross-node
// snapshot divergence. The format is the per-node-diff format invariant
// A-1 calls for; it surfaces:
//
//   - Per-agent balance differences (agent → [n0, n1, ..., nN]).
//   - Per-task settlement-status differences (task → [n0, n1, ..., nN]).
//   - Per-escrow-bucket residual differences.
//   - Total-supply per node (a non-conservation symptom).
//
// Output is committed alongside the harness as a baseline fixture
// (testdata/tied_weight_divergence_baseline.txt) for the F4B fix to
// regress against.
func formatDivergenceDiff(snaps []LedgerSnapshot) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Cross-node divergence summary (%d nodes):\n", len(snaps))

	// Per-node total supply.
	fmt.Fprintf(&sb, "\n=== Per-node total supply ===\n")
	for _, s := range snaps {
		fmt.Fprintf(&sb, "  node[%d] total_supply = %d\n", s.NodeIndex, s.TotalSupply)
	}

	// Per-agent balance divergences.
	fmt.Fprintf(&sb, "\n=== Per-agent balance divergences ===\n")
	agentIDs := uniqueAgents(snaps)
	any := false
	for _, agent := range agentIDs {
		row := make([]uint64, len(snaps))
		for i, s := range snaps {
			row[i] = lookupBalance(s, agent)
		}
		if !allEqual(row) {
			any = true
			fmt.Fprintf(&sb, "  %-66s  %v\n", agent, row)
		}
	}
	if !any {
		fmt.Fprintf(&sb, "  (none — balances byte-equal)\n")
	}

	// Per-task status divergences.
	fmt.Fprintf(&sb, "\n=== Per-task status divergences ===\n")
	taskIDs := uniqueTasks(snaps)
	any = false
	for _, taskID := range taskIDs {
		row := make([]string, len(snaps))
		for i, s := range snaps {
			row[i] = lookupTaskStatus(s, taskID)
		}
		if !allEqualStr(row) {
			any = true
			fmt.Fprintf(&sb, "  %-30s  %v\n", taskID, row)
		}
	}
	if !any {
		fmt.Fprintf(&sb, "  (none — task statuses byte-equal)\n")
	}

	// Per-escrow-bucket residual divergences.
	fmt.Fprintf(&sb, "\n=== Per-escrow-bucket residual divergences ===\n")
	any = false
	for _, taskID := range taskIDs {
		row := make([]uint64, len(snaps))
		for i, s := range snaps {
			row[i] = lookupEscrowBalance(s, taskID)
		}
		if !allEqual(row) {
			any = true
			fmt.Fprintf(&sb, "  escrow:%-23s  %v\n", taskID, row)
		}
	}
	if !any {
		fmt.Fprintf(&sb, "  (none — escrow residuals byte-equal)\n")
	}

	return sb.String()
}

func uniqueAgents(snaps []LedgerSnapshot) []string {
	seen := make(map[string]struct{})
	for _, s := range snaps {
		for _, b := range s.Balances {
			seen[b.AgentID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func uniqueTasks(snaps []LedgerSnapshot) []string {
	seen := make(map[string]struct{})
	for _, s := range snaps {
		for _, ts := range s.TaskStatus {
			seen[ts.TaskID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func lookupBalance(s LedgerSnapshot, agent string) uint64 {
	for _, b := range s.Balances {
		if b.AgentID == agent {
			return b.Amount
		}
	}
	return 0
}

func lookupTaskStatus(s LedgerSnapshot, taskID string) string {
	for _, ts := range s.TaskStatus {
		if ts.TaskID == taskID {
			return ts.Status
		}
	}
	return "missing"
}

func lookupEscrowBalance(s LedgerSnapshot, taskID string) uint64 {
	for _, e := range s.Escrows {
		if e.TaskID == taskID {
			return e.Balance
		}
	}
	return 0
}

func allEqual(row []uint64) bool {
	for i := 1; i < len(row); i++ {
		if row[i] != row[0] {
			return false
		}
	}
	return true
}

func allEqualStr(row []string) bool {
	for i := 1; i < len(row); i++ {
		if row[i] != row[0] {
			return false
		}
	}
	return true
}

// snapshotInputsFor builds the SnapshotInputs covering the cluster's
// well-known agents (treasury, poster, worker, every validator) plus a
// list of task IDs from a corpus.
func snapshotInputsFor(c *Cluster, scenarios []*TaskScenario) SnapshotInputs {
	agents := []crypto.AgentID{TreasuryID, PosterID, WorkerID}
	agents = append(agents, c.Validators...)

	taskIDs := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		taskIDs = append(taskIDs, s.TaskID)
	}
	return SnapshotInputs{Agents: agents, Tasks: taskIDs}
}
