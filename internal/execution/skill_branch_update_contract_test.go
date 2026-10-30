package execution

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// An existing PR branch is updated by merging its base, never rebased or
// force-pushed (#2699). A rebased tip stops descending from origin's, so
// validate's push is rejected and the run ends branch_forked. This test locks
// the rule into the three skills that update a PR branch: planning (which
// writes the plan), feature-dev (conflict re-dispatch) and feature-validate
// (the push), in the canonical tree and the generated plugin mirror.

var branchRewriteTerm = regexp.MustCompile(
	`--force-with-lease|git push (\S+ )*(--force|-f)\b|\+HEAD:|git rebase\b|git pull --rebase|` +
		`(?i)\brebase( of)? (the |this |an |its )?(existing )?(PR )?branch`)

// prohibition marks a line that forbids the term rather than prescribes it.
var prohibition = regexp.MustCompile(`(?i)\b(never|do not|don't|not)\b`)

// branchRewriteViolations returns "path:line: text" for every prescriptive
// branch-rewrite instruction in *.md under dir, CHANGELOG.md excluded.
func branchRewriteViolations(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") || d.Name() == "CHANGELOG.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if branchRewriteTerm.MatchString(line) && !prohibition.MatchString(line) {
				out = append(out, filepath.Base(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func TestSkillBranchUpdateContract_NoRebaseOrForcePush(t *testing.T) {
	root := repoRoot(t)
	dirs := []string{
		"skills/nightgauge-feature-planning",
		"skills/nightgauge-feature-dev",
		"skills/nightgauge-feature-validate",
		"claude-plugins/nightgauge/skills/feature-planning",
		"claude-plugins/nightgauge/skills/feature-dev",
		"claude-plugins/nightgauge/skills/feature-validate",
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		for _, v := range branchRewriteViolations(t, filepath.Join(root, d)) {
			t.Errorf("%s/%s — an existing PR branch is updated by merging its base, never rebased or force-pushed (#2699)", d, v)
		}
	}
}

// TestSkillBranchUpdateContract_MatcherFires is the positive arm: the
// matcher must catch each prescriptive form and pass each prohibition.
func TestSkillBranchUpdateContract_MatcherFires(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"git rebase origin/main",
		"git push --force-with-lease origin HEAD",
		"git push origin -f",
		"git push origin +HEAD:refs/heads/x",
		"Never rebase: do not run git rebase on a PR branch.",
		"1. Re-attempt the rebase of the PR branch onto main",
		"git merge --no-ff origin/main",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := branchRewriteViolations(t, dir); len(got) != 5 {
		t.Fatalf("violations = %d %v, want the 5 prescriptive lines", len(got), got)
	}
}
