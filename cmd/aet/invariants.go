package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/monitoring/cross_node_invariants"
)

// runInvariants dispatches `aet invariants <subcommand>`.
func runInvariants(args []string) {
	if len(args) == 0 {
		fatal("usage: aet invariants <check>")
	}
	switch args[0] {
	case "check":
		runInvariantsCheck(args[1:])
	default:
		fatal("unknown invariants subcommand: %s", args[0])
	}
}

// runInvariantsCheck implements `aet invariants check`.
//
// Behavior:
//
//   - Fetches the local ledger snapshot from --url's
//     /v1/admin/ledger-snapshot endpoint.
//   - Fetches each --peer's snapshot via the same endpoint.
//   - Compares and prints per-peer divergence; exits 0 if no divergence,
//     exit code 2 if any divergence detected (CI-friendly).
//
// Endpoint requirement: every node MUST be started with --enable-admin-api
// so /v1/admin/ledger-snapshot is served. Endpoint implementation lives at
// internal/api/admin_handlers.go (handleAdminLedgerSnapshot, shipped in
// F5 5B Phase 1.3 prep for testnet criterion 12).
func runInvariantsCheck(args []string) {
	fs := flag.NewFlagSet("invariants check", flag.ExitOnError)
	url := fs.String("url", defaultURL(), "Local node URL")
	peersCSV := fs.String("peers", "", "Comma-separated peer addresses (host:port or full URL); required")
	thresholdMicroAET := fs.Uint64("threshold", 0, "µAET divergence above which the check exits with code 2 (default 0: any nonzero divergence)")
	timeout := fs.Duration("timeout", 5*time.Second, "Per-request timeout for snapshot fetches")
	jsonOut := fs.Bool("json", false, "Emit the report as JSON")
	_ = fs.Parse(args)

	if strings.TrimSpace(*peersCSV) == "" {
		fatal("--peers is required (comma-separated host:port or URL list)")
	}
	peerAddrs := splitAndTrim(*peersCSV)

	fetcher := &cross_node_invariants.HTTPFetcher{
		Client:     &http.Client{Timeout: *timeout},
		PeerScheme: schemeFromURL(*url),
	}

	// Local snapshot fetched via the same path on --url.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Second)
	defer cancel()
	local, err := fetcher.Fetch(ctx, *url)
	if err != nil {
		fatal("fetch local snapshot from %s: %v", *url, err)
	}

	peers := &cross_node_invariants.StaticPeerSource{Addrs: peerAddrs}
	monitor := cross_node_invariants.NewMonitor(peers, fetcher, *thresholdMicroAET, nil)

	report, err := monitor.Check(ctx, local)
	if err != nil {
		fatal("check: %v", err)
	}

	if *jsonOut {
		printJSON(report)
	} else {
		fmt.Print(report.Format())
	}

	if report.TotalMagnitude > *thresholdMicroAET {
		os.Exit(2)
	}
}

// schemeFromURL extracts "http://" or "https://" from a URL prefix,
// defaulting to "http://" if neither is present.
func schemeFromURL(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"):
		return "https://"
	case strings.HasPrefix(u, "http://"):
		return "http://"
	default:
		return "http://"
	}
}

// splitAndTrim splits a comma-separated list and trims whitespace.
// Empty entries are dropped.
func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
