package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestStageBudgetsAreSnapshottedOncePerRun is #2257: stage budgets are read
// once at run start. An edit to the tracked config during the run, or to the
// returned map, must not change what later stages of that run dispatch under.
func TestStageBudgetsAreSnapshottedOncePerRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	cfgPath := filepath.Join(root, ".nightgauge", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(turns string) {
		body := "schema_version: \"2\"\nowner: nightgauge\npipeline:\n  stage_budgets:\n    default:\n      max_turns: " + turns + "\n"
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("50")
	snapshot := pipelineStageBudgets(root)
	write("2147483647") // a stage rewrites the tracked config mid-run
	if got := snapshot[config.StageBudgetDefaultKey].MaxTurns; got != 50 {
		t.Fatalf("snapshot max_turns = %d after the edit, want 50", got)
	}
	again := pipelineStageBudgets(root)
	again[config.StageBudgetDefaultKey] = config.StageBudget{MaxTurns: 1}
	if got := snapshot[config.StageBudgetDefaultKey].MaxTurns; got != 50 {
		t.Fatalf("snapshot shares storage with a later read: max_turns = %d", got)
	}

	// runPipeline reads the budgets exactly once and every stage dispatch
	// takes the snapshot, never a fresh read.
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)\nfunc \(s \*Scheduler\) runPipeline\(.*?\n}\n`).Find(src)
	if body == nil {
		t.Fatal("runPipeline not found in scheduler.go")
	}
	if n := len(regexp.MustCompile(`pipelineStageBudgets\(`).FindAll(body, -1)); n != 1 {
		t.Errorf("runPipeline calls pipelineStageBudgets %d times, want once at run start", n)
	}
	if !regexp.MustCompile(`StageBudgets:\s+stageBudgets,`).Match(body) {
		t.Error("stage dispatch does not use the run's stageBudgets snapshot")
	}
}
