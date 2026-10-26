package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// `nightgauge autonomous clear-cooldown` is the headless way out of the global
// quota dispatch cooldown (#2551). The cooldown was reachable only through the
// IPC method and a VS Code command, so a false cooldown suppressed every
// dispatch on a headless host until its deadline.

const testCooldownUntil = "2099-01-01T00:00:00Z"

func runAutonomousClearCooldown(t *testing.T, root string, args ...string) string {
	t.Helper()
	t.Chdir(root)
	cmd := autonomousClearCooldownCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("autonomous clear-cooldown %v: %v (output %q)", args, err, out.String())
	}
	return out.String()
}

func TestAutonomousClearCooldownOffline(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status:              "stopped",
		QuotaCooldownUntil:  testCooldownUntil,
		QuotaCooldownReason: "rate-limit-quota-exhausted",
	})

	out := runAutonomousClearCooldown(t, root)

	st := readAutonomousState(t, root)
	if st.QuotaCooldownUntil != "" || st.QuotaCooldownReason != "" {
		t.Errorf("cooldown survived the clear: until=%q reason=%q", st.QuotaCooldownUntil, st.QuotaCooldownReason)
	}
	if !strings.Contains(out, "Cleared") || !strings.Contains(out, testCooldownUntil) || !strings.Contains(out, "state file") {
		t.Errorf("output %q does not report the cleared deadline and where it was cleared", out)
	}
}

func TestAutonomousClearCooldownNoActiveCooldown(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "stopped"})

	out := runAutonomousClearCooldown(t, root)

	if !strings.Contains(out, "nothing to clear") {
		t.Errorf("output %q does not say there was nothing to clear", out)
	}
}

func TestAutonomousClearCooldownWithNoStateFile(t *testing.T) {
	root := layouttest.Repo(t)

	out := runAutonomousClearCooldown(t, root)

	if !strings.Contains(out, "No autonomous scheduler state found") {
		t.Errorf("output %q does not say there is no state", out)
	}
}

func TestAutonomousClearCooldownRoundTripsThroughTheDaemon(t *testing.T) {
	// shortTempDir: a unix socket path is capped at ~104 bytes.
	root := shortTempDir(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status:              "running",
		QuotaCooldownUntil:  testCooldownUntil,
		QuotaCooldownReason: "rate-limit-quota-exhausted",
	})
	startTestDaemon(t, root)

	out := runAutonomousClearCooldown(t, root)

	if !strings.Contains(out, "running scheduler") || !strings.Contains(out, testCooldownUntil) {
		t.Errorf("output %q does not report that the LIVE scheduler was cleared", out)
	}
	if st := readAutonomousState(t, root); st.QuotaCooldownUntil != "" {
		t.Errorf("the scheduler persisted a state that still carries the cooldown %q", st.QuotaCooldownUntil)
	}
}

func TestOrchestratorClearQuotaCooldownOfflineReportsPrevious(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{QuotaCooldownUntil: testCooldownUntil})

	cleared, prev, found, err := orchestrator.ClearQuotaCooldownOffline(root)
	if err != nil || !cleared || !found || prev != testCooldownUntil {
		t.Fatalf("ClearQuotaCooldownOffline = (%v, %q, %v, %v), want (true, %q, true, nil)", cleared, prev, found, err, testCooldownUntil)
	}
}
