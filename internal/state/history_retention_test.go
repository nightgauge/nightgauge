// Package state_test exercises HistoryWriter's retention pruning as an
// external test package (not `package state`): it needs internal/config to
// resolve pipeline.logs.history_retention_days the same way the real write
// call sites (internal/ipc/server.go, internal/orchestrator/scheduler.go) do,
// and internal/config transitively imports internal/state (via
// intelligence/routing -> platform -> state) — so this dependency can only be
// taken from a package OUTSIDE state, never from state's own internal test
// files, or `go test` reports an import cycle (#674).
package state_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/state"
)

// writeProjectConfig writes a minimal project-tier config.yaml. When
// retentionDays > 0 it sets pipeline.logs.history_retention_days; 0 omits the
// key entirely so LoadMerged/ResolveHistoryRetentionDays exercises the
// "unset" default path.
func writeProjectConfig(t *testing.T, root string, retentionDays int) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := "schema_version: \"2\"\nowner: nightgauge\n"
	if retentionDays > 0 {
		contents += fmt.Sprintf("pipeline:\n  logs:\n    history_retention_days: %d\n", retentionDays)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// newWriterWithConfiguredRetention builds a HistoryWriter exactly the way the
// real headless write paths do (#674): load merged config, apply the
// resolved pipeline.logs.history_retention_days via SetRetentionDays.
func newWriterWithConfiguredRetention(t *testing.T, root string) *state.HistoryWriter {
	t.Helper()
	hw := state.NewHistoryWriter(root)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	hw.SetRetentionDays(cfg.Pipeline.ResolveHistoryRetentionDays())
	return hw
}

// minimalRecord builds the smallest V2RunRecord that survives
// appendAndIndex's idempotency/dedup checks (non-empty Stages, a unique
// RunID) with RecordedAt/StartedAt set to recordedAt, so WriteRecord files it
// under that date's daily JSONL (mirrors the crash-recovery synthesizer's use
// of WriteRecord to backdate a record — see HistoryWriter.WriteRecord).
func minimalRecord(issueNumber int, runID string, recordedAt time.Time) state.V2RunRecord {
	ts := recordedAt.Format(time.RFC3339)
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   issueNumber,
		RunID:         runID,
		Title:         "test run",
		Branch:        "feat/test",
		StartedAt:     ts,
		RecordedAt:    ts,
		Outcome:       "complete",
		Stages: map[string]state.V2StageDetail{
			"issue-pickup": {Status: "complete"},
		},
	}
}

// TestHistoryWriter_HeadlessPruneRespectsConfiguredRetention is the
// acceptance test for #674's headless-workspace gap: writing records spanning
// more than the CONFIGURED retention window (not the hardcoded 90 days) must
// prune the old daily file from disk AND drop its index.json entry, with no
// VSCode extension involved anywhere in this test.
func TestHistoryWriter_HeadlessPruneRespectsConfiguredRetention(t *testing.T) {
	restore := config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(t.TempDir(), "no-machine-config.yaml"), nil
	})
	defer restore()

	dir := t.TempDir()
	writeProjectConfig(t, dir, 10) // 10-day retention, far below the 90-day default

	hw := newWriterWithConfiguredRetention(t, dir)

	now := time.Now()
	oldTime := now.AddDate(0, 0, -40)   // outside a 10-day window
	recentTime := now.AddDate(0, 0, -5) // inside a 10-day window

	if err := hw.WriteRecord(minimalRecord(9101, "run-old-9101", oldTime)); err != nil {
		t.Fatalf("WriteRecord(old): %v", err)
	}
	if err := hw.WriteRecord(minimalRecord(9102, "run-recent-9102", recentTime)); err != nil {
		t.Fatalf("WriteRecord(recent): %v", err)
	}

	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	oldFile := oldTime.Local().Format("2006-01-02") + ".jsonl"
	recentFile := recentTime.Local().Format("2006-01-02") + ".jsonl"

	if _, err := os.Stat(filepath.Join(historyDir, oldFile)); !os.IsNotExist(err) {
		t.Errorf("expected old daily file %s to be pruned under a 10-day retention, stat err = %v", oldFile, err)
	}
	if _, err := os.Stat(filepath.Join(historyDir, recentFile)); err != nil {
		t.Errorf("expected recent daily file %s to survive, stat err = %v", recentFile, err)
	}

	data, err := os.ReadFile(filepath.Join(historyDir, "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}
	var idx state.V2Index
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("unmarshal index.json: %v", err)
	}

	for _, e := range idx.Entries {
		if e.IssueNumber == 9101 {
			t.Errorf("index.json still references issue 9101 whose daily file was pruned — " +
				"an orphaned index entry pointing at a deleted file is its own bug")
		}
	}
	found := false
	for _, e := range idx.Entries {
		if e.IssueNumber == 9102 {
			found = true
		}
	}
	if !found {
		t.Errorf("index.json is missing the surviving entry for issue 9102; entries=%+v", idx.Entries)
	}
}

// TestHistoryWriter_DefaultRetentionWhenUnconfigured pins the fallback: with
// no history_retention_days key at all, a 40-day-old record — which the
// PREVIOUS test proves gets pruned under a 10-day retention — must survive
// under the 90-day default.
func TestHistoryWriter_DefaultRetentionWhenUnconfigured(t *testing.T) {
	restore := config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(t.TempDir(), "no-machine-config.yaml"), nil
	})
	defer restore()

	dir := t.TempDir()
	writeProjectConfig(t, dir, 0) // no history_retention_days key

	hw := newWriterWithConfiguredRetention(t, dir)

	survivingTime := time.Now().AddDate(0, 0, -40)
	if err := hw.WriteRecord(minimalRecord(9201, "run-default-9201", survivingTime)); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	fname := survivingTime.Local().Format("2006-01-02") + ".jsonl"
	if _, err := os.Stat(filepath.Join(historyDir, fname)); err != nil {
		t.Errorf("expected file %s to survive under the 90-day default, stat err = %v", fname, err)
	}
}

// TestHistoryWriter_FallsBackToDefaultWhenRetentionNeverConfigured pins
// state.DefaultHistoryRetentionDays' OWN fallback — distinct from the
// previous test, which pins config.DefaultHistoryRetentionDays flowing
// through an explicit SetRetentionDays call. A HistoryWriter nobody ever
// calls SetRetentionDays on (e.g. because config.Load failed at the write
// call site, or a future call site forgets to wire it) must still apply a
// 90-day default via effectiveRetentionDays, not prune everything (a bare
// zero) or keep everything forever (#674).
func TestHistoryWriter_FallsBackToDefaultWhenRetentionNeverConfigured(t *testing.T) {
	dir := t.TempDir()
	hw := state.NewHistoryWriter(dir) // SetRetentionDays deliberately never called

	survivingTime := time.Now().AddDate(0, 0, -40)
	if err := hw.WriteRecord(minimalRecord(9401, "run-fallback-9401", survivingTime)); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	fname := survivingTime.Local().Format("2006-01-02") + ".jsonl"
	if _, err := os.Stat(filepath.Join(historyDir, fname)); err != nil {
		t.Errorf("expected file %s to survive under the un-configured 90-day default, stat err = %v", fname, err)
	}
}
