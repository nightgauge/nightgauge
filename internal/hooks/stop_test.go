package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStopAllComplete(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Set up a fake branch reference
	setupFakeBranch(t, dir, "feat/42-test-feature")

	plan := `# Plan for #42

- [x] Task 1
- [x] Task 2
- [x] Task 3
`
	if err := os.WriteFile(filepath.Join(planDir, "42-test-feature.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	result := EvaluateStop(dir)
	if !result.OK {
		t.Errorf("expected OK=true, got false: %s", result.Reason)
	}
}

func TestStopIncomplete(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0755); err != nil {
		t.Fatal(err)
	}

	setupFakeBranch(t, dir, "feat/42-test-feature")

	plan := `# Plan for #42

- [x] Task 1
- [ ] Task 2
- [ ] Task 3
`
	if err := os.WriteFile(filepath.Join(planDir, "42-test-feature.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	result := EvaluateStop(dir)
	if result.OK {
		t.Error("expected OK=false for incomplete tasks")
	}
	if result.Reason != "2 tasks incomplete in PLAN.md" {
		t.Errorf("reason = %q, want '2 tasks incomplete in PLAN.md'", result.Reason)
	}
}

func TestStopNoPlanFile(t *testing.T) {
	dir := t.TempDir()
	// No plan file at all — should allow stop
	result := EvaluateStop(dir)
	if !result.OK {
		t.Errorf("expected OK=true when no plan file, got false: %s", result.Reason)
	}
}

func TestStopFallbackPlanMD(t *testing.T) {
	dir := t.TempDir()

	plan := `# PLAN

- [x] Done task
- [ ] Not done yet
`
	if err := os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	result := EvaluateStop(dir)
	if result.OK {
		t.Error("expected OK=false for incomplete PLAN.md")
	}
}

func TestStopPipelinePlanFallback(t *testing.T) {
	dir := t.TempDir()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		t.Fatal(err)
	}

	plan := `- [ ] Incomplete task
`
	if err := os.WriteFile(filepath.Join(pipelineDir, "PLAN.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	result := EvaluateStop(dir)
	if result.OK {
		t.Error("expected OK=false for incomplete pipeline PLAN.md")
	}
}

func TestParsePlanFile(t *testing.T) {
	dir := t.TempDir()
	plan := `# Implementation Plan

## Phase 1
- [x] Set up project structure
- [x] Add dependencies
- [ ] Write core logic

## Phase 2
- [ ] Write tests
- [x] Update docs

Some non-checkbox text
- This is not a checkbox
`
	planPath := filepath.Join(dir, "PLAN.md")
	if err := os.WriteFile(planPath, []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	status, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("parsePlanFile: %v", err)
	}

	if status.Total != 5 {
		t.Errorf("Total = %d, want 5", status.Total)
	}
	if status.Complete != 3 {
		t.Errorf("Complete = %d, want 3", status.Complete)
	}
	if status.Incomplete != 2 {
		t.Errorf("Incomplete = %d, want 2", status.Incomplete)
	}
}

func TestGetIssueNumberFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"feat/42-add-feature", "42"},
		{"fix/123-fix-bug", "123"},
		{"docs/7-update-readme", "7"},
		{"refactor/99-cleanup", "99"},
		{"main", ""},
		{"develop", ""},
	}

	for _, tt := range tests {
		dir := t.TempDir()
		setupFakeBranch(t, dir, tt.branch)
		got := getIssueNumberFromBranch(dir)
		if got != tt.want {
			t.Errorf("getIssueNumberFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestStopResultJSON(t *testing.T) {
	dir := t.TempDir()
	data, err := EvaluateStopJSON(dir)
	if err != nil {
		t.Fatalf("EvaluateStopJSON: %v", err)
	}

	var result StopResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got %+v", result)
	}
}

// TestStopWritesSentinelOnIncomplete verifies the Issue #3542 sentinel: when
// EvaluateStop returns OK=false and the issue number is resolvable from the
// branch, it leaves .nightgauge/pipeline/stop-hook-status-{N}.json so the
// Go scheduler can detect that the stop hook blocked session exit.
func TestStopWritesSentinelOnIncomplete(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupFakeBranch(t, dir, "feat/3542-recovery")
	plan := "# Plan for #3542\n\n- [x] Task 1\n- [ ] Task 2\n"
	if err := os.WriteFile(filepath.Join(planDir, "3542-recovery.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	result := EvaluateStop(dir)
	if result.OK {
		t.Fatal("expected OK=false for incomplete plan")
	}

	sentinelPath := filepath.Join(dir, ".nightgauge", "pipeline", "stop-hook-status-3542.json")
	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("sentinel file not written: %v", err)
	}
	var sentinel struct {
		OK        bool   `json:"ok"`
		Reason    string `json:"reason"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &sentinel); err != nil {
		t.Fatalf("sentinel is not valid JSON: %v", err)
	}
	if sentinel.OK {
		t.Error("sentinel.ok = true, want false")
	}
	if sentinel.Reason != "1 tasks incomplete in PLAN.md" {
		t.Errorf("sentinel.reason = %q, want '1 tasks incomplete in PLAN.md'", sentinel.Reason)
	}
	if sentinel.Timestamp == "" {
		t.Error("sentinel.timestamp empty")
	}
}

// TestStopNoSentinelWhenComplete verifies the sentinel is NOT written on the
// happy path (all tasks complete → OK=true).
func TestStopNoSentinelWhenComplete(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupFakeBranch(t, dir, "feat/3542-recovery")
	plan := "# Plan for #3542\n\n- [x] Task 1\n- [x] Task 2\n"
	if err := os.WriteFile(filepath.Join(planDir, "3542-recovery.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	if result := EvaluateStop(dir); !result.OK {
		t.Fatalf("expected OK=true, got %s", result.Reason)
	}
	sentinelPath := filepath.Join(dir, ".nightgauge", "pipeline", "stop-hook-status-3542.json")
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Errorf("sentinel file should not exist on the OK=true path, stat err=%v", err)
	}
}

// TestStopNoSentinelWhenBranchUnresolvable verifies the sentinel write is
// skipped (no panic, no stray file) when the issue number can't be derived.
func TestStopNoSentinelWhenBranchUnresolvable(t *testing.T) {
	dir := t.TempDir()
	setupFakeBranch(t, dir, "main")
	plan := "# PLAN\n\n- [ ] Not done\n"
	if err := os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := EvaluateStop(dir); result.OK {
		t.Fatal("expected OK=false for incomplete PLAN.md")
	}
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if entries, err := os.ReadDir(pipelineDir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				t.Errorf("unexpected sentinel %q written for unresolvable branch", e.Name())
			}
		}
	}
}

// TestEvaluateStopHookOutput_OKEmitsNothing — the canonical Claude Code Stop
// hook contract: when stop is allowed, emit empty output. Anything else
// triggers the spurious `stop-hook-error` notification we're trying to kill.
func TestEvaluateStopHookOutput_OKEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	// No plan file → OK=true
	out, err := EvaluateStopHookOutput(dir)
	if err != nil {
		t.Fatalf("EvaluateStopHookOutput: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty output for OK=true, got %q", string(out))
	}
}

// TestEvaluateStopHookOutput_BlockEmitsCanonicalJSON — when there are
// incomplete tasks, emit `{"decision":"block","reason":"..."}`. NO `ok`
// field — that's the legacy shape Claude Code can't parse.
func TestEvaluateStopHookOutput_BlockEmitsCanonicalJSON(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupFakeBranch(t, dir, "feat/42-block")
	plan := "# Plan for #42\n\n- [x] Task 1\n- [ ] Task 2\n- [ ] Task 3\n"
	if err := os.WriteFile(filepath.Join(planDir, "42-block.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := EvaluateStopHookOutput(dir)
	if err != nil {
		t.Fatalf("EvaluateStopHookOutput: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("want non-empty output for OK=false")
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw=%q", err, string(out))
	}

	if got["decision"] != "block" {
		t.Errorf("decision=%v, want %q (canonical Claude Code Stop hook contract)", got["decision"], "block")
	}
	if got["reason"] != "2 tasks incomplete in PLAN.md" {
		t.Errorf("reason=%v, want %q", got["reason"], "2 tasks incomplete in PLAN.md")
	}
	// Regression guard: the legacy `ok` field MUST NOT appear in the
	// canonical hook output; its presence is what caused Claude Code to
	// fire stop-hook-error noise on every stage exit (#3605).
	if _, hasOK := got["ok"]; hasOK {
		t.Error("output unexpectedly contains legacy `ok` field — must be {decision,reason} only")
	}
}

// TestEvaluateStopHookOutput_DoesNotAffectSentinel — the canonical output
// path must NOT break the sentinel-file machinery the Go scheduler uses for
// uncommitted-work recovery. The sentinel is the load-bearing internal
// signal; the stdout change is purely about Claude Code's contract.
func TestEvaluateStopHookOutput_DoesNotAffectSentinel(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, ".nightgauge", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupFakeBranch(t, dir, "feat/99-sentinel")
	plan := "# Plan for #99\n\n- [ ] Open work\n"
	if err := os.WriteFile(filepath.Join(planDir, "99-sentinel.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EvaluateStopHookOutput(dir); err != nil {
		t.Fatalf("EvaluateStopHookOutput: %v", err)
	}

	// Sentinel must still be written so the Go scheduler can detect
	// uncommitted work and trigger recovery.
	sentinelPath := filepath.Join(dir, ".nightgauge", "pipeline", "stop-hook-status-99.json")
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("sentinel file should still be written on the OK=false path: %v", err)
	}
}

// setupFakeBranch creates a .git/HEAD file pointing to the given branch.
func setupFakeBranch(t *testing.T, dir, branch string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head), 0644); err != nil {
		t.Fatal(err)
	}
}
