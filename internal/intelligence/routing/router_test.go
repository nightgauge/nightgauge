package routing

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
)

func TestRouter_LowComplexity_UsesHaiku(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 2})
	if rec.Model != ModelHaiku {
		t.Errorf("low complexity feature-dev model = %s, want %s", rec.Model, ModelHaiku)
	}
}

func TestRouter_MediumComplexity_UsesSonnet(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 5})
	if rec.Model != ModelSonnet {
		t.Errorf("med complexity feature-dev model = %s, want %s", rec.Model, ModelSonnet)
	}
}

func TestRouter_HighComplexity_UsesOpus(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 9})
	if rec.Model != ModelOpus {
		t.Errorf("high complexity feature-dev model = %s, want %s", rec.Model, ModelOpus)
	}
}

func TestRouter_LightweightStage_AlwaysHaiku(t *testing.T) {
	r := NewRouter(nil, "")
	for _, stage := range []string{"issue-pickup", "pr-create", "pr-merge"} {
		rec := r.Route(context.Background(), stage, complexity.Score{Value: 10})
		if rec.Model != ModelHaiku {
			t.Errorf("stage %s with high complexity = %s, want %s", stage, rec.Model, ModelHaiku)
		}
	}
}

func TestRouter_PlanningLowComplexity_UsesSonnet(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-planning", complexity.Score{Value: 2})
	if rec.Model != ModelSonnet {
		t.Errorf("low complexity planning = %s, want %s", rec.Model, ModelSonnet)
	}
}

func TestRouter_HasAlternatives(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 5})
	if len(rec.Alternatives) == 0 {
		t.Error("no alternatives provided")
	}
}

func TestRouter_CostEstimate(t *testing.T) {
	r := NewRouter(nil, "")
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 5})
	if rec.EstimatedCost <= 0 {
		t.Errorf("estimated cost = %f, want > 0", rec.EstimatedCost)
	}
	if rec.EstimatedTokens.Input <= 0 || rec.EstimatedTokens.Output <= 0 {
		t.Errorf("estimated tokens = %+v, want > 0", rec.EstimatedTokens)
	}
}

func TestEstimateTokens_ScalesWithComplexity(t *testing.T) {
	low := estimateTokens("feature-dev", 2)
	high := estimateTokens("feature-dev", 9)
	if high.Input <= low.Input {
		t.Errorf("high complexity tokens %d should exceed low %d", high.Input, low.Input)
	}
}

// writeModeFile writes a performance-mode.yaml to dir/.nightgauge/ and
// returns dir as the workspaceRoot.
func writeModeFile(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	ibDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(ibDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "mode: " + mode + "\n"
	if err := os.WriteFile(filepath.Join(ibDir, "performance-mode.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return dir
}

func TestRouter_EfficiencyMode_DevGetsSonnet(t *testing.T) {
	root := writeModeFile(t, "efficiency")
	r := NewRouter(nil, root)
	// feature-dev in efficiency mode → sonnet regardless of complexity
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 9})
	if rec.Model != ModelSonnet {
		t.Errorf("efficiency mode feature-dev = %s, want %s", rec.Model, ModelSonnet)
	}
}

func TestRouter_EfficiencyMode_LightweightGetsHaiku(t *testing.T) {
	root := writeModeFile(t, "efficiency")
	r := NewRouter(nil, root)
	for _, stage := range []string{"issue-pickup", "pr-create", "pr-merge"} {
		rec := r.Route(context.Background(), stage, complexity.Score{Value: 5})
		if rec.Model != ModelHaiku {
			t.Errorf("efficiency mode %s = %s, want %s", stage, rec.Model, ModelHaiku)
		}
	}
}

func TestRouter_MaximumMode_AllStagesGetOpus(t *testing.T) {
	root := writeModeFile(t, "maximum")
	r := NewRouter(nil, root)
	for _, stage := range []string{"issue-pickup", "feature-planning", "feature-dev", "feature-validate", "pr-create", "pr-merge"} {
		rec := r.Route(context.Background(), stage, complexity.Score{Value: 2})
		if rec.Model != ModelOpus {
			t.Errorf("maximum mode %s = %s, want %s", stage, rec.Model, ModelOpus)
		}
	}
}

func TestRouter_ElevatedMode_NoOverride(t *testing.T) {
	root := writeModeFile(t, "elevated")
	r := NewRouter(nil, root)
	// elevated = same as no file — complexity-based selection applies
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 9})
	if rec.Model != ModelOpus {
		t.Errorf("elevated mode high complexity feature-dev = %s, want %s", rec.Model, ModelOpus)
	}
}

func TestRouter_MissingFile_DefaultsToElevated(t *testing.T) {
	// Empty dir — no performance-mode.yaml
	root := t.TempDir()
	r := NewRouter(nil, root)
	// Without file: high complexity feature-dev → opus (elevated behavior)
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 9})
	if rec.Model != ModelOpus {
		t.Errorf("no file high complexity feature-dev = %s, want %s", rec.Model, ModelOpus)
	}
}

func TestRouter_EnvVarOverride_TakesPrecedence(t *testing.T) {
	// File says efficiency, env var says maximum — env var wins
	root := writeModeFile(t, "efficiency")
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
	r := NewRouter(nil, root)
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 2})
	if rec.Model != ModelOpus {
		t.Errorf("env var override: feature-dev = %s, want %s", rec.Model, ModelOpus)
	}
}

func TestRouter_FrontierMode_ReasoningStagesGetFable(t *testing.T) {
	root := writeModeFile(t, "frontier")
	r := NewRouter(nil, root)
	for _, stage := range []string{"feature-planning", "feature-dev", "feature-validate"} {
		// Even at trivial complexity, frontier pins the reasoning stages to Fable.
		rec := r.Route(context.Background(), stage, complexity.Score{Value: 2})
		if rec.Model != ModelFable {
			t.Errorf("frontier mode %s = %s, want %s", stage, rec.Model, ModelFable)
		}
	}
}

func TestRouter_FrontierMode_MechanicalStagesGetHaiku(t *testing.T) {
	root := writeModeFile(t, "frontier")
	r := NewRouter(nil, root)
	// Mechanical stages stay on Haiku — frontier does not pay Fable rates for plumbing.
	for _, stage := range []string{"issue-pickup", "pr-create", "pr-merge"} {
		rec := r.Route(context.Background(), stage, complexity.Score{Value: 10})
		if rec.Model != ModelHaiku {
			t.Errorf("frontier mode %s = %s, want %s", stage, rec.Model, ModelHaiku)
		}
	}
}

// TestRouter_AutomaticRouting_NeverSelectsFable is the load-bearing invariant:
// without an explicit frontier mode, no stage at any complexity may resolve to
// the premium Fable tier. Automatic routing caps at Opus.
func TestRouter_AutomaticRouting_NeverSelectsFable(t *testing.T) {
	stages := []string{"issue-pickup", "feature-planning", "feature-dev", "feature-validate", "pr-create", "pr-merge"}
	for _, mode := range []string{"", "efficiency", "elevated", "maximum"} {
		root := ""
		if mode != "" {
			root = writeModeFile(t, mode)
		}
		r := NewRouter(nil, root)
		for _, stage := range stages {
			for c := 1; c <= 10; c++ {
				rec := r.Route(context.Background(), stage, complexity.Score{Value: c})
				if rec.Model == ModelFable {
					t.Errorf("mode=%q stage=%s complexity=%d auto-selected Fable (must cap at Opus)", mode, stage, c)
				}
			}
		}
	}
}

func TestModelPricing_FableIsTwiceOpus(t *testing.T) {
	oi, oo := modelPricing(ModelOpus)
	fi, fo := modelPricing(ModelFable)
	if oi != 5.00 || oo != 25.00 {
		t.Errorf("opus pricing = %.2f/%.2f, want 5.00/25.00", oi, oo)
	}
	if fi != 10.00 || fo != 50.00 {
		t.Errorf("fable pricing = %.2f/%.2f, want 10.00/50.00", fi, fo)
	}
	if fi != oi*2 || fo != oo*2 {
		t.Errorf("fable should be 2× opus: fable=%.2f/%.2f opus=%.2f/%.2f", fi, fo, oi, oo)
	}
}

func TestRouter_MaximumMode_ReasoningAnnotated(t *testing.T) {
	root := writeModeFile(t, "maximum")
	r := NewRouter(nil, root)
	rec := r.Route(context.Background(), "feature-dev", complexity.Score{Value: 2})
	if rec.Model != ModelOpus {
		t.Fatalf("maximum mode model = %s, want %s", rec.Model, ModelOpus)
	}
	if len(rec.Reasoning) == 0 {
		t.Error("reasoning should not be empty")
	}
	// Reasoning must mention the active mode
	want := "(performance-mode: maximum)"
	if !containsSubstr(rec.Reasoning, want) {
		t.Errorf("reasoning %q missing %q", rec.Reasoning, want)
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
