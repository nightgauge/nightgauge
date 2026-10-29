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
		got, ok := VerifyACAgainstDiff(ac, issue2661ACs[:i], d, ACTestState{})
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
		got, ok := VerifyACAgainstDiff(issue2661ACs[i], issue2661ACs[:i], d, ACTestState{})
		if !ok || got.Status != ACUnmet {
			t.Errorf("AC%d = %+v ok=%v, want unmet", i+1, got, ok)
		}
	}
}

func TestVerifyACAgainstDiff_RuntimeNamesStep(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	got, ok := VerifyACAgainstDiff("The `### Fixed` bullet renders when verified on a physical device.", nil, collect(t, dir), ACTestState{})
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

	got, ok := VerifyACAgainstDiff(ac, nil, d, ACTestState{Passed: true, Source: "feature-validate unit tests"})
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "TestWidgetRendersOnce") || !strings.Contains(got.Evidence, "widget_test.go:3") {
		t.Errorf("passed run: got %+v ok=%v", got, ok)
	}
	got, ok = VerifyACAgainstDiff(ac, nil, d, ACTestState{Source: "feature-validate unit tests"})
	if !ok || got.Status != ACUnmet {
		t.Errorf("failed run: got %+v ok=%v, want unmet", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`TestMissing` covers it.", nil, d, ACTestState{Passed: true})
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
		{"token met in named file", "`docs/a.md` mentions `--dry-run`.", ACMet},
		{"file-less token, no prior file (#2669)", "The diff mentions `--dry-run`.", ""},
		{"token missing in named file", "`docs/a.md` mentions `--wet-run`.", ACUnmet},
		{"token missing, no file", "The CLI accepts `--wet-run`.", ""},
		{"semantic", "The widget behaves well.", ""},
		{"runtime", "The flag works when run manually by an operator.", ACNotVerifiable},
		{"compound worst wins", "`docs/a.md` mentions `--dry-run`, and `README.md` is not modified; `x_test.go` is unchanged.", ACUnmet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := VerifyACAgainstDiff(c.text, nil, d, passed)
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
	if _, ok := VerifyACAgainstDiff("`README.md` is unchanged.", nil, &ACDiff{Files: map[string]*ACFileDiff{}}, passed); ok {
		t.Error("an empty diff must answer nothing")
	}
	if (&ACDiff{}).Checker(passed) != nil {
		t.Error("an empty diff must give no checker")
	}
}

// #2669: the operator-step and credential patterns no longer catch ordinary
// prose, and still catch real steps.
func TestACRuntimeSteps_Narrowed(t *testing.T) {
	step := func(text string) string {
		for _, r := range acRuntimeSteps {
			if r.re.MatchString(text) {
				return r.step
			}
		}
		return ""
	}
	for _, text := range []string{"The output is human-readable.", "Link the manual page.", "Document log rotation."} {
		if s := step(text); s != "" {
			t.Errorf("%q resolved step %q, want none", text, s)
		}
	}
	for _, text := range []string{"Manually verify the panel.", "The operator runs it.", "Rotate the API key.", "Needs human review.", "Rotate the deploy token."} {
		if s := step(text); s == "" {
			t.Errorf("%q resolved no step", text)
		}
	}
}

// #2669: acSection tries every named file before answering unmet.
func TestVerifyACAgainstDiff_SectionChecksEveryFile(t *testing.T) {
	d := memDiff(`diff --git a/docs/a.md b/docs/a.md
--- a/docs/a.md
+++ b/docs/a.md
@@ -2,0 +3 @@
+- unrelated line
diff --git a/docs/b.md b/docs/b.md
--- a/docs/b.md
+++ b/docs/b.md
@@ -2,0 +3 @@
+- cites #77
`, map[string]string{
		"docs/a.md": "# A\n## Usage\n- unrelated line\n",
		"docs/b.md": "# B\n## Usage\n- cites #77\n",
	})
	got, ok := VerifyACAgainstDiff("`docs/a.md` or `docs/b.md` adds a line under `## Usage` citing #77.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet {
		t.Errorf("got %+v ok=%v, want met", got, ok)
	}
	got, _ = VerifyACAgainstDiff("`docs/a.md` or `docs/b.md` adds a line under `## Usage` citing #78.", nil, d, ACTestState{})
	if got.Status != ACUnmet || !strings.Contains(got.Evidence, "docs/a.md") || !strings.Contains(got.Evidence, "docs/b.md") {
		t.Errorf("got %+v, want unmet naming both files", got)
	}
}

// #2669: a file-less clause inherits the prior criterion's file.
func TestVerifyACAgainstDiff_InheritsPriorFile(t *testing.T) {
	d := memDiff(`diff --git a/docs/a.md b/docs/a.md
--- a/docs/a.md
+++ b/docs/a.md
@@ -1,0 +2 @@
+- adds --dry-run
diff --git a/other.go b/other.go
--- a/other.go
+++ b/other.go
@@ -1,0 +2 @@
+// --wet-run
`, nil)
	prior := []string{"`docs/a.md` documents the flag."}
	if got, ok := VerifyACAgainstDiff("It mentions `--dry-run`.", prior, d, ACTestState{}); !ok || got.Status != ACMet {
		t.Errorf("inherited met: %+v ok=%v", got, ok)
	}
	if got, ok := VerifyACAgainstDiff("It mentions `--wet-run`.", prior, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("inherited file lacks token: %+v ok=%v, want unmet", got, ok)
	}
}

// #2669: oversized files are skipped by size, not read.
func TestCollectACDiff_BoundedReads(t *testing.T) {
	dir := changelogRepo(t)
	big := filepath.Join(dir, "big.md")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(acReadMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	writeFile(t, dir, "small.md", "one\ntwo\n")
	d := collect(t, dir)
	if _, ok := d.Files["big.md"]; ok {
		t.Error("oversized untracked file was collected")
	}
	if s := d.Files["small.md"]; s == nil || len(s.Added) != 2 || s.Added[1].Text != "two" {
		t.Errorf("small.md = %+v", s)
	}
	if l := d.lines("big.md"); l != nil {
		t.Errorf("lines(big.md) = %d lines, want nil", len(l))
	}
}

func TestCollectACDiff_CancelledContext(t *testing.T) {
	dir := changelogRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectACDiff(ctx, dir, "main"); err == nil {
		t.Error("cancelled context: want error")
	}
}

// #2669: a diff verdict resting on uncommitted paths says so.
func TestVerifyACAgainstDiff_UncommittedProvenance(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	got, _ := VerifyACAgainstDiff(issue2661ACs[0], nil, collect(t, dir), ACTestState{})
	if len(got.Uncommitted) != 1 || got.Uncommitted[0] != "packages/nightgauge/CHANGELOG.md" {
		t.Errorf("uncommitted = %v", got.Uncommitted)
	}
	gitOut(t, dir, "commit", "-qam", "bullet")
	got, _ = VerifyACAgainstDiff(issue2661ACs[0], nil, collect(t, dir), ACTestState{})
	if got.Status != ACMet || len(got.Uncommitted) != 0 {
		t.Errorf("committed: %+v", got)
	}
}
