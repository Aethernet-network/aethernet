package main

// Admin-only subcommands. These hit endpoints registered only when the node
// is started with --enable-admin-api; on production deployments without the
// flag they return 404 and are inert. Testnet rehearsal surface for Part F
// of the canonical-distribution-integer-migration workstream.

import (
	"flag"
	"fmt"
	"strconv"
)

// runAdminActivateIntegerMigration emits a canonical
// EventTypeIntegerMigrationActivation event via the node's admin API. The
// request is signed by the active wallet; the node-side handler copies the
// verified signer's AgentID into the event payload's EmittingAgent field.
//
// After consensus, every node's IntegerMigrationActivationConsumer picks up
// the event, persists the activation state, and flips the settler and
// generation-ledger calculator out of shadow mode. Subsequent settlements
// are integer-canonical.
//
// Activation is one-way and idempotent. A second invocation produces a
// second event, which the consumers short-circuit at their early-idempotency
// pre-check.
func runAdminActivateIntegerMigration(args []string) {
	fs := flag.NewFlagSet("admin activate-integer-migration", flag.ExitOnError)
	url := fs.String("url", defaultURL(), "API URL")
	jsonOut := fs.Bool("json", false, "JSON output")
	reason := fs.String("reason", "", "Activation reason (required, free-form string)")
	_ = fs.Parse(args)

	if *reason == "" {
		fatal("--reason is required")
	}

	wf, err := getActiveWallet()
	if err != nil {
		fatal("no active wallet (run: aet wallet create)")
	}
	pk, err := unlockWallet(wf)
	if err != nil {
		fatal("unlock wallet: %v", err)
	}

	var result map[string]any
	if err := signedPost(*url, "/v1/admin/integer-migration/activate", map[string]any{
		"reason": *reason,
	}, wf.AgentID, pk, &result); err != nil {
		fatal("activate: %v", err)
	}

	if *jsonOut {
		printJSON(result)
		return
	}

	evID, _ := result["event_id"].(string)
	emitter, _ := result["emitting_agent"].(string)
	emittedAt, _ := result["emitted_at_unix"].(float64)
	activationReason, _ := result["activation_reason"].(string)

	printHeader("Integer Migration Activation")
	printRow("Event ID", evID)
	printRow("Emitting Agent", truncateID(emitter, 24))
	printRow("Emitted (unix)", strconv.FormatInt(int64(emittedAt), 10))
	printRow("Reason", activationReason)
	fmt.Println("\n  Activation event submitted to DAG — settles through consensus (~15s).")
	fmt.Println("  Every node's migration consumer flips settlement out of shadow mode on apply.")
}
