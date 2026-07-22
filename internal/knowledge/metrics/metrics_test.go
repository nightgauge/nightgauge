package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

func writeFixture(t *testing.T, dir string, events []telemetry.Event) {
	t.Helper()
	histDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(histDir, "knowledge-events.jsonl"))
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func TestAggregate_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	r, err := Aggregate(dir, 7, 30)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if r.Status != StatusEmpty {
		t.Errorf("Status = %s; want empty", r.Status)
	}
	if r.WindowDays != 7 || r.StaleDays != 30 {
		t.Errorf("WindowDays/StaleDays not echoed: %+v", r)
	}
	if r.PerStage == nil || r.TopRecalled == nil || r.StaleEntries == nil || r.GraduationHistory == nil {
		t.Error("slices should be initialized to empty, not nil")
	}
}

func TestAggregate_WindowFilter(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	inside := now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	outside := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	writeFixture(t, dir, []telemetry.Event{
		{Timestamp: inside, Type: telemetry.EventWrite, Stage: "feature-dev", Path: "k/a.md"},
		{Timestamp: outside, Type: telemetry.EventWrite, Stage: "feature-dev", Path: "k/b.md"},
		{Timestamp: inside, Type: telemetry.EventRead, Stage: "feature-dev", Path: "k/a.md"},
	})

	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if r.Totals.Writes != 1 {
		t.Errorf("Writes = %d; want 1", r.Totals.Writes)
	}
	if r.Totals.Reads != 1 {
		t.Errorf("Reads = %d; want 1", r.Totals.Reads)
	}
	if r.Status != StatusEnabled {
		t.Errorf("Status = %s; want enabled", r.Status)
	}
}

func TestAggregate_HitRateAndPerStage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	events := []telemetry.Event{
		{Timestamp: ts, Type: telemetry.EventRecall, Stage: "feature-planning"},
		{Timestamp: ts, Type: telemetry.EventRecall, Stage: "feature-planning"},
		{Timestamp: ts, Type: telemetry.EventRecall, Stage: "feature-dev"},
		{Timestamp: ts, Type: telemetry.EventRecallHit, Stage: "feature-planning", Path: "k/a.md"},
		{Timestamp: ts, Type: telemetry.EventRecallHit, Stage: "feature-planning", Path: "k/a.md"},
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "feature-dev", Path: "k/b.md"},
		{Timestamp: ts, Type: telemetry.EventWrite, Stage: "feature-dev", Path: "k/c.md"},
	}
	writeFixture(t, dir, events)

	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if r.HitRate == nil || *r.HitRate != 2.0/3.0 {
		t.Errorf("HitRate = %v; want 2/3", r.HitRate)
	}
	if r.Totals.Recalls != 3 || r.Totals.RecallHits != 2 {
		t.Errorf("Recalls/Hits = %d/%d; want 3/2", r.Totals.Recalls, r.Totals.RecallHits)
	}
	if len(r.PerStage) != 2 {
		t.Errorf("PerStage rows = %d; want 2", len(r.PerStage))
	}
	// stages are alphabetical
	if r.PerStage[0].Stage != "feature-dev" || r.PerStage[1].Stage != "feature-planning" {
		t.Errorf("PerStage not alphabetical: %+v", r.PerStage)
	}
	if r.PerStage[1].Recalls != 2 || r.PerStage[1].RecallHits != 2 {
		t.Errorf("planning stage wrong: %+v", r.PerStage[1])
	}
}

func TestAggregate_TopRecalledOrdering(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	events := []telemetry.Event{
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "x", Path: "a.md"},
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "x", Path: "a.md"},
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "x", Path: "a.md"},
		{Timestamp: ts, Type: telemetry.EventRecallHit, Stage: "x", Path: "b.md"},
		{Timestamp: ts, Type: telemetry.EventRecallHit, Stage: "x", Path: "b.md"},
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "x", Path: "c.md"},
	}
	writeFixture(t, dir, events)
	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(r.TopRecalled) != 3 {
		t.Fatalf("TopRecalled len = %d; want 3", len(r.TopRecalled))
	}
	if r.TopRecalled[0].Path != "a.md" || r.TopRecalled[0].Hits != 3 {
		t.Errorf("first row = %+v", r.TopRecalled[0])
	}
	if r.TopRecalled[1].Path != "b.md" || r.TopRecalled[1].Hits != 2 {
		t.Errorf("second row = %+v", r.TopRecalled[1])
	}
}

func TestAggregate_StaleEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	old := now.Add(-25 * 24 * time.Hour).Format(time.RFC3339)
	events := []telemetry.Event{
		{Timestamp: fresh, Type: telemetry.EventRead, Stage: "x", Path: "fresh.md"},
		{Timestamp: old, Type: telemetry.EventRead, Stage: "x", Path: "old.md"},
		{Timestamp: fresh, Type: telemetry.EventWrite, Stage: "x", Path: "neverread.md"},
		{Timestamp: fresh, Type: telemetry.EventScaffold, Stage: "x", Path: "scaffoldonly.md"},
	}
	writeFixture(t, dir, events)
	r, err := AggregateAt(dir, 90, 7, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	paths := map[string]int{}
	for _, s := range r.StaleEntries {
		paths[s.Path] = s.DaysSinceTouch
	}
	if _, ok := paths["fresh.md"]; ok {
		t.Errorf("fresh.md should not be stale: %+v", paths)
	}
	if d, ok := paths["old.md"]; !ok || d < 7 {
		t.Errorf("old.md should be stale with days >= 7, got %d", d)
	}
	if _, ok := paths["neverread.md"]; !ok {
		t.Errorf("neverread.md (written but never read) should be stale")
	}
}

func TestAggregate_GraduationHistory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	t1 := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	t2 := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	events := []telemetry.Event{
		{Timestamp: t1, Type: telemetry.EventGraduate, Stage: "manual", Path: "decisions.md", Mode: "manual", IssueNumber: 100},
		{Timestamp: t2, Type: telemetry.EventGraduate, Stage: "manual", Path: "decisions.md", Mode: "auto", IssueNumber: 101},
	}
	writeFixture(t, dir, events)
	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(r.GraduationHistory) != 2 {
		t.Fatalf("GraduationHistory len = %d", len(r.GraduationHistory))
	}
	// Most recent first.
	if r.GraduationHistory[0].IssueNumber != 101 {
		t.Errorf("expected issue 101 first, got %+v", r.GraduationHistory[0])
	}
	if r.GraduationHistory[0].Mode != "auto" || r.GraduationHistory[1].Mode != "manual" {
		t.Errorf("modes wrong: %+v", r.GraduationHistory)
	}
}

func TestAggregate_EmptyModeBackfillsManual(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	writeFixture(t, dir, []telemetry.Event{
		{Timestamp: ts, Type: telemetry.EventGraduate, Stage: "manual", Path: "d.md"},
	})
	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if r.GraduationHistory[0].Mode != "manual" {
		t.Errorf("empty Mode should backfill to manual; got %q", r.GraduationHistory[0].Mode)
	}
}

func TestAggregate_UnknownStage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	writeFixture(t, dir, []telemetry.Event{
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "", Path: "x.md"},
	})
	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(r.PerStage) != 1 || r.PerStage[0].Stage != "unknown" {
		t.Errorf("expected single unknown stage row; got %+v", r.PerStage)
	}
}

func TestAggregate_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	histDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	good, _ := json.Marshal(telemetry.Event{Timestamp: ts, Type: telemetry.EventRead, Stage: "x", Path: "a.md"})
	if err := os.WriteFile(filepath.Join(histDir, "knowledge-events.jsonl"),
		[]byte(string(good)+"\nnot-json\n"+string(good)+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := AggregateAt(dir, 7, 30, now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if r.Totals.Reads != 2 {
		t.Errorf("Reads = %d; want 2 (malformed line skipped)", r.Totals.Reads)
	}
}

func TestAggregate_InvalidArgs(t *testing.T) {
	if _, err := Aggregate(t.TempDir(), 0, 30); err == nil {
		t.Error("expected error for windowDays=0")
	}
	if _, err := Aggregate(t.TempDir(), 7, -1); err == nil {
		t.Error("expected error for staleDays<0")
	}
}
