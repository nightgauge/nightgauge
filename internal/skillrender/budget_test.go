package skillrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEstimate(t *testing.T) {
	cases := []struct {
		content string
		want    int
	}{
		{"", 0},
		{"abcd", 1},
		{strings.Repeat("x", 4000), 1000},
	}
	for _, tt := range cases {
		if got := Estimate(tt.content); got != tt.want {
			t.Errorf("Estimate(%d bytes) = %d, want %d", len(tt.content), got, tt.want)
		}
	}
}

func TestShare(t *testing.T) {
	// pr-merge is the heaviest measured stage: its share is always 1.0.
	if got := Share("pr-merge"); got != 1.0 {
		t.Errorf("Share(pr-merge) = %v, want 1.0", got)
	}
	// Every measured stage stays within (minShare, 1.0].
	for stage := range stageBaseTokens {
		got := Share(stage)
		if got < minShare || got > 1.0 {
			t.Errorf("Share(%s) = %v, want in [%v, 1.0]", stage, got, minShare)
		}
	}
	// An unmeasured stage gets the floor, not zero and not a guess.
	if got := Share("issue-refine"); got != minShare {
		t.Errorf("Share(issue-refine) = %v, want minShare %v", got, minShare)
	}
	if got := Share("not-a-real-stage"); got != minShare {
		t.Errorf("Share(not-a-real-stage) = %v, want minShare %v", got, minShare)
	}
}

func TestFit_UnknownWindowFailsOpen(t *testing.T) {
	for _, window := range []int{0, -1, -100} {
		got := Fit("pr-merge", strings.Repeat("x", 1_000_000), window)
		if !got.Fits {
			t.Errorf("Fit(window=%d) = %+v, want Fits=true (fail-open, ADR 023 §4)", window, got)
		}
		if got.EstimatedTokens != 0 || got.Budget != 0 || got.Window != 0 || got.Share != 0 {
			t.Errorf("Fit(window=%d) = %+v, want the zero FitResult", window, got)
		}
	}
}

func TestFit_SafetyMarginAppliedBeforeComparison(t *testing.T) {
	// Construct content whose raw estimate sits just under a budget, but whose
	// 1.15x-margined estimate pushes it over — proving the margin is applied,
	// not decoration on the struct.
	window := 1_000_000
	budget := int(float64(usableWindow(window)) * Share("feature-dev"))
	// raw estimate * 4 bytes/token, sized so raw < budget but raw*1.15 > budget.
	rawTokensJustUnderBudget := budget - 1
	rawTokensOverAfterMargin := int(float64(rawTokensJustUnderBudget) * safetyMargin)
	if rawTokensOverAfterMargin <= budget {
		t.Fatalf("test construction invalid: margined estimate %d does not exceed budget %d", rawTokensOverAfterMargin, budget)
	}
	content := strings.Repeat("x", rawTokensJustUnderBudget*4)
	got := Fit("feature-dev", content, window)
	if got.Fits {
		t.Errorf("Fit() = %+v, want Fits=false once the 1.15x safety margin is applied", got)
	}
}

// pr-merge golden cases, verified against the LIVE render of skills/nightgauge-pr-merge
// (not a synthetic fixture), matching the issue's own Verification section.
func TestFit_PrMergeGoldenCases(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	res, err := Render(Options{Stage: "pr-merge", SkillsRoots: []string{root}})
	if err != nil {
		t.Fatalf("render pr-merge: %v", err)
	}

	t.Run("fails at 32768", func(t *testing.T) {
		got := Fit("pr-merge", res.Content, 32768)
		if got.Fits {
			t.Errorf("Fit(pr-merge, 32768) = %+v, want Fits=false", got)
		}
		if got.EstimatedTokens < 15_000 {
			t.Errorf("EstimatedTokens = %d, want roughly the issue's measured ~21.7k tokens (at least 15k)", got.EstimatedTokens)
		}
	})

	t.Run("passes at 262144", func(t *testing.T) {
		got := Fit("pr-merge", res.Content, 262144)
		if !got.Fits {
			t.Errorf("Fit(pr-merge, 262144) = %+v, want Fits=true", got)
		}
	})
}

// TestFit_NeverRunsOnUnsetWindow guards the CLI contract from the other side:
// budget.go's own zero value must be inert so cmd/nightgauge/skill.go can gate
// the whole call behind `--context-window > 0` and get a byte-identical
// no-flag render for free, never a Fit side effect.
func TestFit_NeverRunsOnUnsetWindow(t *testing.T) {
	got := Fit("pr-merge", "anything", 0)
	if !got.Fits {
		t.Fatal("Fit with window=0 must fail open")
	}
}
