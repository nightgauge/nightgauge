package execution

import "testing"

// The defect (#1924): a run that reports phases 1 and 9 was in 2..8 and said
// nothing, and those seven phases stayed invisible until the end-of-stage
// back-fill stamped them `unreported`. Gap-fill reports them live instead.
func TestAdvanceReportsThePhasesItJumpedOver(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	if _, ok := inf.Start(); !ok {
		t.Fatal("Start() did not emit the stage's first phase")
	}

	// A Read advances to 1; an edit under a non-pipeline path advances to 8.
	if _, passed, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "PLAN.md"}); !ok {
		t.Fatal("Read did not advance the cursor")
	} else if len(passed) != 0 {
		t.Fatalf("adjacent advance 0 -> 1 reported %d passed phases, want 0", len(passed))
	}

	_, passed, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/app.go"})
	if !ok {
		t.Fatal("Write did not advance the cursor")
	}
	if len(passed) != 6 {
		t.Fatalf("advance 1 -> 8 reported %d passed phases, want 6 (indices 2..7)", len(passed))
	}
	for i, m := range passed {
		wantIndex := 2 + i
		if m.Index != wantIndex {
			t.Errorf("passed[%d].Index = %d, want %d", i, m.Index, wantIndex)
		}
		if m.Name == "" {
			t.Errorf("passed[%d] has no name; the registry should have supplied one", i)
		}
		if m.Stage != "feature-dev" {
			t.Errorf("passed[%d].Stage = %q, want feature-dev", i, m.Stage)
		}
	}
}

// A real marker closes a gap too: a skill that printf's 0 then 6 was in 1..5.
func TestRealMarkerReportsTheGapItRevealed(t *testing.T) {
	inf := NewPhaseInferer("feature-planning")
	if _, ok := inf.Start(); !ok {
		t.Fatal("Start() did not emit")
	}
	passed := inf.ObserveRealMarker(6)
	if len(passed) != 5 {
		t.Fatalf("real marker 0 -> 6 reported %d passed phases, want 5 (indices 1..5)", len(passed))
	}
	// A marker that does not advance reveals nothing.
	if got := inf.ObserveRealMarker(6); len(got) != 0 {
		t.Errorf("re-observing the same marker reported %d passed phases, want 0", len(got))
	}
	if got := inf.ObserveRealMarker(3); len(got) != 0 {
		t.Errorf("a regressing marker reported %d passed phases, want 0", len(got))
	}
}

// Gap-fill derives from ORDERING, so it must work for a stage that has a phase
// table and NO inference rules. Requiring both is what kept it switched off.
func TestGapFillWorksWithoutInferenceRules(t *testing.T) {
	inf := NewPhaseInferer("feature-validate")
	inf.rules = nil // a stage the rule table has not been taught
	if !inf.enabled {
		t.Fatal("a stage with a phase table but no rules must still gap-fill")
	}
	if passed := inf.ObserveRealMarker(4); len(passed) != 4 {
		t.Fatalf("reported %d passed phases, want 4 (indices 0..3)", len(passed))
	}
}
