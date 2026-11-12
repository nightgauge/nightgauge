package skillrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// section returns the text of the step whose heading starts with heading, up to
// the next heading of any level three or higher.
func section(t *testing.T, text, heading string) string {
	t.Helper()
	start := strings.Index(text, heading)
	if start < 0 {
		t.Fatalf("no %q section", heading)
	}
	rest := text[start+len(heading):]
	if end := strings.Index(rest, "\n### "); end >= 0 {
		rest = rest[:end]
	}
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// feature-validate ran the full gate before the independent review, so every
// catch fix voided it and the agent killed and restarted it (#3207). The full
// gate now runs once, after the final review round; rounds run scoped checks.
func TestValidateRunsTheFullGateOnceAfterTheReview(t *testing.T) {
	text := readRepoFile(t, "skills/nightgauge-feature-validate/_includes/ci-and-knowledge.md")

	scoped := section(t, text, "### Step 2.5.0a:")
	for _, want := range []string{"build", "vet", "tests of the changed packages", "adversarial_review.enabled", "Step 2.7.4"} {
		if !strings.Contains(scoped, want) {
			t.Errorf("Step 2.5.0a must name %q", want)
		}
	}
	if strings.Contains(scoped, "go test ./...") {
		t.Error("Step 2.5.0a runs every package's tests, not the changed packages'")
	}
	// A repository that is not Go gets its own scoped form, never the full gate.
	for _, want := range []string{"npm run -w \"$WS\" test", "workspaces only",
		"neither Go nor npm", "Never run the full gate between review rounds"} {
		if !strings.Contains(scoped, want) {
			t.Errorf("Step 2.5.0a's non-Go form must say %q", want)
		}
	}

	review := section(t, text, "### Step 2.7.3:")
	for _, want := range []string{"scoped checks", "never\n  the full gate", "never kill a running gate"} {
		if !strings.Contains(review, want) {
			t.Errorf("Step 2.7.3 must say %q", want)
		}
	}
	if strings.Contains(review, "re-run ALL") || strings.Contains(review, "re-run the checks they touch") {
		t.Error("Step 2.7.3 still re-runs every check after a catch")
	}

	full := section(t, text, "### Step 2.7.4:")
	for _, want := range []string{"once", "after Step 2.7.3", "before the pre-push gate", "nightgauge ci gate-run", "nightgauge ci gate-count"} {
		if !strings.Contains(full, want) {
			t.Errorf("Step 2.7.4 must say %q", want)
		}
	}
	if strings.Index(text, "### Step 2.7.4:") < strings.Index(text, "### Step 2.7.3:") {
		t.Error("Step 2.7.4 must follow the review step")
	}

	run := section(t, text, "### Step 2.5.2:")
	for _, want := range []string{"`75`", "`76`", "superseded", "never start another"} {
		if !strings.Contains(run, want) {
			t.Errorf("Step 2.5.2 must document %q", want)
		}
	}
}

func TestValidateDirectivesNameTheOneFullGate(t *testing.T) {
	for _, rel := range []string{
		"skills/nightgauge-feature-validate/SKILL.md",
		"skills/nightgauge-feature-validate/_profiles/compact.md",
	} {
		phase := section(t, readRepoFile(t, rel), "### Phase 2.5:")
		for _, want := range []string{"scoped checks", "Step 2.7.4", "never the full gate", "after the final review round"} {
			if !strings.Contains(phase, want) {
				t.Errorf("%s Phase 2.5 must say %q", rel, want)
			}
		}
	}
}
