package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── #1662: feature-dev's compact render profile ───────────────────────────
//
// feature-dev runs the longest session, so every token in its prompt is paid
// again on every turn. These tests pin the compact profile
// (skills/nightgauge-feature-dev/_profiles/compact.md) to the issue's
// Verification list: it fits the ADR-023 share at a 32768-token window,
// keeps every must-survive marker of the full render, the `dev-{N}.json`
// handoff contract, the no-commit rule and the stop-and-declare rule
// verbatim, carries no instruction that contradicts #1651's single-step
// scope preamble, and every Read directive it carries is an absolute,
// existing path under the skill root or its _shared sibling. Shared helpers
// live in compact_issue_pickup_test.go.

// The no-commit rule (AGENTS.md #1608) as it reads in the base SKILL.md.
const fdNoCommitRule = "No commit or push in feature-dev."

// The stop-and-declare rule for work outside the plan (Phase 1 Backstop).
const fdStopAndDeclareRule = "Stop here and declare it.** Do not implement, do not commit"

func TestCompactFeatureDev_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	got := fitCompact(t, "feature-dev", compact.Content)
	if !got.Fits {
		t.Errorf("Fit(feature-dev compact, %d) = %+v, want Fits=true", compactTestWindow, got)
	}
}

// TestCompactFeatureDev_BudgetCatchesInlinedIncludes proves the fit check
// above is sensitive: appending the text of one large include (implementation
// and testing, the stage's biggest single supporting file) takes the render
// back over the share.
func TestCompactFeatureDev_BudgetCatchesInlinedIncludes(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	include := filepath.Join(filepath.Dir(compact.SkillPath), "_includes", "implementation-and-testing.md")
	data, err := os.ReadFile(include)
	if err != nil {
		t.Fatalf("read %s: %v", include, err)
	}
	inflated := compact.Content + "\n" + string(data)
	if got := fitCompact(t, "feature-dev", inflated); got.Fits {
		t.Errorf("compact + implementation-and-testing.md still fits (%+v); the budget check is not discriminating", got)
	}
}

func TestCompactFeatureDev_MarkerParity(t *testing.T) {
	full, compact := renderStagePair(t, "feature-dev")
	for _, m := range missingMarkers(full.Content, compact.Content) {
		t.Errorf("compact render is missing must-survive marker: %s", m)
	}
}

// TestCompactFeatureDev_KeepsContractAndRules names the elements #1662 lists
// as retained, so a regression reports which one went, not just that the
// marker sets differ.
func TestCompactFeatureDev_KeepsContractAndRules(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	for _, want := range []string{
		fdNoCommitRule,
		fdStopAndDeclareRule,
		// The dev-{N}.json handoff contract fields the derived-handoff gate
		// (#1076) reconciles.
		"`.nightgauge/pipeline/dev-{N}.json`",
		"files_changed",
		"tests_status",
		// Build-before-tests and tests-alongside discipline.
		"Build before tests.",
		"UNOBSERVED-MECHANISM RULE",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

// TestCompactFeatureDev_RulesAreInBothRenders proves these are a trim of the
// base skill, not something the compact profile is stricter about than full.
func TestCompactFeatureDev_RulesAreInBothRenders(t *testing.T) {
	full, compact := renderStagePair(t, "feature-dev")
	for _, want := range []string{fdNoCommitRule, fdStopAndDeclareRule} {
		if !strings.Contains(full.Content, want) {
			t.Errorf("full render is missing %q — the compact profile cannot keep what the base does not have", want)
		}
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

// allStepsRE is the #1662 AC5 guard: no directive tells the model to
// implement every remaining plan step, which would contradict #1651's
// single-step scope preamble injected ahead of the render.
var allStepsRE = regexp.MustCompile(`(?i)all (remaining )?(plan )?steps|complete the entire plan`)

// TestCompactFeatureDev_NoAllStepsDirective is AC5, mechanically: rendering
// the compact profile (the shape #1651's scheduler prepends its step preamble
// to) carries no instruction that contradicts a single-step scope.
func TestCompactFeatureDev_NoAllStepsDirective(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	if m := allStepsRE.FindString(compact.Content); m != "" {
		t.Errorf("compact render carries an all-steps directive (%q), which contradicts #1651's single-step scope preamble", m)
	}
}

// TestCompactFeatureDev_NoAllStepsDirective_CatchesAddition proves the guard
// above is sensitive, not vacuously green.
func TestCompactFeatureDev_NoAllStepsDirective_CatchesAddition(t *testing.T) {
	if m := allStepsRE.FindString("Implement all remaining plan steps before finishing."); m == "" {
		t.Fatal("expected the regex to catch an injected all-steps directive")
	}
	if m := allStepsRE.FindString("Complete the entire plan in this turn."); m == "" {
		t.Fatal("expected the regex to catch 'complete the entire plan'")
	}
}

func TestCompactFeatureDev_ReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	assertCompactReadDirectives(t, compact)
}

func TestCompactFeatureDev_CodeBlocksAreVerbatim(t *testing.T) {
	assertProfileCodeBlocksAreVerbatim(t, "feature-dev")
}
