package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// `nightgauge autonomous clear-failures` is the only CLI way out of the
// per-issue lifetime failure cap (#1487). Before it existed, ClearIssueFailures
// was reachable only from the IPC method and a VS Code command, so three issues
// quarantined at 2/2 by a gate defect that had already been fixed could not be
// released from a headless host without editing state.json.
//
// Both halves are covered here: the offline path that rewrites the state file,
// and the daemon path that round-trips through the IPC method on a live
// scheduler (startTestDaemon, shared with attention_test.go).

func runAutonomousClearFailures(t *testing.T, root string, args ...string) string {
	t.Helper()
	t.Chdir(root)
	cmd := autonomousClearFailuresCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("autonomous clear-failures %v: %v (output %q)", args, err, out.String())
	}
	return out.String()
}

func TestAutonomousClearFailuresClearsOneIssueOffline(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status: "stopped",
		LifetimeIssueFailures: map[string]int{
			"octocat/acme-site#69": 2,
			"octocat/acme-app#530": 2,
		},
		QuarantinedIssues: map[string]bool{"octocat/acme-site#69": true},
	})

	out := runAutonomousClearFailures(t, root, "octocat/acme-site#69")

	st := readAutonomousState(t, root)
	if _, still := st.LifetimeIssueFailures["octocat/acme-site#69"]; still {
		t.Error("the cleared issue still carries a lifetime failure counter")
	}
	if st.QuarantinedIssues["octocat/acme-site#69"] {
		t.Error("the quarantine record survived the clear")
	}
	if got := st.LifetimeIssueFailures["octocat/acme-app#530"]; got != 2 {
		t.Errorf("the untargeted issue's counter = %d, want 2 — clearing one issue must not clear the fleet", got)
	}
	if !strings.Contains(out, "Cleared") || !strings.Contains(out, "autonomous run") {
		t.Errorf("output %q does not say what happened and what to do next", out)
	}
}

func TestAutonomousClearFailuresAllOffline(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status: "stopped",
		LifetimeIssueFailures: map[string]int{
			"octocat/acme-site#69": 2,
			"octocat/acme-app#530": 2,
			"octocat/acme-web#779": 2,
		},
		QuarantinedIssues: map[string]bool{"octocat/acme-site#69": true},
	})

	out := runAutonomousClearFailures(t, root, "--all")

	st := readAutonomousState(t, root)
	if len(st.LifetimeIssueFailures) != 0 {
		t.Errorf("LifetimeIssueFailures = %v, want empty", st.LifetimeIssueFailures)
	}
	if len(st.QuarantinedIssues) != 0 {
		t.Errorf("QuarantinedIssues = %v, want empty", st.QuarantinedIssues)
	}
	if !strings.Contains(out, "3 issue(s)") {
		t.Errorf("output %q does not report how many issues were cleared", out)
	}
}

func TestAutonomousClearFailuresRoundTripsThroughTheDaemon(t *testing.T) {
	// shortTempDir, not t.TempDir: a unix socket path is capped at ~104 bytes
	// and the test-name-derived temp dir overflows it.
	root := shortTempDir(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status:                "running",
		LifetimeIssueFailures: map[string]int{"octocat/acme-site#69": 2},
		QuarantinedIssues:     map[string]bool{"octocat/acme-site#69": true},
	})
	// The scheduler loads that state at construction, so the daemon this
	// starts is holding the quarantined counter in memory — which is the whole
	// reason the daemon path exists. Clearing the file underneath a live
	// scheduler would be overwritten by its next persist.
	startTestDaemon(t, root)

	out := runAutonomousClearFailures(t, root, "octocat/acme-site#69")

	if !strings.Contains(out, "running scheduler") {
		t.Errorf("output %q does not report that the LIVE scheduler was cleared", out)
	}
	st := readAutonomousState(t, root)
	if _, still := st.LifetimeIssueFailures["octocat/acme-site#69"]; still {
		t.Error("the scheduler persisted a state that still carries the cleared counter")
	}
	if st.QuarantinedIssues["octocat/acme-site#69"] {
		t.Error("the quarantine record survived the daemon-path clear")
	}
}

func TestAutonomousClearFailuresRejectsAMalformedKey(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "stopped"})
	t.Chdir(root)

	for _, arg := range []string{"1487", "#1487", "nightgauge#1487", "nightgauge/nightgauge"} {
		cmd := autonomousClearFailuresCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{arg})
		err := cmd.Execute()
		if err == nil {
			t.Errorf("%q was accepted as an issue key — a key that matches no entry clears nothing and reports 0, which reads exactly like an issue that was already clean", arg)
			continue
		}
		if !strings.Contains(err.Error(), "owner/repo#number") {
			t.Errorf("error for %q does not name the expected shape: %v", arg, err)
		}
	}
}

func TestAutonomousClearFailuresNeedsATarget(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cmd := autonomousClearFailuresCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Error("a bare `clear-failures` cleared something — it must name an issue or pass --all")
	}
}

func TestAutonomousClearFailuresWithNoStateFile(t *testing.T) {
	out := runAutonomousClearFailures(t, t.TempDir(), "--all")
	if !strings.Contains(out, "No autonomous scheduler state") {
		t.Errorf("output %q, want the no-state notice", out)
	}
}
