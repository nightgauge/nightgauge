package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// With a live daemon `autonomous status` returned before asking the run
// registry, so the reload-safety answer existed only when no daemon answered:
// the one case with no window to reload (#2720). These drive a real socket
// daemon and write no state file, so the state-file branch cannot pass them.

// startStatusDaemon serves workspace with a scheduler that has dispatched
// nothing: any run in the answer came from the registry, as a manual run does.
func startStatusDaemon(t *testing.T, workspace string) {
	t.Helper()
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), workspace)
	srv := ipc.NewServer(nil, ipc.WithAutonomousScheduler(as), ipc.WithWorkspaceRoot(workspace))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sock, err := ipc.DaemonSocketPath(workspace)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	ln, err := srv.BindSocket(sock)
	if err != nil {
		t.Fatalf("bind the test daemon's socket: %v", err)
	}
	go func() { _ = srv.ServeSocket(ctx, ln, sock) }()
}

func liveStatusJSON(t *testing.T, workspace string) map[string]any {
	t.Helper()
	t.Chdir(workspace)
	cmd := autonomousStatusCmd()
	cmd.SetArgs([]string{"--json"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("autonomous status --json: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	return got
}

func TestAutonomousStatusLiveJSONListsAManualRun(t *testing.T) {
	workspace := daemonWorkspace(t)
	startStatusDaemon(t, workspace)
	seedRun(t, workspace, 1429)

	got := liveStatusJSON(t, workspace)

	if got["daemon"] != true {
		t.Fatalf("answered from the state-file branch: %v", got)
	}
	for _, key := range []string{"status", "cycles", "running", "inReviewWithoutPR"} {
		if _, ok := got[key]; !ok {
			t.Errorf("existing key %q is gone: %v", key, got)
		}
	}
	if got["running_pipelines_known"] != true || got["reload_safe"] != false {
		t.Errorf("known=%v reload_safe=%v, want true, false",
			got["running_pipelines_known"], got["reload_safe"])
	}
	runs, _ := got["running_pipelines"].([]any)
	if len(runs) != 1 {
		t.Fatalf("running_pipelines = %v, want the one manual run", got["running_pipelines"])
	}
	if run, _ := runs[0].(map[string]any); run["issueNumber"] != float64(1429) {
		t.Errorf("run = %v, want #1429", run)
	}
}

func TestAutonomousStatusLiveJSONWithNoRunsIsReloadSafe(t *testing.T) {
	workspace := daemonWorkspace(t)
	startStatusDaemon(t, workspace)

	got := liveStatusJSON(t, workspace)

	if got["running_pipelines_known"] != true || got["reload_safe"] != true {
		t.Errorf("known=%v reload_safe=%v, want true, true",
			got["running_pipelines_known"], got["reload_safe"])
	}
	if runs, ok := got["running_pipelines"].([]any); !ok || len(runs) != 0 {
		t.Errorf("running_pipelines = %#v, want an empty list, never null", got["running_pipelines"])
	}
}

func TestAutonomousStatusLiveHumanNamesAManualRun(t *testing.T) {
	workspace := daemonWorkspace(t)
	startStatusDaemon(t, workspace)
	seedRun(t, workspace, 1429)

	out := runAutonomousStatus(t, workspace)

	if !strings.Contains(out, "Source: live daemon") {
		t.Fatalf("answered from the state-file branch:\n%s", out)
	}
	if !strings.Contains(out, "#1429 platform") || !strings.Contains(out, "reload is not safe yet") {
		t.Errorf("status does not name the manual run:\n%s", out)
	}
}

func TestAutonomousStatusLiveHumanSaysSafeWithNoRuns(t *testing.T) {
	workspace := daemonWorkspace(t)
	startStatusDaemon(t, workspace)

	out := runAutonomousStatus(t, workspace)

	if !strings.Contains(out, "0 running; safe to reload") {
		t.Errorf("status does not say a reload is safe:\n%s", out)
	}
}

// A real server always answers the summary, so the failed-summary arm is pinned
// at the functions that render it.
func TestLiveStatusWithUnansweredSummaryIsNeverSafe(t *testing.T) {
	line := liveAutonomousModeLine("stopped", ipc.RunningPipelinesResult{}, false)
	if line != "Autonomous Mode: Stopped — reload safety unknown" {
		t.Errorf("line = %q", line)
	}

	got := liveAutonomousStatusJSON(orchestrator.AutonomousState{}, ipc.RunningPipelinesResult{}, false)
	if got["running_pipelines_known"] != false || got["reload_safe"] != false {
		t.Errorf("known=%v reload_safe=%v, want false, false",
			got["running_pipelines_known"], got["reload_safe"])
	}
}
