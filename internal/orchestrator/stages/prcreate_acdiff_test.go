package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The five acceptance criteria of #2661, verbatim. PR #2664 met all five
// with a docs-only diff, yet every one rendered "not verifiable" (#2665).
var issue2661ACs = []string{
	"`packages/nightgauge/CHANGELOG.md` has a bullet under `## [Unreleased]` → `### Fixed` describing the #2656 fix: a concurrent edit to a clean file in a sibling repo no longer fails the running stage.",
	"The bullet states that a clean → dirty sibling path is attributed to the stage only when its own tool calls name it, and that other such paths are reported as `[containment-ambiguous]` warnings.",
	"The bullet cites `#2656` and matches the substance of the root `CHANGELOG.md` entry for #2656 (lines 121–131).",
	"The root `CHANGELOG.md` entry is unchanged, and no file other than `packages/nightgauge/CHANGELOG.md` is modified.",
	"Existing `### Fixed` bullets and the `### Added` / `### Changed` sections in `[Unreleased]` are unchanged.",
}

const extChangelogBase = `# Changelog

## [Unreleased]

### Changed

- A changed thing (#1).

### Added

- An added thing (#2).

### Fixed

- An existing fix (#3).

## [0.1.0]

### Added

- The first release.
`

// The bullet PR #2664 added, under ### Fixed.
const bullet2656 = `
- **A concurrent edit to a clean file in a sibling repo no longer fails the
  running stage (#2656).** A clean → dirty path is now the stage's only when its
  own tool calls name it. Every other such path is a ` + "`[containment-ambiguous]`" + `
  warning.
`

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// changelogRepo builds a repo whose main holds both changelogs, with a
// feature branch checked out and nothing changed yet.
func changelogRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "packages/nightgauge/CHANGELOG.md", extChangelogBase)
	writeFile(t, dir, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n- The #2656 fix.\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "docs/2661-changelog")
	return dir
}

func addBullet2656(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "packages/nightgauge/CHANGELOG.md",
		strings.Replace(extChangelogBase, "- An existing fix (#3).\n", "- An existing fix (#3).\n"+bullet2656, 1))
}

func collect(t *testing.T, dir string) *ACDiff {
	t.Helper()
	d, err := CollectACDiff(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Regression fixture (#2665): the #2661 diff answers all five criteria. The
// change is left uncommitted, as it is when the commit owner has not run yet.
func TestVerifyACAgainstDiff_Issue2661AllMet(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	d := collect(t, dir)
	for i, ac := range issue2661ACs {
		got, ok := VerifyACAgainstDiff(ac, d, ACTestState{})
		if !ok || got.Status != ACMet {
			t.Errorf("AC%d = %+v ok=%v, want met", i+1, got, ok)
		}
	}
}

// The same criteria against a diff that also edits the root CHANGELOG.md and
// rewrites an existing bullet must not close the issue.
func TestVerifyACAgainstDiff_Issue2661Violated(t *testing.T) {
	dir := changelogRepo(t)
	writeFile(t, dir, "packages/nightgauge/CHANGELOG.md",
		strings.Replace(extChangelogBase, "- An existing fix (#3).\n", "- A reworded fix (#3).\n"+bullet2656, 1))
	writeFile(t, dir, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n- The #2656 fix, edited.\n")
	d := collect(t, dir)
	for _, i := range []int{3, 4} {
		got, ok := VerifyACAgainstDiff(issue2661ACs[i], d, ACTestState{})
		if !ok || got.Status != ACUnmet {
			t.Errorf("AC%d = %+v ok=%v, want unmet", i+1, got, ok)
		}
	}
}

func TestVerifyACAgainstDiff_RuntimeNamesStep(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	got, ok := VerifyACAgainstDiff("The `### Fixed` bullet renders when verified on a physical device.", collect(t, dir), ACTestState{})
	if !ok || got.Status != ACNotVerifiable || got.Step != "a physical device" {
		t.Errorf("got %+v ok=%v, want not_verifiable needing a physical device", got, ok)
	}
}

// A test-named criterion is met only when the test is defined on the branch
// (here found by git grep, outside the diff) and the run passed; the
// evidence cites the test name (#2665 AC2).
func TestVerifyACAgainstDiff_TestNamedCriterion(t *testing.T) {
	dir := changelogRepo(t)
	writeFile(t, dir, "widget_test.go", "package w\n\nfunc TestWidgetRendersOnce(t *testing.T) {}\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "test")
	gitOut(t, dir, "checkout", "-q", "main")
	gitOut(t, dir, "merge", "-q", "docs/2661-changelog")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	addBullet2656(t, dir)
	d := collect(t, dir)
	ac := "`TestWidgetRendersOnce` covers the duplicate list."

	got, ok := VerifyACAgainstDiff(ac, d, ACTestState{Passed: true, Source: "feature-validate unit tests"})
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "TestWidgetRendersOnce") || !strings.Contains(got.Evidence, "widget_test.go:3") {
		t.Errorf("passed run: got %+v ok=%v", got, ok)
	}
	got, ok = VerifyACAgainstDiff(ac, d, ACTestState{Source: "feature-validate unit tests"})
	if !ok || got.Status != ACUnmet {
		t.Errorf("failed run: got %+v ok=%v, want unmet", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`TestMissing` covers it.", d, ACTestState{Passed: true})
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "not defined") {
		t.Errorf("missing test: got %+v ok=%v, want unmet", got, ok)
	}
}

func TestCollectACDiff_CommittedAndUntracked(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	gitOut(t, dir, "commit", "-qam", "bullet")
	writeFile(t, dir, "docs/new.md", "# New\n\nhello\n")
	d := collect(t, dir)
	f := d.Files["packages/nightgauge/CHANGELOG.md"]
	if f == nil || f.Deleted != 0 || len(f.Added) != 5 || f.Added[0].N < 16 {
		t.Fatalf("committed change = %+v", f)
	}
	if n := d.Files["docs/new.md"]; n == nil || n.Status != "added" || len(n.Added) != 3 {
		t.Errorf("untracked file = %+v", n)
	}
	if _, err := CollectACDiff(context.Background(), dir, "no-such-base"); err == nil {
		t.Error("an unresolvable base must be an error, not an empty diff")
	}
}

func TestParseUnifiedDiff(t *testing.T) {
	out := `diff --git a/a.md b/a.md
index 1..2 100644
--- a/a.md
+++ b/a.md
@@ -3,0 +4,2 @@ x
+one
++++ not a header
@@ -9 +10,0 @@
-gone
diff --git a/old.go b/new.go
similarity index 90%
rename from old.go
rename to new.go
diff --git a/b.go b/b.go
new file mode 100644
--- /dev/null
+++ b/b.go
@@ -0,0 +1 @@
+package b
`
	files := parseUnifiedDiff(out)
	a := files["a.md"]
	if a == nil || a.Deleted != 1 || len(a.Added) != 2 || a.Added[1] != (acDiffLine{N: 5, Text: "+++ not a header"}) {
		t.Errorf("a.md = %+v", a)
	}
	if files["new.go"] == nil || files["new.go"].Status != "renamed" {
		t.Errorf("rename = %+v", files["new.go"])
	}
	if b := files["b.go"]; b == nil || b.Status != "added" || len(b.Added) != 1 {
		t.Errorf("b.go = %+v", b)
	}
}

// memDiff builds an in-memory diff whose branch content is given per file.
func memDiff(diff string, content map[string]string) *ACDiff {
	d := &ACDiff{Files: parseUnifiedDiff(diff), content: map[string][]string{}}
	for p, c := range content {
		d.content[p] = strings.Split(c, "\n")
	}
	return d
}

func TestVerifyACAgainstDiff_Rules(t *testing.T) {
	d := memDiff(`diff --git a/docs/a.md b/docs/a.md
--- a/docs/a.md
+++ b/docs/a.md
@@ -4,0 +5 @@
+- adds the --dry-run flag (#77)
diff --git a/x_test.go b/x_test.go
--- a/x_test.go
+++ b/x_test.go
@@ -1,0 +2 @@
+func TestDryRun(t *testing.T) {}
`, map[string]string{"docs/a.md": "# A\n\n## Usage\n\n- adds the --dry-run flag (#77)\n\n## Other\n"})
	passed := ACTestState{Passed: true, Source: "feature-validate unit tests"}
	cases := []struct {
		name, text, want string // want "" means not diff-checkable
	}{
		{"scope met", "Only `docs/a.md` and `x_test.go` change.", ACMet},
		{"scope unmet", "No file other than `docs/a.md` is modified.", ACUnmet},
		{"unchanged path absent", "`README.md` is unchanged.", ACMet},
		{"unchanged dot path", "`.github/workflows/ci.yml` is unchanged.", ACMet},
		{"unchanged path changed", "`docs/a.md` is unchanged.", ACUnmet},
		{"test met", "`TestDryRun` passes.", ACMet},
		{"section met", "`docs/a.md` documents the flag under `## Usage`, citing #77.", ACMet},
		{"section wrong heading", "`docs/a.md` has a line under `## Other`.", ACUnmet},
		{"section missing token", "`docs/a.md` adds a line under `## Usage` citing #78.", ACUnmet},
		{"token met", "The diff mentions `--dry-run`.", ACMet},
		{"token missing in named file", "`docs/a.md` mentions `--wet-run`.", ACUnmet},
		{"token missing, no file", "The CLI accepts `--wet-run`.", ""},
		{"semantic", "The widget behaves well.", ""},
		{"runtime", "The flag works when run manually by an operator.", ACNotVerifiable},
		{"compound worst wins", "`docs/a.md` mentions `--dry-run`, and `README.md` is not modified; `x_test.go` is unchanged.", ACUnmet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := VerifyACAgainstDiff(c.text, d, passed)
			if c.want == "" {
				if ok {
					t.Errorf("got %+v, want not diff-checkable", got)
				}
				return
			}
			if !ok || got.Status != c.want {
				t.Errorf("got %+v ok=%v, want %s", got, ok, c.want)
			}
		})
	}
	if _, ok := VerifyACAgainstDiff("`README.md` is unchanged.", &ACDiff{Files: map[string]*ACFileDiff{}}, passed); ok {
		t.Error("an empty diff must answer nothing")
	}
	if (&ACDiff{}).Checker(passed) != nil {
		t.Error("an empty diff must give no checker")
	}
}

// #2916 (ac46da49): a `-run` pattern selecting existing tests, and a
// changelog entry under `## [Unreleased]`.
const acDiff2916 = `diff --git a/CHANGELOG.md b/CHANGELOG.md
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -4,0 +5,2 @@
+- The architecture-release tests no longer fail intermittently when the held
+  issue's background board read reaches the test's fixture forge.
diff --git a/internal/orchestrator/autonomous_architecture_release_test.go b/internal/orchestrator/autonomous_architecture_release_test.go
--- a/internal/orchestrator/autonomous_architecture_release_test.go
+++ b/internal/orchestrator/autonomous_architecture_release_test.go
@@ -3,0 +4,2 @@
+		case strings.Contains(string(body), "projectV2("):
+			_, _ = io.WriteString(w, ` + "`" + `{"errors":[{"message":"board read not served by the release fixture"}]}` + "`" + `)
`

const acChangelog2916 = "# Changelog\n\n## [Unreleased]\n\n" +
	"- The architecture-release tests no longer fail intermittently when the held\n" +
	"  issue's background board read reaches the test's fixture forge.\n\n## [0.8.0] - 2026-11-04\n"

// #2912 (4cdeaef9): the script hunks; `git add -A` predates the diff.
const acDiff2912 = `diff --git a/scripts/ci-local-go-test.sh b/scripts/ci-local-go-test.sh
--- a/scripts/ci-local-go-test.sh
+++ b/scripts/ci-local-go-test.sh
@@ -1 +1,2 @@
-# Must match TreeFingerprint in internal/hooks/pre_push_skip.go.
+# Must match TreeFingerprint in internal/hooks/pre_push_skip.go. The scratch
+# index starts from HEAD so tracked files matching an ignore rule stay (#2889).
@@ -4,0 +6,3 @@
+  if git rev-parse -q --verify 'HEAD^{tree}' >/dev/null 2>&1; then
+    GIT_INDEX_FILE="$dir/index" git read-tree HEAD || { rm -rf "$dir"; return 1; }
+  fi
`

const acScript2912 = "# Must match TreeFingerprint in internal/hooks/pre_push_skip.go. The scratch\n" +
	"# index starts from HEAD so tracked files matching an ignore rule stay (#2889).\n" +
	"tree_fingerprint() {\n  local dir\n  dir=$(mktemp -d)\n" +
	"  if git rev-parse -q --verify 'HEAD^{tree}' >/dev/null 2>&1; then\n" +
	"    GIT_INDEX_FILE=\"$dir/index\" git read-tree HEAD || { rm -rf \"$dir\"; return 1; }\n  fi\n" +
	"  GIT_INDEX_FILE=\"$dir/index\" git add -A\n}\n"

func TestVerifyACAgainstDiff_Issue2917(t *testing.T) {
	const runAC = "`go test ./internal/orchestrator -run 'TestArchitecture' -race -count=200` passes on the branch, and the command and result are recorded in the PR"
	const tokenAC = "The scratch index starts from `HEAD` (`git read-tree HEAD`) before `git add -A`, in `TreeFingerprint` and in `scripts/ci-local-go-test.sh`"
	const changelogAC = "A changelog entry under `## [Unreleased]`"
	relTest := "internal/orchestrator/autonomous_architecture_release_test.go"
	d2916 := func() *ACDiff {
		return memDiff(acDiff2916, map[string]string{
			"CHANGELOG.md": acChangelog2916,
			relTest:        "package orchestrator\n\nfunc TestArchitectureRelease_HoldsIssue(t *testing.T) {\n",
		})
	}
	d2912 := func(script string) *ACDiff {
		return memDiff(acDiff2912, map[string]string{"scripts/ci-local-go-test.sh": script})
	}
	misplaced := memDiff(`diff --git a/CHANGELOG.md b/CHANGELOG.md
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -6,0 +7 @@
+- an entry in the wrong section
`, map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n## [0.8.0]\n\n- an entry in the wrong section\n"})
	passed := ACTestState{Passed: true, Source: "feature-validate unit tests"}
	cases := []struct {
		name, text string
		d          *ACDiff
		tests      ACTestState
		want       string
	}{
		{"run pattern met", runAC, d2916(), passed, ACMet},
		{"run pattern, run failed", runAC, d2916(), ACTestState{}, ACUnmet},
		{"run pattern matches nothing", "`go test ./x -run 'TestNoSuch'` passes", d2916(), passed, ACUnmet},
		{"run unquoted", "`go test ./internal/orchestrator -run TestArchitecture` passes", d2916(), passed, ACMet},
		{"run equals", "`go test ./internal/orchestrator -run=TestArchitecture` passes", d2916(), passed, ACMet},
		{"run subtest pattern", "`go test ./x -run 'TestArchitecture/hold'` passes", d2916(), passed, ACMet},
		{"name outside -run still required", "`TestMissing` passes via `go test -run TestMissing`", d2916(), passed, ACUnmet},
		{"run invalid regex", "`go test ./x -run 'Test(['` passes", d2916(), passed, ""},
		{"token pre-existing in file", tokenAC, d2912(acScript2912), passed, ACMet},
		{"token absent from file", tokenAC, d2912(strings.Replace(acScript2912, "git add -A", "git add .", 1)), passed, ACUnmet},
		{"tokens all pre-existing", "`scripts/ci-local-go-test.sh` runs `git add -A`", d2912(acScript2912), passed, ""},
		{"changelog met 2916", changelogAC, d2916(), passed, ACMet},
		{"changelog met 2912", changelogAC, memDiff(`diff --git a/CHANGELOG.md b/CHANGELOG.md
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -4,0 +5,4 @@
+- A reviewed or gated tree now keeps tracked files that match an ignore rule
+  (`+"`.gitignore`, `.git/info/exclude`, `core.excludesFile`"+`). They no longer read
+  as deleted in review diffs, and the tree fingerprint no longer varies between
+  clones with different local excludes.
`, map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n- A reviewed\n"}), passed, ACMet},
		{"changelog not in diff", changelogAC, d2912(acScript2912), passed, ACUnmet},
		{"changelog wrong heading", changelogAC, misplaced, passed, ACUnmet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := VerifyACAgainstDiff(c.text, c.d, c.tests)
			if c.want == "" {
				if ok {
					t.Errorf("got %+v, want not diff-checkable", got)
				}
				return
			}
			if !ok || got.Status != c.want {
				t.Errorf("got %+v ok=%v, want %s", got, ok, c.want)
			}
		})
	}
}
