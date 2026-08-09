package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// `nightgauge autonomous resume` is the CLI half of the one action that
// clears a machine-raised halt (#405) — and the command DiscordService's
// safety-pause notification has been telling operators to run. These cover
// the no-daemon path; the daemon path is the IPC method, pinned in
// internal/ipc/autonomous_halt_wire_test.go.

func writeAutonomousState(t *testing.T, root string, st orchestrator.AutonomousState) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "autonomous")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readAutonomousState(t *testing.T, root string) orchestrator.AutonomousState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".nightgauge", "autonomous", "state.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var st orchestrator.AutonomousState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return st
}

func runAutonomousResume(t *testing.T, root string) string {
	t.Helper()
	t.Chdir(root)
	cmd := autonomousResumeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("autonomous resume: %v (output %q)", err, out.String())
	}
	return out.String()
}

func TestAutonomousResumeCmdClearsALatchedHaltOffline(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		// The laundering shape: a graceful shutdown wrote its exit status
		// over the halted fleet. The latch is what is still true.
		Status:           "cancelled",
		PauseReason:      "haltQueueOnSlotFailure: issue #405 failed at pr-merge",
		PauseTriggeredBy: "haltQueueOnSlotFailure",
		MachineHalt:      &orchestrator.MachineHaltRecord{Tag: "haltQueueOnSlotFailure", Status: "paused"},
	})

	out := runAutonomousResume(t, root)

	st := readAutonomousState(t, root)
	if st.MachineHalt != nil {
		t.Error("the halt latch survived `autonomous resume`")
	}
	if st.PauseTriggeredBy != "" || st.PauseReason != "" {
		t.Errorf("provenance survived the clear: %+v", st)
	}
	if st.Status != "stopped" {
		t.Errorf("status = %q, want %q — no process is dispatching after an offline clear", st.Status, "stopped")
	}
	if !strings.Contains(out, "Cleared") || !strings.Contains(out, "autonomous run") {
		t.Errorf("output %q does not tell the operator what happened and what to do next", out)
	}
}

func TestAutonomousResumeCmdIsANoOpWithNothingLatched(t *testing.T) {
	root := t.TempDir()
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "stopped"})

	out := runAutonomousResume(t, root)

	if !strings.Contains(out, "No machine-raised halt") {
		t.Errorf("output %q does not say there was nothing to clear", out)
	}
	if st := readAutonomousState(t, root); st.Status != "stopped" {
		t.Errorf("status = %q, want stopped — a no-op resume must not rewrite state", st.Status)
	}
}

func TestAutonomousResumeCmdWithNoStateFile(t *testing.T) {
	// A workspace that has never run autonomous mode: no file, no error.
	out := runAutonomousResume(t, t.TempDir())
	if !strings.Contains(out, "No machine-raised halt") {
		t.Errorf("output %q, want the nothing-to-do notice", out)
	}
}
