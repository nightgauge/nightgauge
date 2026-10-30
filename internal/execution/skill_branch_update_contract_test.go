package execution

import (
	"fmt"
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
// Only explicit negations count: a bare "not" ("rebase the PR branch when it
// is not up to date") would exempt a prescription.
var prohibition = regexp.MustCompile(`(?i)\b(never|do not|don't|must not)\b`)

// includeDirective is the `<!-- include: path -->` form the skill renderer
// expands; like skillrender.ExpandIncludes, the path is relative to the skill
// directory. sharedReference is the compact profiles' "Read
// `skills/_shared/X.md` now" form, which names the same fragments by path.
var (
	includeDirective = regexp.MustCompile(`<!--\s*include:\s*(\S+)\s*-->`)
	sharedReference  = regexp.MustCompile("`skills/_shared/([A-Za-z0-9_.-]+\\.md)`")
)

// branchRewriteViolations returns "path:line: text" for every prescriptive
// branch-rewrite instruction in *.md under dir, CHANGELOG.md excluded, and in
// every fragment those files include or name by path, transitively — the
// skills pull their freshness step from skills/_shared, outside dir.
func branchRewriteViolations(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	var scan func(path string) error
	scan = func(path string) error {
		if seen[path] {
			return nil
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if branchRewriteTerm.MatchString(line) && !prohibition.MatchString(line) {
				out = append(out, filepath.Base(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
			if m := includeDirective.FindStringSubmatch(line); m != nil {
				if err := scan(filepath.Clean(filepath.Join(dir, m[1]))); err != nil {
					return fmt.Errorf("%s:%d include: %w", path, i+1, err)
				}
			}
			for _, m := range sharedReference.FindAllStringSubmatch(line, -1) {
				if err := scan(filepath.Join(filepath.Dir(dir), "_shared", m[1])); err != nil {
					return fmt.Errorf("%s:%d reference: %w", path, i+1, err)
				}
			}
		}
		return nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") || d.Name() == "CHANGELOG.md" {
			return nil
		}
		return scan(path)
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
		"Rebase the PR branch when it is not up to date",
		"<!-- include: ../frag.md -->",
	}, "\n")
	skill := filepath.Join(dir, "skill")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	// The included fragment lives outside the walked directory, like _shared.
	if err := os.WriteFile(filepath.Join(dir, "frag.md"), []byte("git rebase origin/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := branchRewriteViolations(t, skill); len(got) != 7 {
		t.Fatalf("violations = %d %v, want the 6 prescriptive lines plus the included one", len(got), got)
	}
}
