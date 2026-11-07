package stages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}

func repoText(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(reviewRepoRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The gate parses what scripts/pr-review.mjs renders, and it refuses on drift.
// A renderer change that this test would have caught instead refuses every
// reviewed merge, so each literal the parser depends on is pinned to its source.
func TestReviewCommentContractMatchesTheRenderer(t *testing.T) {
	render := repoText(t, "scripts/pr-review.mjs")
	for name, want := range map[string]string{
		"marker":        `export const MARKER = "` + reviewCommentMarker + `"`,
		"clean review":  `"` + reviewCleanSentence + `"`,
		"severities":    `export const SEVERITIES = ["blocker", "major", "nitpick"];`,
		"finding shape": "`- **${f.severity}** (${f.pass}) \\`${escapeCode(where)}\\` — ${escapeText(f.claim)}`",
		"fix shape":     "`  - Fix: ${fix}`",
		"fix wrapping":  `.join("\n    ")`,
	} {
		if !strings.Contains(render, want) {
			t.Errorf("%s drifted: scripts/pr-review.mjs no longer contains %q", name, want)
		}
	}
	workflow := repoText(t, ".github/workflows/pr-review.yml")
	if !strings.Contains(workflow, "REVIEWER: "+reviewCommentAuthor) {
		t.Errorf("the workflow no longer posts as %s", reviewCommentAuthor)
	}
}

// The LLM stage has to reach the same gate, and it must not keep the old
// fix-everything instruction beside it. AC 6: nothing in this stage runs the
// full gate outside the ledger-keyed script.
func TestPRMergeSkillGoesThroughTheGate(t *testing.T) {
	skill := repoText(t, "skills/nightgauge-pr-merge/SKILL.md")
	merge := repoText(t, "skills/nightgauge-pr-merge/_includes/merge.md")
	compact := repoText(t, "skills/nightgauge-pr-merge/_profiles/compact.md")

	for _, want := range []string{
		"pr-stage review-gate",
		"pr-stage review-round begin",
		"pr-stage review-round end",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md lost %q", want)
		}
		if !strings.Contains(compact, want[:strings.LastIndex(want, " ")]) {
			t.Errorf("the compact profile lost %q", want)
		}
	}
	if !strings.Contains(merge, "pr-stage review-gate") {
		t.Error("merge.md no longer runs the gate before merging")
	}
	if strings.Contains(skill+compact, "Major issues should typically be fixed") ||
		strings.Contains(skill+compact, "major issues should be") {
		t.Error("the fix-every-major instruction came back")
	}

	entries, err := os.ReadDir(filepath.Join(reviewRepoRoot(t), "skills/nightgauge-pr-merge/_includes"))
	if err != nil {
		t.Fatal(err)
	}
	bodies := []string{skill, compact}
	for _, e := range entries {
		bodies = append(bodies, repoText(t, "skills/nightgauge-pr-merge/_includes/"+e.Name()))
	}
	for _, body := range bodies {
		if strings.Contains(body, "ci-local.sh") {
			t.Error("pr-merge runs the full local gate; a tree that passed feature-validate must not repeat it")
		}
	}
}
