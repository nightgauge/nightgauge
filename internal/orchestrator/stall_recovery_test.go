package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

func TestHasCostCapKillMarker(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{"empty", "", false},
		{"bracketed token", "[cost-cap-exceeded] feature-dev exceeded $5.00 cap", true},
		{"freeform", "stage feature-dev cost cap exceeded after $5.12", true},
		{"hyphenated", "killed: cost-cap-exceeded at $5.00", true},
		{"case insensitive", "[COST-CAP-EXCEEDED] feature-dev", true},
		{"plain stall", "subagent stalled and killed after 4800s", false},
		{"plain budget", "stage_budget_exceeded for feature-dev", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasCostCapKillMarker(tc.err); got != tc.want {
				t.Errorf("HasCostCapKillMarker(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestCanRewindFromStage(t *testing.T) {
	tests := []struct {
		stage state.PipelineStage
		want  bool
	}{
		{state.StageIssuePickup, false},
		{state.StageFeaturePlanning, false},
		{state.StageFeatureDev, true},
		{state.StageFeatureValidate, true},
		{state.StagePRCreate, false},
		{state.StagePRMerge, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.stage), func(t *testing.T) {
			if got := CanRewindFromStage(tc.stage); got != tc.want {
				t.Errorf("CanRewindFromStage(%s) = %v, want %v", tc.stage, got, tc.want)
			}
		})
	}
}

func writePlanningContext(t *testing.T, root string, issue int, filesToModify []string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"schema_version":   "1.0",
		"issue_number":     issue,
		"plan_file":        "plan.md",
		"approach":         "x",
		"files_to_create":  []string{},
		"files_to_modify":  filesToModify,
		"files_to_read":    []string{},
		"validation_steps": []string{},
	})
	if err := os.WriteFile(filepath.Join(dir, "planning-1.json"), body, 0644); err != nil {
		t.Fatalf("write planning: %v", err)
	}
}

func TestClassifyStallSignal_ComplexityFromHighModifyCount(t *testing.T) {
	root := t.TempDir()
	writePlanningContext(t, root, 1, []string{"a.go", "b.go", "c.go", "d.go", "e.go"})
	signal := ClassifyStallSignal(state.StageFeatureDev, "stall kill threshold reached", root, 1)
	if signal.SignalType != "COMPLEXITY_UNDERESTIMATED" {
		t.Errorf("signal_type = %s, want COMPLEXITY_UNDERESTIMATED", signal.SignalType)
	}
	if signal.Severity != "blocking" {
		t.Errorf("severity = %s, want blocking", signal.Severity)
	}
	if signal.BacktrackTargetStage != string(state.StageFeaturePlanning) {
		t.Errorf("backtrack_target_stage = %s, want feature-planning", signal.BacktrackTargetStage)
	}
	if !strings.Contains(signal.Rationale, "synthesized by scheduler on stall-kill") {
		t.Errorf("rationale missing synthetic-signal prefix: %q", signal.Rationale)
	}
}

func TestClassifyStallSignal_ScopeDiscoveredFromMissingFiles(t *testing.T) {
	root := t.TempDir()
	// Two files, but neither exists on disk.
	writePlanningContext(t, root, 1, []string{"missing-1.go", "missing-2.go"})
	signal := ClassifyStallSignal(state.StageFeatureValidate, "heartbeat stall", root, 1)
	if signal.SignalType != "SCOPE_DISCOVERED" {
		t.Errorf("signal_type = %s, want SCOPE_DISCOVERED", signal.SignalType)
	}
	if len(signal.Evidence) < 2 {
		t.Errorf("evidence too short: %v", signal.Evidence)
	}
	foundMissing := false
	for _, e := range signal.Evidence {
		if strings.Contains(e, "missing:") {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Errorf("expected evidence to include missing-file marker, got %v", signal.Evidence)
	}
}

func TestClassifyStallSignal_FallbackPlanRevisionWhenPlanAbsent(t *testing.T) {
	root := t.TempDir()
	signal := ClassifyStallSignal(state.StageFeatureDev, "stall kill threshold", root, 1)
	if signal.SignalType != "PLAN_REVISION_NEEDED" {
		t.Errorf("signal_type = %s, want PLAN_REVISION_NEEDED", signal.SignalType)
	}
}

func TestClassifyStallSignal_FallbackForNonRewindableStage(t *testing.T) {
	root := t.TempDir()
	// Even with a 5-file plan, a stall in pr-create can't trigger
	// COMPLEXITY_UNDERESTIMATED — the classifier short-circuits to
	// PLAN_REVISION_NEEDED for non-rewindable stages.
	writePlanningContext(t, root, 1, []string{"a.go", "b.go", "c.go", "d.go", "e.go"})
	signal := ClassifyStallSignal(state.StagePRCreate, "stall kill threshold", root, 1)
	if signal.SignalType != "PLAN_REVISION_NEEDED" {
		t.Errorf("signal_type = %s, want PLAN_REVISION_NEEDED for pr-create", signal.SignalType)
	}
}

func TestClassifyStallSignal_FallbackWhenSmallPlanAllFilesPresent(t *testing.T) {
	root := t.TempDir()
	// Create the files so missingFiles is empty; plan size below threshold.
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(""), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	writePlanningContext(t, root, 1, []string{"a.go", "b.go"})
	signal := ClassifyStallSignal(state.StageFeatureDev, "stall kill threshold", root, 1)
	if signal.SignalType != "PLAN_REVISION_NEEDED" {
		t.Errorf("signal_type = %s, want PLAN_REVISION_NEEDED (small plan, no missing files)", signal.SignalType)
	}
}

func TestWriteSyntheticFeedbackContext_RoundTrip(t *testing.T) {
	root := t.TempDir()
	signal := FeedbackSignal{
		SignalType:           "PLAN_REVISION_NEEDED",
		EmittedByStage:       string(state.StageFeatureDev),
		BacktrackTargetStage: string(state.StageFeaturePlanning),
		Rationale:            "synthesized by scheduler on stall-kill in feature-dev",
		Evidence:             []string{"stall-kill in feature-dev: heartbeat stall"},
		Severity:             "blocking",
	}
	if err := WriteSyntheticFeedbackContext(root, 42, signal); err != nil {
		t.Fatalf("WriteSyntheticFeedbackContext: %v", err)
	}

	// File round-trips through RetryEngine.EvaluateBacktrack — same path the
	// scheduler uses on stage-success.
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-42.json")
	if _, err := os.Stat(feedbackPath); err != nil {
		t.Fatalf("feedback file not written: %v", err)
	}

	engine := NewRetryEngine(DefaultRetryConfig())
	decision, err := engine.EvaluateBacktrack(feedbackPath)
	if err != nil {
		t.Fatalf("EvaluateBacktrack: %v", err)
	}
	if !decision.ShouldBacktrack {
		t.Errorf("expected backtrack, got %+v", decision)
	}
	if decision.TargetStage != state.StageFeaturePlanning {
		t.Errorf("target_stage = %s, want feature-planning", decision.TargetStage)
	}
	if decision.SignalType != "PLAN_REVISION_NEEDED" {
		t.Errorf("signal_type = %s, want PLAN_REVISION_NEEDED", decision.SignalType)
	}
}

// #3020 — flipped default to true so a stall-killed stage rewinds to planning
// instead of going straight to terminal failure. Original value was false.
func TestGetAdaptiveStallRecoveryEnabled_DefaultTrue(t *testing.T) {
	root := t.TempDir()
	if !GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected true for empty workspace (default flipped to true in #3020)")
	}
}

func TestGetAdaptiveStallRecoveryEnabled_EnvOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY", "true")
	if !GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected true when env is true")
	}

	t.Setenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY", "false")
	if GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected false when env is false")
	}
}

func TestGetAdaptiveStallRecoveryEnabled_YAMLTrue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := "pipeline:\n  adaptive_stall_recovery: true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Ensure env doesn't shadow the YAML value.
	t.Setenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY", "")
	if !GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected true from YAML")
	}
}

func TestGetAdaptiveStallRecoveryEnabled_YAMLFalseExplicit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	_ = os.MkdirAll(dir, 0755)
	yaml := "pipeline:\n  adaptive_stall_recovery: false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY", "")
	if GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected false from explicit YAML false")
	}
}

// #3020 — when the flag appears outside the `pipeline:` section we ignore it
// and fall back to the default. The default flipped to true in #3020, so the
// expected behaviour is now "default true, top-level flag ignored". To keep
// this test focused on the parser scope (not the default), use an explicit
// `pipeline.adaptive_stall_recovery: false` to override the default and
// assert the top-level value is still ignored.
func TestGetAdaptiveStallRecoveryEnabled_OutOfPipelineSection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	_ = os.MkdirAll(dir, 0755)
	// Top-level "true" must NOT be picked up; the in-section "false" wins.
	yaml := "adaptive_stall_recovery: true\npipeline:\n  adaptive_stall_recovery: false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY", "")
	if GetAdaptiveStallRecoveryEnabled(root) {
		t.Error("expected false from in-section value, top-level should be ignored")
	}
}
