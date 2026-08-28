package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// writePipelineFile writes a .nightgauge/pipeline/<name> file under root.
func writePipelineFile(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// The complete report the reconciler emits, and the truncated block the model
// wrote on the live run — the three scalars its shell phase echoes, and none of
// the three the schema requires.
const acReportJSON = `{
  "schema_version": "1.0",
  "issue_number": 228,
  "main_sha": "9f8e7d6c5b4a",
  "evaluated_at": "2026-08-27T00:05:41Z",
  "acceptance_criteria": [
    {"index": 0, "text": "a", "classification": "undetectable"},
    {"index": 1, "text": "b", "classification": "undetectable"},
    {"index": 2, "text": "c", "classification": "undetectable"}
  ],
  "aggregate_status": "undetectable",
  "suggested_route": {"approach": "standard", "focus_acs": [0,1,2], "rationale": "x"}
}`

const truncatedPlanningJSON = `{
  "schema_version": "1.1",
  "issue_number": 228,
  "approach": "standard",
  "ac_reconcile": {
    "schema_version": "1.0",
    "issue_number": 228,
    "aggregate_status": "undetectable",
    "suggested_route": {"approach": "standard", "focus_acs": [], "rationale": "x"}
  }
}`

// TestValidateStageOutput_SplicesACReconcile pins the Go-path half of #1011.
//
// It drives validateStageOutput — the real post-stage step this path runs —
// rather than calling spliceACReconcile directly, because the defect is that
// nothing on this path spliced at all. A test that called the splice function
// would prove the logic and leave the wiring exactly as broken as it was.
func TestValidateStageOutput_SplicesACReconcile(t *testing.T) {
	root := t.TempDir()
	writePipelineFile(t, root, "ac-reconcile-228.json", acReportJSON)
	planningPath := writePipelineFile(t, root, "planning-228.json", truncatedPlanningJSON)

	if err := validateStageOutput(state.StageFeaturePlanning, root, 228); err != nil {
		t.Fatalf("validateStageOutput: %v", err)
	}

	raw, err := os.ReadFile(planningPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("planning context is not valid JSON after splice: %v", err)
	}
	block, ok := got["ac_reconcile"].(map[string]any)
	if !ok {
		t.Fatalf("ac_reconcile = %v, want an object", got["ac_reconcile"])
	}

	// The three fields the model dropped — the whole defect.
	acs, ok := block["acceptance_criteria"].([]any)
	if !ok || len(acs) != 3 {
		t.Errorf("acceptance_criteria = %v, want 3 entries", block["acceptance_criteria"])
	}
	if block["main_sha"] != "9f8e7d6c5b4a" {
		t.Errorf("main_sha = %v, want the report's value", block["main_sha"])
	}
	if block["evaluated_at"] != "2026-08-27T00:05:41Z" {
		t.Errorf("evaluated_at = %v, want the report's value", block["evaluated_at"])
	}

	// The report wins outright: the model's stale empty focus list is gone.
	route, _ := block["suggested_route"].(map[string]any)
	focus, _ := route["focus_acs"].([]any)
	if len(focus) != 3 {
		t.Errorf("focus_acs = %v, want the report's [0 1 2] — the splice must not merge", focus)
	}
}

// TestValidateStageOutput_NoReportLeavesPlanningIntact pins the degradation.
// A stage that succeeded must not be failed, and the planning file must not be
// corrupted, just because the reconciler produced nothing.
func TestValidateStageOutput_NoReportLeavesPlanningIntact(t *testing.T) {
	root := t.TempDir()
	planningPath := writePipelineFile(t, root, "planning-228.json", truncatedPlanningJSON)

	if err := validateStageOutput(state.StageFeaturePlanning, root, 228); err != nil {
		t.Fatalf("validateStageOutput: %v", err)
	}

	raw, err := os.ReadFile(planningPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != truncatedPlanningJSON {
		t.Errorf("planning context was rewritten with no report present:\n%s", raw)
	}
}

// TestValidateStageOutput_OtherStagesAreUntouched keeps the splice scoped.
func TestValidateStageOutput_OtherStagesAreUntouched(t *testing.T) {
	root := t.TempDir()
	writePipelineFile(t, root, "ac-reconcile-228.json", acReportJSON)
	prPath := writePipelineFile(t, root, "pr-228.json", `{"issue_number":228,"pr_number":1}`)

	if err := validateStageOutput(state.StagePRCreate, root, 228); err != nil {
		t.Fatalf("validateStageOutput: %v", err)
	}
	raw, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"issue_number":228,"pr_number":1}` {
		t.Errorf("pr context was rewritten: %s", raw)
	}
}
