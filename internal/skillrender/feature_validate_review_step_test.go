package skillrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The self-critique this step replaced said "fresh eyes" and then told the
// validating agent to do the work itself (#2839). This reads the real files, so
// a return to that wording, or to a verdict the model records by hand, is red.
func reviewStep(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "### Step 2.7.3:")
	if start < 0 {
		t.Fatalf("%s has no Step 2.7.3", rel)
	}
	rest := text[start+1:]
	if end := strings.Index(rest, "\n### "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestValidateReviewStepIsIndependent(t *testing.T) {
	step := reviewStep(t, "skills/nightgauge-feature-validate/_includes/ci-and-knowledge.md")
	lower := strings.ToLower(step)
	if !strings.Contains(step, "Step 2.7.3: Independent Review Gate") {
		t.Error("the heading must say what the step does")
	}
	for _, banned := range []string{"yourself", "four-lens", "fresh eyes", "--gate adversarial-review"} {
		if strings.Contains(lower, banned) {
			t.Errorf("step regressed to a self-critique: contains %q", banned)
		}
	}
	for _, required := range []string{"review prepare", "review run", "review record", "non-forked subagent"} {
		if !strings.Contains(step, required) {
			t.Errorf("step must contain %q", required)
		}
	}
}

func TestValidateDirectivesNameTheIndependentReview(t *testing.T) {
	for _, rel := range []string{
		"skills/nightgauge-feature-validate/SKILL.md",
		"skills/nightgauge-feature-validate/_profiles/compact.md",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "adversarial-review `gate record-metric`") {
			t.Errorf("%s still says the verdict is recorded by hand", rel)
		}
		if strings.Contains(text, "adversarial **gate**") {
			t.Errorf("%s still says an adversarial judge closes validation", rel)
		}
		if !strings.Contains(text, "closed by the independent review") {
			t.Errorf("%s must say the independent review closes validation", rel)
		}
		if !strings.Contains(text, "Step 2.7.3") || !strings.Contains(text, "review record") {
			t.Errorf("%s must name Step 2.7.3 and review record", rel)
		}
	}
}

func TestFeatureDevDoesNotReviewItsOwnDiff(t *testing.T) {
	for _, rel := range []string{
		"skills/nightgauge-feature-dev/SKILL.md",
		"skills/nightgauge-feature-dev/_profiles/compact.md",
		"skills/nightgauge-feature-dev/_includes/review-and-correction.md",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"sequentially in this agent", "sequentially in this single agent", "**sequentially**"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s keeps the in-agent review floor: %q", rel, banned)
			}
		}
	}
}
