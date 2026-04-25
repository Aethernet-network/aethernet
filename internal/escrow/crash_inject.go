package escrow

import (
	"log/slog"
	"os"
	"strconv"
)

// crashFlagEnvVar is the testnet-only feature flag that, when set, causes
// ApplySettlementRecords to deterministically crash the process after a
// specified number of records have been fully applied. Used by F5 5B
// testnet criterion 11a (crash-mid-apply self-heal verification per
// Plan v3 §3.3 crash-position table).
//
// **PRODUCTION DEPLOYMENTS MUST NOT SET THIS VARIABLE.** When set to a
// non-empty value during process startup, it is logged as an explicit
// "TESTNET-ONLY" warning so any operator who accidentally enables it in
// a production environment sees the warning at boot.
const crashFlagEnvVar = "AETHERNET_CRASH_AFTER_NTH_RECORD"

// crashAfterNthRecord is the process-exit hook fired when the env-flag
// triggers a crash. Defaults to os.Exit(1); tests override to capture
// the trigger position without terminating the test process.
var crashAfterNthRecord = func(index int, taskID, recordCID string) {
	slog.Error("CRASH INJECTED — testnet-only feature flag (AETHERNET_CRASH_AFTER_NTH_RECORD)",
		"task_id", taskID,
		"record_canonical_id", recordCID,
		"record_index", index,
		"reason", "intentional crash for criterion 11a self-heal verification",
	)
	os.Exit(1)
}

// maybeCrashAfterRecord checks the AETHERNET_CRASH_AFTER_NTH_RECORD
// env-flag at the top of each ApplySettlementRecords loop iteration.
// When the flag is set to integer N AND the current record index ==
// N, crashAfterNthRecord fires.
//
// Semantic: with flag=N, records 0..N-1 are fully applied (ledger
// write + paid-flag projection persist) before the crash; record N is
// untouched. This is "crash position 1" of the Plan v3 §3.3 crash-
// position table — between records, after a clean per-record
// commit-and-persist cycle.
//
// Restart self-heal validation: on retry, records 0..N-1 fast-path skip
// via paid-flag projection; records N..end re-derive identical
// PayoutRecords (D-1) and apply via the ledger's ErrDuplicateEntry
// idempotency.
func maybeCrashAfterRecord(index int, taskID, recordCID string) {
	val := os.Getenv(crashFlagEnvVar)
	if val == "" {
		return
	}
	target, err := strconv.Atoi(val)
	if err != nil || target != index {
		return
	}
	crashAfterNthRecord(index, taskID, recordCID)
}

// LogCrashFlagAtStartup emits a TESTNET-ONLY warning if the env-flag is
// set when the process starts. Wired into cmd/node/main.go boot so any
// production deployment that accidentally inherits the flag in its
// environment sees a loud warning before serving traffic.
func LogCrashFlagAtStartup() {
	val := os.Getenv(crashFlagEnvVar)
	if val == "" {
		return
	}
	slog.Warn("TESTNET-ONLY crash-injection flag detected at startup — production deployments MUST NOT set this",
		"flag", crashFlagEnvVar,
		"value", val,
		"behavior", "ApplySettlementRecords will os.Exit(1) after applying N records (0-indexed)",
	)
}
