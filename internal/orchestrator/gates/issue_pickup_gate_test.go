package gates

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIssuePickupGate_Pass(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "issue-42.json"), map[string]any{
		"issue_number": 42,
		"branch":       "feat/42-test",
	})
	gr := IssuePickupGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; got reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	if gr.GateName != "issue-pickup" {
		t.Errorf("GateName = %q", gr.GateName)
	}
}

func TestIssuePickupGate_Fail_ContextMissing(t *testing.T) {
	gr := IssuePickupGate{}.Verify(context.Background(), 42, t.TempDir())
	if gr.Passed {
		t.Fatalf("expected fail when context missing")
	}
}

// TestIssuePickupGate_SkillSaidSuccessButNoBranch covers the canonical
// "skill reported success but didn't actually do the work" scenario: the
// context file exists and parses, but the branch field is empty — meaning
// the skill emitted a stub without resolving the feature branch.
func TestIssuePickupGate_SkillSaidSuccessButNoBranch(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "issue-42.json"), map[string]any{
		"issue_number": 42,
		"branch":       "",
	})
	gr := IssuePickupGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when branch empty; got pass")
	}
	if gr.Reason == "" {
		t.Errorf("expected non-empty reason")
	}
}

func TestIssuePickupGate_Fail_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "issue-42.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gr := IssuePickupGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail on malformed JSON")
	}
}
