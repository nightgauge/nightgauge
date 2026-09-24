package skillrender

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ─── #1664: pr-create's compact render profile ─────────────────────────────
//
// pr-create is the stage whose output a human reads first. These tests pin
// the compact profile (skills/nightgauge-pr-create/_profiles/compact.md) to
// the issue's Verification list, corrected by the issue's own 2026-09-16
// plan-audit note: this repository's pr-create creates the PR through the
// Nightgauge Go binary's own `"$BINARY" pr create --title ... --body
// "$PR_BODY" ...` against a forge-client API — there is no `gh pr create`
// call and no `--body-file` convention here, so the guard below is
// "--body appears, --body-file never does" rather than the issue's
// literal (and inapplicable) `gh pr create ... --body-file` check. These
// tests fit the ADR-023 share at a 32768-token window, keep every
// must-survive marker of the full render, the `pr-{N}.json` contract, the
// idempotency check (Phase 3.6) and the security-scan step, and check every
// Read directive is an absolute, existing path under the skill root or its
// _shared sibling. Shared helpers live in compact_issue_pickup_test.go.

func TestCompactPrCreate_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	got := fitCompact(t, "pr-create", compact.Content)
	if !got.Fits {
		t.Errorf("Fit(pr-create compact, %d) = %+v, want Fits=true", compactTestWindow, got)
	}
}

// TestCompactPrCreate_BudgetCatchesInlinedSKILLMD proves the fit check above
// is sensitive: appending the full base SKILL.md body (what the profile
// trims to Read directives) takes the render back over the share.
func TestCompactPrCreate_BudgetCatchesInlinedSKILLMD(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	data, err := os.ReadFile(compact.SkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", compact.SkillPath, err)
	}
	inflated := compact.Content + "\n" + string(data)
	if got := fitCompact(t, "pr-create", inflated); got.Fits {
		t.Errorf("compact + full SKILL.md body still fits (%+v); the budget check is not discriminating", got)
	}
}

func TestCompactPrCreate_MarkerParity(t *testing.T) {
	full, compact := renderStagePair(t, "pr-create")
	for _, m := range missingMarkers(full.Content, compact.Content) {
		t.Errorf("compact render is missing must-survive marker: %s", m)
	}
}

// TestCompactPrCreate_KeepsContractAndChecks names the elements #1664 (as
// corrected by its plan-audit note) lists as retained.
func TestCompactPrCreate_KeepsContractAndChecks(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	for _, want := range []string{
		// The pr-{N}.json contract.
		`"schema_version": "1.0"`,
		`"pr_number": <PR_NUMBER>`,
		"preflight_results",
		"ci_monitoring",
		// Phase 3.6 idempotency check.
		"### Phase 3.6: Verify PR Created (Idempotency)",
		"This is the idempotency check",
		"pr-create is idempotent and safe to re-run",
		// Security re-scan.
		"### Phase 2.5: Security Re-Scan",
		// Completion Checklist.
		"## Completion Checklist",
		"PR created with issue linkage",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

// bodyFlagRE finds a `pr create` invocation's --body flag usage; bodyFileRE
// finds the unsupported --body-file flag this repository's Go binary does
// not implement (issue #1664's 2026-09-16 plan-audit correction).
var (
	bodyFlagRE = regexp.MustCompile(`--body "\$PR_BODY"`)
	bodyFileRE = regexp.MustCompile(`--body-file\s+\S`)
)

// TestCompactPrCreate_BodyAlwaysViaBodyFlagNeverBodyFile is the corrected
// AC: the PR body always reaches the Go binary through --body "$PR_BODY",
// and --body-file (unsupported here) never appears as an actual invocation
// flag — only as prose explaining it does not exist.
func TestCompactPrCreate_BodyAlwaysViaBodyFlagNeverBodyFile(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	if !bodyFlagRE.MatchString(compact.Content) {
		t.Error(`compact render never invokes "pr create" with --body "$PR_BODY"`)
	}
	if bodyFileRE.MatchString(compact.Content) {
		t.Error("compact render passes --body-file as an actual flag; the Go binary has no such flag")
	}
}

// TestCompactPrCreate_NeverCallsGhPrCreate is the sibling negative check:
// this stage never shells out to `gh pr create` — PR creation goes through
// the Go binary's own forge-client API.
func TestCompactPrCreate_NeverCallsGhPrCreate(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	ghCreateRE := regexp.MustCompile(`\bgh pr create\b`)
	if ghCreateRE.MatchString(compact.Content) {
		t.Error(`compact render invokes "gh pr create"; this repository creates PRs through the Go binary's "$BINARY" pr create`)
	}
}

func TestCompactPrCreate_ReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	_, compact := renderStagePair(t, "pr-create")
	assertCompactReadDirectives(t, compact)
}

func TestCompactPrCreate_CodeBlocksAreVerbatim(t *testing.T) {
	assertProfileCodeBlocksAreVerbatim(t, "pr-create")
}
