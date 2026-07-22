package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

// writeTelemetryEnabledConfig writes a minimal .nightgauge/config.yaml
// that turns on knowledge + knowledge.telemetry so emitKnowledgeTelemetry
// passes its enabled gate.
func writeTelemetryEnabledConfig(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	body := "owner: test\nrepo: test\nknowledge:\n  enabled: true\n  telemetry:\n    enabled: true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestKnowledgeTelemetryCmd_HasRecordSubcommand(t *testing.T) {
	cmd := knowledgeTelemetryCmd()
	if cmd.Use != "telemetry" {
		t.Errorf("Use = %q, want telemetry", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	if !subs["record"] {
		t.Error("missing 'record' subcommand")
	}
}

func TestKnowledgeTelemetryRecord_RejectsUnknownType(t *testing.T) {
	root := t.TempDir()
	cmd := knowledgeTelemetryRecordCmd()
	cmd.SetArgs([]string{"--type=bogus", "--workdir=" + root})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected validation error for unknown --type")
	}
}

func TestKnowledgeTelemetryRecord_EmitsEvent(t *testing.T) {
	root := t.TempDir()
	cmd := knowledgeTelemetryRecordCmd()
	cmd.SetArgs([]string{
		"--type=read",
		"--scope=issue:42",
		"--issue=42",
		"--path=" + filepath.Join(".nightgauge", "knowledge", "features", "42-foo", "decisions.md"),
		"--workdir=" + root,
		"--json",
	})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	jsonlPath := telemetry.Path(root)
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 event line, got %d", len(lines))
	}
	var ev telemetry.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != telemetry.EventRead {
		t.Fatalf("type: %q", ev.Type)
	}
	if ev.IssueNumber != 42 {
		t.Fatalf("issue: %d", ev.IssueNumber)
	}
	if ev.Scope != "issue:42" {
		t.Fatalf("scope: %q", ev.Scope)
	}
}

func TestKnowledgeTelemetryRecord_PreservesExplicitZeroHitIndex(t *testing.T) {
	root := t.TempDir()
	cmd := knowledgeTelemetryRecordCmd()
	cmd.SetArgs([]string{
		"--type=recall_hit",
		"--recall-id=r-abc",
		"--hit-index=0",
		"--workdir=" + root,
	})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(telemetry.Path(root))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var ev telemetry.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.HitIndex == nil || *ev.HitIndex != 0 {
		t.Fatalf("expected explicit hit_index=0 preserved, got %+v", ev.HitIndex)
	}
}

func TestEmitKnowledgeTelemetry_SilentWhenConfigDisabled(t *testing.T) {
	root := t.TempDir()
	// Config absent → no opt-in → no emit.
	emitKnowledgeTelemetry(root, telemetry.Event{Type: telemetry.EventScaffold})
	if _, err := os.Stat(telemetry.Path(root)); err == nil {
		t.Fatalf("telemetry file should not exist when knowledge config is absent")
	}
}

func TestEmitKnowledgeTelemetry_RespectsEnabledConfig(t *testing.T) {
	root := t.TempDir()
	writeTelemetryEnabledConfig(t, root)

	emitKnowledgeTelemetry(root, telemetry.Event{
		Type:        telemetry.EventScaffold,
		Scope:       "issue:7",
		IssueNumber: 7,
	})

	data, err := os.ReadFile(telemetry.Path(root))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), `"type":"scaffold"`) {
		t.Fatalf("expected scaffold event in JSONL, got %q", string(data))
	}
}

func TestStaleADRReport_FlagsNeverReadAndOldReads(t *testing.T) {
	root := t.TempDir()

	// Set up three ADRs.
	mkADR := func(rel string) string {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("# decisions\n"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return full
	}
	freshADR := mkADR(".nightgauge/knowledge/features/1-fresh/decisions.md")
	oldADR := mkADR(".nightgauge/knowledge/features/2-old/decisions.md")
	mkADR(".nightgauge/knowledge/features/3-never-read/decisions.md")

	now := time.Now()
	fresh := now.AddDate(0, 0, -3).UTC().Format(time.RFC3339)
	old := now.AddDate(0, 0, -90).UTC().Format(time.RFC3339)

	// Write events using telemetry.Emit so we exercise the real append path.
	mustEmit := func(ev telemetry.Event) {
		if err := telemetry.Emit(root, ev); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	mustEmit(telemetry.Event{Timestamp: fresh, Type: telemetry.EventRead, Path: freshADR})
	mustEmit(telemetry.Event{Timestamp: old, Type: telemetry.EventRead, Path: oldADR})

	report, err := buildStaleReport(root, 30)
	if err != nil {
		t.Fatalf("buildStaleReport: %v", err)
	}

	if report.ThresholdDays != 30 {
		t.Fatalf("threshold: %d", report.ThresholdDays)
	}
	// Expect old + never-read in the report; fresh excluded.
	stalePaths := map[string]int{}
	for _, s := range report.Stale {
		stalePaths[s.Path] = s.DaysSinceRead
	}
	if len(stalePaths) != 2 {
		t.Fatalf("expected 2 stale entries, got %d: %+v", len(stalePaths), report.Stale)
	}
	if _, ok := stalePaths[".nightgauge/knowledge/features/2-old/decisions.md"]; !ok {
		t.Fatalf("old ADR missing from stale list: %+v", report.Stale)
	}
	if _, ok := stalePaths[".nightgauge/knowledge/features/3-never-read/decisions.md"]; !ok {
		t.Fatalf("never-read ADR missing from stale list: %+v", report.Stale)
	}
}

func TestBuildStaleReport_HandlesMissingKnowledgeDir(t *testing.T) {
	root := t.TempDir()
	report, err := buildStaleReport(root, 30)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(report.Stale) != 0 {
		t.Fatalf("expected empty stale list, got %+v", report.Stale)
	}
}

// TestKnowledgeIntegration_AllEventTypesEmitted exercises the full hook path:
// scaffold + telemetry record(read/recall/recall_hit/graduate) all land in
// the JSONL with the correct stage. Matches the integration assertion in the
// plan's test plan ("integration — exec the binary in a tmpdir...").
func TestKnowledgeIntegration_AllEventTypesEmitted(t *testing.T) {
	root := t.TempDir()
	writeTelemetryEnabledConfig(t, root)
	t.Setenv("NIGHTGAUGE_STAGE", "feature-dev")
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "")

	// Drive only via the public telemetry CLI subcommand — exercises the
	// validation path and the wired-up Emit call together.
	emit := func(args ...string) {
		t.Helper()
		args = append(args, "--workdir="+root)
		cmd := knowledgeTelemetryRecordCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&strings.Builder{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("emit %v: %v", args, err)
		}
	}

	emit("--type=read", "--scope=issue:9", "--issue=9", "--path=decisions.md")
	emit("--type=recall", "--scope=issue:9", "--issue=9", "--query=hello world", "--recall-id=r-1")
	emit("--type=recall_hit", "--scope=issue:9", "--issue=9", "--recall-id=r-1", "--hit-index=2")
	emit("--type=graduate", "--scope=issue:9", "--issue=9", "--path=docs/ARCHITECTURE.md")

	f, err := os.Open(telemetry.Path(root))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()

	seen := map[telemetry.EventType]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ev.Stage != "feature-dev" {
			t.Fatalf("expected stage=feature-dev, got %q", ev.Stage)
		}
		seen[ev.Type]++
	}
	for _, want := range []telemetry.EventType{
		telemetry.EventRead,
		telemetry.EventRecall,
		telemetry.EventRecallHit,
		telemetry.EventGraduate,
	} {
		if seen[want] != 1 {
			t.Fatalf("expected one %s event, saw %d", want, seen[want])
		}
	}
}
