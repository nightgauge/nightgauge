package main

// `nightgauge hook stop-verify --emit-event` is the second writer of an
// OpenCode run's events file (#1810). It exists because opencode 1.18.30
// neither awaits a plugin `event` hook's promise nor stays alive past
// publishing session.idle, so the verdict has to be recorded by the process
// that computes it rather than by the plugin that asked for it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// emitEventRun sets the two run variables the verb reads, runs it in this
// process, and returns the run's events file path.
func emitEventRun(t *testing.T, workdir, sessionID string, child bool) string {
	t.Helper()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-emit-1"
	t.Setenv(opencodeplugin.RunOutputFileEnvVar, outputFile)
	t.Setenv(opencodeplugin.RunIDEnvVar, runID)

	if err := emitStopVerifyEvent(workdir, sessionID, child); err != nil {
		t.Fatalf("emitStopVerifyEvent: %v", err)
	}
	path, ok := opencodeplugin.EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused an absolute output file")
	}
	return path
}

func onlyStopVerify(t *testing.T, path string) opencodeplugin.Event {
	t.Helper()
	events, err := opencodeplugin.ReadRunEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []opencodeplugin.Event
	for _, e := range events {
		if e.Kind == "stop_verify" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d stop_verify events, want exactly 1: %+v", len(found), events)
	}
	return found[0]
}

// TestHookStopVerifyEmitEventRecordsComplete: no plan file means
// EvaluateStop's own OK path, which the events file records as "complete".
func TestHookStopVerifyEmitEventRecordsComplete(t *testing.T) {
	path := emitEventRun(t, t.TempDir(), "ses_emit_1", false)
	ev := onlyStopVerify(t, path)
	if verdict, _ := ev.Detail["verdict"].(string); verdict != "complete" {
		t.Errorf("detail.verdict = %v, want complete", ev.Detail["verdict"])
	}
	if ev.SessionID != "ses_emit_1" {
		t.Errorf("session_id = %q, want ses_emit_1", ev.SessionID)
	}
	if ev.Child {
		t.Error("child = true, want false")
	}
}

// TestHookStopVerifyEmitEventRecordsBlockedWithoutTheReason is the retention
// assertion: an incomplete PLAN.md makes EvaluateStopHookOutput emit
// {"decision":"block","reason":"N tasks incomplete in PLAN.md"}, and only the
// verdict CODE may reach the events file — never Reason, which is
// plan-derived text. Recording the reason turns this red, and ReadRunEvents
// would drop the line anyway.
func TestHookStopVerifyEmitEventRecordsBlockedWithoutTheReason(t *testing.T) {
	workdir := t.TempDir()
	plan := "# Plan\n\n- [x] done\n- [ ] not done\n- [ ] also not done\n"
	if err := os.WriteFile(filepath.Join(workdir, "PLAN.md"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}

	path := emitEventRun(t, workdir, "ses_emit_2", true)
	ev := onlyStopVerify(t, path)
	if verdict, _ := ev.Detail["verdict"].(string); verdict != "blocked" {
		t.Errorf("detail.verdict = %v, want blocked", ev.Detail["verdict"])
	}
	if !ev.Child {
		t.Error("child = false, want true (--child was passed)")
	}
	if len(ev.Detail) != 1 {
		t.Errorf("detail = %v, want only a verdict", ev.Detail)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"tasks incomplete", "PLAN.md", "reason"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the events file carries %q; only ids, counts and verdict codes may be recorded:\n%s", forbidden, raw)
		}
	}
}

// TestHookStopVerifyEmitEventDropsANonOpaqueSessionID: the session id arrives
// through a child process's argv, so its shape is re-checked here rather than
// trusted.
func TestHookStopVerifyEmitEventDropsANonOpaqueSessionID(t *testing.T) {
	path := emitEventRun(t, t.TempDir(), "the user wrote: deploy key is PLACEHOLDER-SECRET", false)
	ev := onlyStopVerify(t, path)
	if ev.SessionID != "" {
		t.Errorf("session_id = %q, want \"\" — a free-text value must never be recorded", ev.SessionID)
	}
}

// TestHookStopVerifyEmitEventOutsideARunIsANoOp: with no run variables set
// there is no events file to write, and that is not a failure.
func TestHookStopVerifyEmitEventOutsideARunIsANoOp(t *testing.T) {
	t.Setenv(opencodeplugin.RunOutputFileEnvVar, "")
	t.Setenv(opencodeplugin.RunIDEnvVar, "")
	if err := emitStopVerifyEvent(t.TempDir(), "ses_x", false); err != nil {
		t.Errorf("emitStopVerifyEvent outside a run = %v, want nil", err)
	}
}

// TestHookStopVerifyEmitEventPrintsNothing: the --emit-event path records the
// verdict instead of printing it. Nothing reads this process's stdout — the
// plugin spawns it with stdio ignored — so a verdict written there would be
// the silent loss this whole design exists to prevent.
func TestHookStopVerifyEmitEventPrintsNothing(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "PLAN.md"), []byte("- [ ] not done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(opencodeplugin.RunOutputFileEnvVar, outputFile)
	t.Setenv(opencodeplugin.RunIDEnvVar, "run-emit-print-1")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	emitErr := emitStopVerifyEvent(workdir, "ses_emit_3", false)
	os.Stdout = previous
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var captured strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		captured.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if emitErr != nil {
		t.Fatalf("emitStopVerifyEvent: %v", emitErr)
	}
	if captured.Len() != 0 {
		t.Errorf("--emit-event wrote %q to stdout; it must record the verdict, not print it", captured.String())
	}
}
