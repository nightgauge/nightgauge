package execution

import "testing"

func TestPhaseInferer_FeatureDevStartsAtValidateEnvironment(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	if !inf.enabled {
		t.Fatal("expected feature-dev inference to be enabled")
	}
	m, ok := inf.Start()
	if !ok {
		t.Fatal("expected a start marker")
	}
	if m.Name != "validate-environment" || m.Index != 0 || m.Total != 18 || m.Stage != "feature-dev" {
		t.Fatalf("unexpected start marker: %+v", m)
	}
}

func TestPhaseInferer_AdvancesThroughWaypoints(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()

	cases := []struct {
		tool  string
		input map[string]any
		want  string
		index int
	}{
		{"Read", map[string]any{"file_path": "PLAN.md"}, "read-planning-context", 1},
		{"Write", map[string]any{"file_path": "src/feature.ts"}, "implementation", 8},
		{"Bash", map[string]any{"command": "go test ./..."}, "testing", 9},
		{"Write", map[string]any{"file_path": ".nightgauge/pipeline/dev-42.json"}, "write-dev-context", 14},
		{"Bash", map[string]any{"command": "nightgauge project move-status 42"}, "sync-project-status", 15},
	}
	for _, c := range cases {
		m, _, ok := inf.ObserveToolUse(c.tool, c.input)
		if !ok {
			t.Fatalf("expected advancement for %s %v", c.tool, c.input)
		}
		if m.Name != c.want || m.Index != c.index {
			t.Fatalf("got %s/%d, want %s/%d", m.Name, m.Index, c.want, c.index)
		}
	}
}

func TestPhaseInferer_Monotonic(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); !ok {
		t.Fatal("expected implementation advancement")
	}
	// A later context read must not regress the phase.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "src/b.ts"}); ok {
		t.Fatal("expected no regression for a late read")
	}
	// Re-editing source must not re-emit implementation.
	if _, _, ok := inf.ObserveToolUse("Edit", map[string]any{"file_path": "src/c.ts"}); ok {
		t.Fatal("expected no duplicate implementation emission")
	}
}

func TestPhaseInferer_DevContextWriteIsNotImplementation(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	m, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": ".nightgauge/pipeline/dev-42.json"})
	if !ok {
		t.Fatal("expected advancement")
	}
	if m.Name != "write-dev-context" || m.Index != 14 {
		t.Fatalf("dev-context write should map to write-dev-context, got %s/%d", m.Name, m.Index)
	}
}

func TestPhaseInferer_RealMarkerWins(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	inf.ObserveRealMarker(11) // quality-review reached via a genuine marker
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("inferred implementation must not regress past a real marker")
	}
}

// TestPhaseInferer_DisabledForSelfReportingStages uses pr-merge, which really
// does self-report: its phases come from the deterministic Go reporter as
// direct function calls, so there is nothing for inference to add.
//
// It used to use feature-validate, on the premise that feature-validate
// self-reports. It does not (#1850). It is an LLM stage whose 23 markers are
// standalone printfs the model skips for exactly feature-dev's reason, and the
// observed run reported 0 of 23 across four minutes and $0.68 while exiting 0.
func TestPhaseInferer_DisabledForSelfReportingStages(t *testing.T) {
	inf := NewPhaseInferer("pr-merge")
	if inf.enabled {
		t.Fatal("pr-merge reports phases from the deterministic runner and must not infer them")
	}
	if _, ok := inf.Start(); ok {
		t.Fatal("disabled inferer must not emit a start marker")
	}
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("disabled inferer must not emit from tool use")
	}
}

// TestPhaseInferer_FeatureValidateInfersFromItsRealWork is the #1850 half: the
// worst-reported stage in the observed run now has the same fallback
// feature-dev already had.
func TestPhaseInferer_FeatureValidateInfersFromItsRealWork(t *testing.T) {
	inf := NewPhaseInferer("feature-validate")
	if !inf.enabled {
		t.Fatal("feature-validate must infer phases — it does not reliably emit markers")
	}

	m, ok := inf.Start()
	if !ok || m.Index != 0 || m.Total != 23 {
		t.Fatalf("Start should open validate-environment of 23, got %+v ok=%v", m, ok)
	}

	if m, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "dev-42.json"}); !ok ||
		m.Name != "read-dev-context" {
		t.Fatalf("a read should map to read-dev-context, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Bash", map[string]any{"command": "go test ./..."}); !ok ||
		m.Name != "run-tests" {
		t.Fatalf("a test command should map to run-tests, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Bash", map[string]any{"command": "git push -u origin HEAD"}); !ok ||
		m.Name != "commit-and-push" {
		t.Fatalf("a branch push should map to commit-and-push, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Write", map[string]any{
		"file_path": ".nightgauge/pipeline/validate-42.json",
	}); !ok || m.Name != "write-validate-context" {
		t.Fatalf("the validate-context write should map to write-validate-context, got %+v ok=%v", m, ok)
	}

	// Monotonic, exactly as for feature-dev: a later read must not drag the
	// cursor back to an earlier phase.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("a read after write-validate-context must not regress the cursor")
	}
}

func TestPhaseInferer_FeaturePlanningWaypoints(t *testing.T) {
	inf := NewPhaseInferer("feature-planning")
	if !inf.enabled {
		t.Fatal("expected feature-planning inference to be enabled")
	}
	m, ok := inf.Start()
	if !ok || m.Name != "feedback-context-check" || m.Index != 0 || m.Total != 14 {
		t.Fatalf("unexpected start marker: %+v ok=%v", m, ok)
	}

	cases := []struct {
		tool  string
		input map[string]any
		want  string
		index int
	}{
		{"Grep", map[string]any{"pattern": "x", "path": "docs/"}, "documentation-analysis", 6},
		{"Write", map[string]any{"file_path": ".nightgauge/plans/6-flutter-ia-nav.md"}, "produce-plan", 9},
		{"Write", map[string]any{"file_path": ".nightgauge/pipeline/planning-6.json"}, "write-planning-context", 10},
	}
	for _, c := range cases {
		m, _, ok := inf.ObserveToolUse(c.tool, c.input)
		if !ok {
			t.Fatalf("expected advancement for %s %v", c.tool, c.input)
		}
		if m.Name != c.want || m.Index != c.index {
			t.Fatalf("got %s/%d, want %s/%d", m.Name, m.Index, c.want, c.index)
		}
	}

	// A late read must not regress past produce-plan/write-planning-context.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "docs/ARCHITECTURE.md"}); ok {
		t.Fatal("expected no regression for a late planning read")
	}
}

func TestPhaseInferer_FeaturePlanningRealMarkerWins(t *testing.T) {
	inf := NewPhaseInferer("feature-planning")
	inf.Start()
	inf.ObserveRealMarker(10) // write-planning-context reached via a genuine marker
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "docs/x.md"}); ok {
		t.Fatal("inferred documentation-analysis must not regress past a real marker")
	}
}

func TestExtractToolUses(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Write","input":{"file_path":"src/x.ts"}}]}}`
	uses := extractToolUses(line)
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(uses))
	}
	if uses[0].Name != "Write" || inputStr(uses[0].Input, "file_path") != "src/x.ts" {
		t.Fatalf("unexpected tool use: %+v", uses[0])
	}

	// Non-assistant lines yield nothing.
	if got := extractToolUses(`{"type":"user","message":{}}`); got != nil {
		t.Fatalf("expected nil for non-assistant line, got %+v", got)
	}
	// Malformed JSON yields nothing.
	if got := extractToolUses("not json"); got != nil {
		t.Fatalf("expected nil for malformed line, got %+v", got)
	}
}
