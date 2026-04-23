package main

// Tests for the aet admin subcommand. Verifies argument parsing and the
// fatal-on-missing-reason contract without actually hitting a testnet
// endpoint (the CLI's network path shares signedPost with every other
// subcommand; that surface is covered by client_test.go).

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestAdminActivateIntegerMigration_MissingReason confirms the CLI fails
// with a clear message when --reason is absent. The guard is the last line
// of defense against a silently-successful activation with an empty
// reason field in the DAG event.
func TestAdminActivateIntegerMigration_MissingReason(t *testing.T) {
	if os.Getenv("AET_CLI_RECURSIVE_TEST") == "1" {
		// Child process: invoke the subcommand without --reason and expect fatal.
		runAdminActivateIntegerMigration([]string{})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAdminActivateIntegerMigration_MissingReason")
	cmd.Env = append(os.Environ(), "AET_CLI_RECURSIVE_TEST=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; stdout/stderr:\n%s", out)
	}
	if !strings.Contains(string(out), "--reason is required") {
		t.Fatalf("expected '--reason is required' in output; got:\n%s", out)
	}
}
