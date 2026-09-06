package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// The lifetime failure cap is enforced at dispatch and logged exactly once, at
// the moment the quarantine is applied (#1487). An operator asking hours later
// why an issue is not moving had no surface to read — `autonomous status`
// showed the fleet as idle with no work. These pin the counters and the
// quarantine onto the status output.

func runAutonomousStatus(t *testing.T, root string) string {
	t.Helper()
	t.Chdir(root)
	cmd := autonomousStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)
	// The command prints with fmt.Printf, not cmd.OutOrStdout, so capture the
	// process stdout rather than the cobra buffer.
	return captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("autonomous status: %v", err)
		}
	})
}

func TestAutonomousStatusShowsLifetimeCountsAndQuarantine(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status: "running",
		LifetimeIssueFailures: map[string]int{
			"octocat/acme-site#69": 2,
			"octocat/acme-app#530": 1,
		},
		QuarantinedIssues: map[string]bool{"octocat/acme-site#69": true},
	})

	out := runAutonomousStatus(t, root)

	if !strings.Contains(out, "octocat/acme-site#69") || !strings.Contains(out, "2/2") {
		t.Errorf("status does not show the quarantined issue at its cap:\n%s", out)
	}
	if !strings.Contains(out, "QUARANTINED") {
		t.Errorf("status does not name the quarantine:\n%s", out)
	}
	if !strings.Contains(out, "clear-failures") {
		t.Errorf("status names the quarantine but not the way out of it:\n%s", out)
	}
	// The under-cap issue is the one an operator can still act on before it
	// locks out, so it is shown too — not only the ones already quarantined.
	if !strings.Contains(out, "octocat/acme-app#530") || !strings.Contains(out, "1/2") {
		t.Errorf("status hides an issue that is one failure from quarantine:\n%s", out)
	}
}

func TestAutonomousStatusOmitsTheSectionWithNoFailures(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "running"})

	if out := runAutonomousStatus(t, root); strings.Contains(out, "Lifetime failures") {
		t.Errorf("status prints an empty lifetime-failure section:\n%s", out)
	}
}
