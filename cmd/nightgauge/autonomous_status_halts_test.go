package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// A repository halt stops dispatch for that repo while the scheduler still
// reads "running", so status must say so (#2748).

func haltedState() orchestrator.AutonomousState {
	return orchestrator.AutonomousState{
		Status: "running",
		PausedRepos: map[string]*orchestrator.RepoPauseRecord{
			"octocat/acme-app": {Repo: "octocat/acme-app", Issue: 12, Stage: "feature-validate",
				Reason: "terminal stage failure", PausedAt: "2026-11-05T10:00:00Z"},
		},
		Failed: []orchestrator.FailedItem{
			{Repo: "octocat/acme-app", Number: 44, Title: "rework the auth layer",
				FailedAt: "2026-11-05T09:00:00Z", Kind: orchestrator.TerminalKindArchitectureApprovalRequired},
		},
	}
}

func statusJSON(t *testing.T, root string) map[string]json.RawMessage {
	t.Helper()
	t.Chdir(root)
	cmd := autonomousStatusCmd()
	cmd.SetArgs([]string{"--json"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("autonomous status --json: %v", err)
		}
	})
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	return got
}

func TestHaltedSuffix(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: " (1 repository halted)", 3: " (3 repositories halted)"} {
		if got := haltedSuffix(n); got != want {
			t.Errorf("haltedSuffix(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAutonomousStatusNoHaltsPrintsNoSuffixOrSection(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "running"})
	out := runAutonomousStatus(t, root)
	if strings.Contains(out, "halted") || strings.Contains(out, "architecture approval") {
		t.Errorf("status reports halts that do not exist:\n%s", out)
	}
	got := statusJSON(t, root)
	for _, k := range []string{"haltedRepos", "architectureApprovalParked"} {
		if string(got[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, got[k])
		}
	}
}

func TestAutonomousStatusStateFileShowsHaltAndParked(t *testing.T) {
	root := layouttest.Repo(t)
	st := haltedState()
	st.Status = "stopped" // "running" with no live pid reads as Stalled
	writeAutonomousState(t, root, st)
	out := runAutonomousStatus(t, root)
	for _, want := range []string{
		"Autonomous Mode: Stopped (1 repository halted)",
		"Halted repositories (1) (from state file)",
		"octocat/acme-app  #12 at feature-validate — terminal stage failure (halted 2026-11-05T10:00:00Z",
		"Awaiting architecture approval (1) (from state file)",
		"octocat/acme-app#44  rework the auth layer (parked 2026-11-05T09:00:00Z)",
		"nightgauge attention list",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	got := statusJSON(t, root)
	var halts []orchestrator.RepoPauseRecord
	var parked []orchestrator.FailedItem
	_ = json.Unmarshal(got["haltedRepos"], &halts)
	_ = json.Unmarshal(got["architectureApprovalParked"], &parked)
	if len(halts) != 1 || halts[0].Issue != 12 || halts[0].Stage != "feature-validate" {
		t.Errorf("haltedRepos = %s", got["haltedRepos"])
	}
	if len(parked) != 1 || parked[0].Number != 44 {
		t.Errorf("architectureApprovalParked = %s", got["architectureApprovalParked"])
	}
	if string(got["status"]) != `"stopped"` {
		t.Errorf("raw status changed: %s", got["status"])
	}
}

func TestAutonomousStatusNoStateFileSaysHaltStateUnknown(t *testing.T) {
	root := layouttest.Repo(t)
	if out := runAutonomousStatus(t, root); !strings.Contains(out, "halt state is unknown") {
		t.Errorf("status with no state file claims nothing about halts:\n%s", out)
	}
}

func TestAutonomousStatusLiveShowsRepoHalt(t *testing.T) {
	workspace := daemonWorkspace(t)
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), workspace)
	if !as.PauseRepo("octocat/acme-app", "terminal stage failure", "slot-failure", 12, "feature-validate") {
		t.Fatal("PauseRepo did not record the halt")
	}
	srv := ipc.NewServer(nil, ipc.WithAutonomousScheduler(as), ipc.WithWorkspaceRoot(workspace))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sock, err := ipc.DaemonSocketPath(workspace)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	ln, err := srv.BindSocket(sock)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	go func() { _ = srv.ServeSocket(ctx, ln, sock) }()

	got := liveStatusJSON(t, workspace)
	if got["daemon"] != true {
		t.Fatalf("answered from the state-file branch: %v", got)
	}
	halts, _ := got["haltedRepos"].([]any)
	if len(halts) != 1 {
		t.Fatalf("haltedRepos = %v, want the one halt", got["haltedRepos"])
	}
	if parked, ok := got["architectureApprovalParked"].([]any); !ok || len(parked) != 0 {
		t.Errorf("architectureApprovalParked = %v, want []", got["architectureApprovalParked"])
	}

	out := runAutonomousStatus(t, workspace)
	if !strings.Contains(out, "(1 repository halted) — ") ||
		!strings.Contains(out, "Halted repositories (1) — ") ||
		strings.Contains(out, "from state file") {
		t.Errorf("live status does not report the halt as live:\n%s", out)
	}
}
