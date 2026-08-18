// Tests that pipeline.notifyComplete — the primary headless/CLI write path
// (internal/ipc/server.go's `notifyComplete` handler) — actually resolves
// pipeline.logs.history_retention_days from config.yaml and applies it via
// HistoryWriter.SetRetentionDays before writing (#674). The pruning mechanics
// themselves are covered exhaustively at the HistoryWriter level
// (internal/state/history_retention_test.go); this file pins that the wiring
// at THIS call site actually reaches the writer with the configured value,
// not the hardcoded default.
package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestNotifyComplete_PrunesUnderConfiguredRetention seeds a 40-day-old daily
// JSONL file (simulating a prior run) alongside a project config.yaml setting
// pipeline.logs.history_retention_days to 10, then drives a single fresh run
// through pipeline.notifyStageTransition/notifyComplete. If the configured
// value reaches the writer, appendAndIndex's prune pass (which fires on every
// write) deletes the 40-day-old file; a 40-day-old file survives the
// hardcoded 90-day default, so this fails loudly if SetRetentionDays is never
// called or is called with the wrong value.
func TestNotifyComplete_PrunesUnderConfiguredRetention(t *testing.T) {
	restore := config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(t.TempDir(), "no-machine-config.yaml"), nil
	})
	defer restore()

	dir := t.TempDir()

	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	cfgYAML := "schema_version: \"2\"\nowner: nightgauge\npipeline:\n  logs:\n    history_retention_days: 10\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatalf("mkdir history dir: %v", err)
	}
	oldDate := time.Now().AddDate(0, 0, -40)
	oldFile := oldDate.Format("2006-01-02") + ".jsonl"
	oldRecord := fmt.Sprintf(
		`{"schema_version":"2","record_type":"run","issue_number":9301,"run_id":"019009e1-0000-7000-8000-000000009301","title":"stale run","branch":"feat/9301","started_at":%q,"recorded_at":%q,"outcome":"complete","stages":{"issue-pickup":{"status":"complete"}}}`+"\n",
		oldDate.Format(time.RFC3339), oldDate.Format(time.RFC3339),
	)
	if err := os.WriteFile(filepath.Join(historyDir, oldFile), []byte(oldRecord), 0o644); err != nil {
		t.Fatalf("seed stale daily file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(historyDir, oldFile)); err != nil {
		t.Fatalf("seeded stale file missing before test: %v", err)
	}

	s := NewServer(nil, WithWorkspaceRoot(dir))
	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":9302,"stage":"feature-dev","status":"running","runId":"019009e1-0000-7000-8000-000000009302"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":9302,"success":true,"totalDurationMs":1000,"runId":"019009e1-0000-7000-8000-000000009302"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	if _, err := os.Stat(filepath.Join(historyDir, oldFile)); !os.IsNotExist(err) {
		t.Errorf("expected the 40-day-old daily file %s to be pruned under the configured 10-day "+
			"retention (pipeline.logs.history_retention_days) — a surviving file means notifyComplete "+
			"is not threading the config value to HistoryWriter.SetRetentionDays, stat err = %v", oldFile, err)
	}
}
