// Issue #161: a stage-exit record carrying every documented field was still
// not sufficient to identify which ceiling killed a stage.
//
// Three stages were SIGTERM'd with signal_source=runaway-progress at 1800s,
// 2400s and 2400s while mid-tool-call (idle_ms_at_exit of 59ms / 621ms /
// 376ms). signal_source names the CLOSURE that delivered the signal, and four
// unrelated limits funnel into "runaway-progress" alone; the one that actually
// fired — the Nx stall multiple, `stall warn threshold × 8` — is computed at
// runtime and appears in no config file, so re-reading the resolver chain ruled
// out every documented candidate and produced no answer.
//
// kill_ceiling / kill_ceiling_value close that: the name of the limit, and the
// value it resolved to together with how it was derived. These tests pin the
// pair across the IPC dispatch path — the one the autonomous workflow actually
// uses, and the one where the #161 records were written.
package ipc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
)

// TestRecordStageExitIPC_PersistsKillCeiling is the #161 regression guard: the
// record must name the ceiling and its configured value, not leave them to be
// deduced from signal_source.
func TestRecordStageExitIPC_PersistsKillCeiling(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{
		workspaceRoot: dir,
		methods:       map[string]Handler{},
	}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)
	handler := srv.methods["diagnostics.recordStageExit"]
	if handler == nil {
		t.Fatal("diagnostics.recordStageExit handler not registered")
	}

	exitCode := 143
	// The bowlsheet-infra#186 kill, verbatim.
	params := RecordStageExitParams{
		Repo:             "EdibuLLC/bowlsheet-infra",
		IssueNumber:      186,
		Stage:            "feature-validate",
		Success:          false,
		ExitCode:         &exitCode,
		TerminalKind:     "runaway_progress",
		Signal:           "SIGTERM",
		SignalSource:     "runaway-progress",
		ElapsedMs:        2400654,
		IdleMsAtExit:     376,
		KillCeiling:      "nx-stall-multiple",
		KillCeilingValue: "2400000ms (stall warn threshold 300s (source: static) × NX_RUNAWAY_KILL_MULTIPLE=8)",
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	records := readDailyRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected 1 record in daily file, got %d", len(records))
	}
	rec := records[0]

	if rec.KillCeiling != params.KillCeiling {
		t.Errorf("on-disk KillCeiling = %q, want %q", rec.KillCeiling, params.KillCeiling)
	}
	if rec.KillCeilingValue != params.KillCeilingValue {
		t.Errorf("on-disk KillCeilingValue = %q, want %q", rec.KillCeilingValue, params.KillCeilingValue)
	}
	// The derivation is the load-bearing half: "2400000ms" alone sends the
	// reader back into the resolver chain, which is exactly where #161's
	// investigation died.
	if !strings.Contains(rec.KillCeilingValue, "NX_RUNAWAY_KILL_MULTIPLE") {
		t.Errorf("KillCeilingValue = %q, want it to carry the derivation", rec.KillCeilingValue)
	}
}

// TestStageExitRecord_KillCeilingJSONTags pins the on-disk keys. They are a
// diagnostic contract — retros grep them and historical records already carry
// them, so a rename silently breaks every query written against them.
func TestStageExitRecord_KillCeilingJSONTags(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{workspaceRoot: dir, methods: map[string]Handler{}}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)
	handler := srv.methods["diagnostics.recordStageExit"]

	raw, err := json.Marshal(RecordStageExitParams{
		Repo:             "nightgauge/nightgauge",
		IssueNumber:      161,
		Stage:            "feature-dev",
		Success:          false,
		KillCeiling:      "stage-hard-cap",
		KillCeilingValue: "5400000ms (pipeline.stage_hard_caps.feature-dev)",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	line := readDailyLine(t, dir)
	for _, key := range []string{`"kill_ceiling":`, `"kill_ceiling_value":`} {
		if !strings.Contains(line, key) {
			t.Errorf("daily JSONL line missing %s\nline=%s", key, line)
		}
	}
}

// TestStageExitRecord_KillCeilingOmittedWhenAbsent — a healthy exit, or a kill
// that enforced no configured limit (operator abort, external signal), must
// carry no ceiling. Emitting an empty pair would assert a limit fired when none
// did, which is worse than silence.
func TestStageExitRecord_KillCeilingOmittedWhenAbsent(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{workspaceRoot: dir, methods: map[string]Handler{}}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)
	handler := srv.methods["diagnostics.recordStageExit"]

	raw, err := json.Marshal(RecordStageExitParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 161,
		Stage:       "pr-merge",
		Success:     true,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	line := readDailyLine(t, dir)
	for _, key := range []string{"kill_ceiling", "kill_ceiling_value"} {
		if strings.Contains(line, key) {
			t.Errorf("healthy record carries %s; want it omitted\nline=%s", key, line)
		}
	}
}

// readDailyLine returns the single RAW JSONL line as written to disk. Reading
// the bytes rather than a decoded-then-remarshalled record is deliberate: only
// the raw line proves what the writer actually emitted, which is what an
// operator's `jq` and `grep` see.
func readDailyLine(t *testing.T, rootDir string) string {
	t.Helper()
	path := diagnostics.DailyFilePath(rootDir, time.Now())
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read daily file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line in daily file, got %d", len(lines))
	}
	return lines[0]
}
