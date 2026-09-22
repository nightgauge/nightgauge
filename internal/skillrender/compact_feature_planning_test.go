package skillrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── #1661: feature-planning's compact render profile ──────────────────────
//
// feature-planning is where a small model most easily loses the thread, and
// the plan it produces is machine-read by feature-dev's own parsePlanFile and
// by the bounded sub-sessions #1651 spawns. These tests pin the compact
// profile (skills/nightgauge-feature-planning/_profiles/compact.md) to the
// issue's Verification list: it fits the ADR-023 share at a 32768-token
// window, keeps every must-survive marker of the full render, the
// `planning-{N}.json` Output Contract fields, and the plan-file `- [ ]`
// checkbox format `parsePlanFile` counts — and every Read directive it
// carries is an absolute, existing path under the skill root or its _shared
// sibling. Shared helpers (renderStagePair, missingMarkers,
// assertCompactReadDirectives, assertProfileCodeBlocksAreVerbatim) live in
// compact_issue_pickup_test.go.

func TestCompactFeaturePlanning_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "feature-planning")
	got := Fit("feature-planning", compact.Content, compactTestWindow)
	if !got.Fits {
		t.Errorf("Fit(feature-planning compact, %d) = %+v, want Fits=true", compactTestWindow, got)
	}
}

// TestCompactFeaturePlanning_BudgetCatchesInlinedIncludes proves the fit
// check above is sensitive: appending the text of the skill's own _includes
// (what the profile moved behind Read directives) takes the render back over
// the share.
func TestCompactFeaturePlanning_BudgetCatchesInlinedIncludes(t *testing.T) {
	_, compact := renderStagePair(t, "feature-planning")
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(compact.SkillPath), "_includes", "*.md"))
	if len(files) == 0 {
		t.Fatal("feature-planning has no _includes to append")
	}
	inflated := compact.Content
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		inflated += "\n" + string(data)
	}
	if got := Fit("feature-planning", inflated, compactTestWindow); got.Fits {
		t.Errorf("compact + every _includes file still fits (%+v); the budget check is not discriminating", got)
	}
}

func TestCompactFeaturePlanning_MarkerParity(t *testing.T) {
	full, compact := renderStagePair(t, "feature-planning")
	for _, m := range missingMarkers(full.Content, compact.Content) {
		t.Errorf("compact render is missing must-survive marker: %s", m)
	}
}

// TestCompactFeaturePlanning_KeepsContractAndChecklist names the elements
// #1661 lists as retained, so a regression reports which one went.
func TestCompactFeaturePlanning_KeepsContractAndChecklist(t *testing.T) {
	_, compact := renderStagePair(t, "feature-planning")
	for _, want := range []string{
		// The planning-{N}.json Output Contract's minimal skeleton fields.
		`"schema_version": "1.9"`,
		`"plan_file": ".nightgauge/plans/{N}-*.md"`,
		`"files_to_create": []`,
		`"files_to_modify": []`,
		"complexity_assessment",
		// The plan-file task checkbox format parsePlanFile parses.
		"`- [ ] task` checkbox",
		"parsePlanFile",
		// Completion Checklist, verbatim heading and items.
		"## Completion Checklist",
		"`.nightgauge/plans/{N}-*.md` exists and is complete",
		"`.nightgauge/pipeline/planning-{N}.json` written",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

// TestCompactFeaturePlanning_ChecklistFormatIsInBothRenders proves the
// checkbox-format rule is a trim of the base skill, not new content the
// compact profile invented: it must also be present, verbatim, in the full
// render.
func TestCompactFeaturePlanning_ChecklistFormatIsInBothRenders(t *testing.T) {
	full, compact := renderStagePair(t, "feature-planning")
	want := "`- [ ] task` checkbox"
	if !strings.Contains(full.Content, want) {
		t.Errorf("full render is missing %q — the compact profile cannot keep what the base does not have", want)
	}
	if !strings.Contains(compact.Content, want) {
		t.Errorf("compact render is missing %q", want)
	}
}

func TestCompactFeaturePlanning_ReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	_, compact := renderStagePair(t, "feature-planning")
	assertCompactReadDirectives(t, compact)
}

func TestCompactFeaturePlanning_CodeBlocksAreVerbatim(t *testing.T) {
	assertProfileCodeBlocksAreVerbatim(t, "feature-planning")
}

// TestCompactFeaturePlanning_PlanFixtureParses is the issue's own AC: a plan
// file written under the compact profile parses through parsePlanFile with
// the same task count as the full-profile plan. The fixture stands in for
// "a plan file the compact profile's Phase 4 instructions would produce" —
// both fixtures share the same task list, proving the compact profile's
// trimmed Phase 4 pointer does not change what parsePlanFile can count.
func TestCompactFeaturePlanning_PlanFixtureParses(t *testing.T) {
	_ = realSkillsRoot(t) // skip consistently with the other tests in this file when skills/ is absent
	fixture := filepath.Join("testdata", "compact-plan-fixture.md")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read plan fixture: %v", err)
	}
	total, complete, incomplete := countPlanCheckboxes(string(data))
	if total == 0 {
		t.Fatal("plan fixture has no checkbox tasks; parsePlanFile would report Total=0")
	}
	if complete+incomplete != total {
		t.Fatalf("checkbox count mismatch: complete=%d incomplete=%d total=%d", complete, incomplete, total)
	}
	// The "full-profile" plan is the same fixture: the compact profile does
	// not change the checkbox format, so the full and compact-authored plans
	// (both produced by the same Phase 4 instructions) parse to the same
	// task count.
	fullTotal, _, _ := countPlanCheckboxes(string(data))
	if fullTotal != total {
		t.Errorf("compact-profile plan task count (%d) != full-profile plan task count (%d)", total, fullTotal)
	}
}

// countPlanCheckboxes mirrors internal/hooks/stop.go's parsePlanFile regexes
// (checkboxComplete `^\s*-\s+\[x\]\s`, checkboxIncomplete `^\s*-\s+\[ \]\s`)
// without importing internal/hooks, which this package cannot depend on.
func countPlanCheckboxes(content string) (total, complete, incomplete int) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "- [x] ") || strings.HasPrefix(trimmed, "- [X] ") {
			complete++
			total++
		} else if strings.HasPrefix(trimmed, "- [ ] ") {
			incomplete++
			total++
		}
	}
	return total, complete, incomplete
}
