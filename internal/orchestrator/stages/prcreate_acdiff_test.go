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
	d := &ACDiff{Files: parseUnifiedDiff(diff), content: map[string]acContent{}}
	for p, c := range content {
		d.content[p] = acContent{lines: strings.Split(c, "\n"), ok: true}
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
	for _, text := range []string{
		"The output is human-readable.", "Link the manual page.", "Document log rotation.",
		"The log output never contains credentials.", "Credentials are redacted from the PR body.",
		"The API key is masked in errors.",
	} {
		if s := step(text); s != "" {
			t.Errorf("%q resolved step %q, want none", text, s)
		}
	}
	for _, text := range []string{"Manually verify the panel.", "The operator runs it.", "Rotate the API key.", "Needs human review.", "Rotate the deploy token."} {
		if s := step(text); s == "" {
			t.Errorf("%q resolved no step", text)
		}
	}
	for _, text := range []string{"Rotate the service credentials.", "Provision an API key for CI.", "The credentials are supplied from the vault."} {
		if s := step(text); s != "a credential" {
			t.Errorf("%q resolved %q, want a credential", text, s)
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

// #2669 review: an unreadable named file may hold the section, so a readable
// file lacking it does not make the clause unmet.
func TestVerifyACAgainstDiff_SectionUnreadableNotUnmet(t *testing.T) {
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
`, map[string]string{"docs/a.md": "# A\n## Usage\n- unrelated line\n"})
	if got, ok := VerifyACAgainstDiff("`docs/a.md` or `docs/b.md` adds a line under `## Usage` citing #77.", nil, d, ACTestState{}); ok {
		t.Errorf("got %+v, want not diff-checkable (docs/b.md unreadable)", got)
	}
}

// #2669 review: a -run pattern is not unmet while a diffed test file is
// unreadable, since that file may define the test.
func TestVerifyACAgainstDiff_RunUnreadableTestFile(t *testing.T) {
	d := memDiff(`diff --git a/x_test.go b/x_test.go
--- a/x_test.go
+++ b/x_test.go
@@ -1,0 +2 @@
+// helper
`, nil)
	if got, ok := VerifyACAgainstDiff("`go test -run TestHidden` passes.", nil, d, ACTestState{Passed: true}); ok {
		t.Errorf("got %+v, want not diff-checkable", got)
	}
}

// #2669 review: a changelog clause is answered by the changelog, not by a
// file inherited from an earlier criterion.
func TestVerifyACAgainstDiff_ChangelogBeatsInherited(t *testing.T) {
	d := memDiff(`diff --git a/internal/foo.go b/internal/foo.go
--- a/internal/foo.go
+++ b/internal/foo.go
@@ -1,0 +2 @@
+// mentions --dry-run
diff --git a/CHANGELOG.md b/CHANGELOG.md
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -1,0 +2 @@
+- unrelated entry
`, nil)
	prior := []string{"`internal/foo.go` adds the flag."}
	if got, ok := VerifyACAgainstDiff("The changelog mentions `--dry-run`.", prior, d, ACTestState{}); ok && got.Status == ACMet {
		t.Errorf("got %+v, want not met from inherited internal/foo.go", got)
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
	// Inheritance may only add met: the criterion never named docs/a.md.
	if got, ok := VerifyACAgainstDiff("It mentions `--wet-run`.", prior, d, ACTestState{}); ok {
		t.Errorf("inherited file lacks token: %+v, want not diff-checkable", got)
	}
}

// #2669 review: a later criterion about another file must not fail against
// the file an earlier criterion named.
func TestVerifyACAgainstDiff_InheritedNeverUnmet(t *testing.T) {
	// #3038: the token criterion 3 asks for is in the diff, in cmd/root.go,
	// and absent from the inherited internal/foo.go. A search scoped to the
	// inherited file misses it; that miss must not become an unmet verdict.
	d := memDiff(`diff --git a/internal/foo.go b/internal/foo.go
--- a/internal/foo.go
+++ b/internal/foo.go
@@ -1,0 +2 @@
+func Foo() {}
diff --git a/internal/foo_test.go b/internal/foo_test.go
--- a/internal/foo_test.go
+++ b/internal/foo_test.go
@@ -1,0 +2 @@
+func TestFoo(t *testing.T) {}
diff --git a/cmd/root.go b/cmd/root.go
--- a/cmd/root.go
+++ b/cmd/root.go
@@ -1,0 +2 @@
+	return errors.New("invalid mode")
`, map[string]string{"internal/foo.go": "package foo\n\nfunc Foo() {}\n"})
	criteria := []string{
		"`internal/foo.go` validates the input.",
		"Unit tests cover the invalid case.",
		"The CLI prints `invalid mode` on failure.",
	}
	pass := ACTestState{Passed: true}
	if got, ok := VerifyACAgainstDiff(criteria[2], criteria[:2], d, pass); ok && got.Status == ACUnmet {
		t.Errorf("criterion 3 resolved unmet from an inherited file: %+v", got)
	}
	// A token the diff lacks entirely, inherited, is still not unmet.
	absent := "The CLI prints `bad flag` on failure."
	if got, ok := VerifyACAgainstDiff(absent, criteria[:2], d, pass); ok && got.Status == ACUnmet {
		t.Errorf("absent token resolved unmet from an inherited file: %+v", got)
	}
	// Inherited met is still allowed.
	parent := []string{"`internal/foo.go` adds `Foo`."}
	if got, ok := VerifyACAgainstDiff("Unit tests cover `Foo`.", parent, d, pass); !ok || got.Status != ACMet {
		t.Errorf("inherited met still allowed: %+v ok=%v", got, ok)
	}
	// A clause that names its file still resolves unmet on a missing token.
	named := "`internal/foo.go` mentions `--wet-run`."
	if got, ok := VerifyACAgainstDiff(named, nil, d, pass); !ok || got.Status != ACUnmet {
		t.Errorf("named-file missing token: got %+v ok=%v, want unmet", got, ok)
	}
}

// #2669 review: a file the verifier cannot read is not an empty file.
func TestVerifyACAgainstDiff_UnreadableNotEmpty(t *testing.T) {
	dir := changelogRepo(t)
	big := filepath.Join(dir, "CHANGELOG.md")
	f, err := os.OpenFile(big, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n### Fixed\n\n- " + strings.Repeat("x", acReadMaxBytes) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	d := collect(t, dir)
	if _, ok := d.lines("CHANGELOG.md"); ok {
		t.Fatal("oversized CHANGELOG.md was read")
	}
	for _, text := range []string{
		"`CHANGELOG.md` adds a bullet under `## [Unreleased]`.",
		"`CHANGELOG.md` section `## [Unreleased]` is unchanged.",
		"The `## [Unreleased]` section is unchanged.",
		// #3037 AC 3 and AC 4: the nested heading phrasing.
		"`CHANGELOG.md` adds a bullet under `## [Unreleased]` -> `### Fixed`.",
		"The `### Fixed` section is unchanged.",
	} {
		if got, ok := VerifyACAgainstDiff(text, nil, d, ACTestState{}); ok {
			t.Errorf("%q: got %+v, want not diff-checkable", text, got)
		}
	}
}

// #3037: a token missing from the added lines of a file the checker could
// not read is undecided, never unmet. The same file, read, stays unmet.
func TestVerifyACAgainstDiff_TokenUnreadableNotUnmet(t *testing.T) {
	const diff = `diff --git a/big.go b/big.go
--- a/big.go
+++ b/big.go
@@ -1,0 +2 @@
+var other = 1
`
	const text = "`big.go` defines `MissingSymbol`."
	read := memDiff(diff, map[string]string{"big.go": "package big\nvar other = 1\n"})
	if got, ok := VerifyACAgainstDiff(text, nil, read, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Fatalf("readable: got %+v ok=%v, want unmet", got, ok)
	}
	unread := memDiff(diff, nil)
	unread.content["big.go"] = acContent{ok: false}
	if got, ok := VerifyACAgainstDiff(text, nil, unread, ACTestState{}); ok {
		t.Errorf("unreadable: got %+v, want not diff-checkable", got)
	}
	added := memDiff(diff, nil)
	added.content["big.go"] = acContent{ok: false}
	if got, ok := VerifyACAgainstDiff("`big.go` defines `other`.", nil, added, ACTestState{}); !ok || got.Status != ACMet {
		t.Errorf("token in added lines of unreadable file: got %+v ok=%v, want met", got, ok)
	}
}

// #3037 AC 5: a readable empty file is read, not treated as unreadable.
func TestVerifyACAgainstDiff_EmptyFileReadable(t *testing.T) {
	dir := changelogRepo(t)
	writeFile(t, dir, "CHANGELOG.md", "")
	d := collect(t, dir)
	if l, ok := d.lines("CHANGELOG.md"); !ok || strings.Join(l, "") != "" {
		t.Fatalf("lines(empty CHANGELOG.md) = %q ok=%v, want readable and empty", l, ok)
	}
	text := "`CHANGELOG.md` adds a bullet under `## [Unreleased]`."
	if got, ok := VerifyACAgainstDiff(text, nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("got %+v ok=%v, want unmet", got, ok)
	}
}

// #2669 review: a tracked symlink to a regular file is read.
func TestACDiff_LinesFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.md", "# A\n## B\n")
	if err := os.Symlink("real.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	d := &ACDiff{Files: map[string]*ACFileDiff{}, Workdir: dir}
	if l, ok := d.lines("link.md"); !ok || len(l) < 2 || l[1] != "## B" {
		t.Errorf("lines(link.md) = %q ok=%v", l, ok)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.lines("sub"); ok {
		t.Error("a directory was read")
	}
}

// #2669 review: provenance is structural, so paths elided from the rendered
// evidence ("and N more") are still reported as uncommitted.
func TestVerifyACAgainstDiff_UncommittedBeyondEvidence(t *testing.T) {
	var diff strings.Builder
	var allowed []string
	for i := 1; i <= 8; i++ {
		p := "docs/f" + string(rune('0'+i)) + ".md"
		allowed = append(allowed, "`"+p+"`")
		diff.WriteString("diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n@@ -1,0 +2 @@\n+x\n")
	}
	d := memDiff(diff.String(), nil)
	d.Uncommitted = map[string]bool{"docs/f6.md": true, "docs/f7.md": true, "docs/f8.md": true}
	got, ok := VerifyACAgainstDiff("Only "+strings.Join(allowed, ", ")+" change.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet {
		t.Fatalf("got %+v ok=%v, want met", got, ok)
	}
	if strings.Contains(got.Evidence, "docs/f8.md") {
		t.Fatalf("fixture must elide f8 from evidence: %q", got.Evidence)
	}
	if want := "docs/f6.md docs/f7.md docs/f8.md"; strings.Join(got.Uncommitted, " ") != want {
		t.Errorf("uncommitted = %v, want %s", got.Uncommitted, want)
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
	if l, ok := d.lines("big.md"); ok {
		t.Errorf("lines(big.md) = %d lines, want unreadable", len(l))
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
			got, ok := VerifyACAgainstDiff(c.text, nil, c.d, c.tests)
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

// TestVerifyACAgainstDiff_Issue2840: tokens found only in a changed file the
// criterion is not about (a changelog line restating it, or some other file
// when the criterion names undiffed files) must not mark it met.
func TestVerifyACAgainstDiff_Issue2840(t *testing.T) {
	changelogOnly := memDiff(`diff --git a/CHANGELOG.md b/CHANGELOG.md
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -4,0 +5 @@
+- The FrobnicateWidget helper now retries once.
diff --git a/internal/w.go b/internal/w.go
--- a/internal/w.go
+++ b/internal/w.go
@@ -1,0 +2 @@
+func Other() {}
`, map[string]string{
		"CHANGELOG.md":  "# Changelog\n\n## [Unreleased]\n\n- The FrobnicateWidget helper now retries once.\n",
		"internal/w.go": "package w\nfunc Other() {}\n",
	})
	docsOnly := memDiff(`diff --git a/docs/y.md b/docs/y.md
--- a/docs/y.md
+++ b/docs/y.md
@@ -1,0 +2 @@
+Call FrobnicateWidget to retry.
`, map[string]string{"docs/y.md": "# Y\nCall FrobnicateWidget to retry.\n"})
	source := memDiff(`diff --git a/internal/w.go b/internal/w.go
--- a/internal/w.go
+++ b/internal/w.go
@@ -1,0 +2 @@
+func FrobnicateWidget() {}
`, map[string]string{"internal/w.go": "package w\nfunc FrobnicateWidget() {}\n"})
	passed := ACTestState{Passed: true, Source: "unit tests"}
	cases := []struct {
		name, text string
		d          *ACDiff
		want       string
	}{
		{"pathless token only in changelog", "The `FrobnicateWidget` helper retries once", changelogOnly, ""},
		{"named undiffed path, token elsewhere", "`internal/x.go` calls `FrobnicateWidget`", docsOnly, ""},
		// #2669 (ADR-007): a clause naming no file draws no met from an
		// arbitrary changed file, so this is not diff-checkable.
		{"pathless token in source", "The `FrobnicateWidget` helper retries once", source, ""},
		{"changelog clause token in changelog", "The changelog mentions `FrobnicateWidget`", changelogOnly, ACMet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := VerifyACAgainstDiff(c.text, nil, c.d, passed)
			if c.want == "" {
				if ok && got.Status == ACMet {
					t.Errorf("got %+v, want not met", got)
				}
				return
			}
			if !ok || got.Status != c.want {
				t.Errorf("got %+v ok=%v, want %s", got, ok, c.want)
			}
		})
	}
}

// #2669 merge with #2941 (ADR-007): a clause naming a diffed file still finds
// a token elsewhere in the diff, but a clause that inherited its file does not
// widen to the rest of the diff.
func TestVerifyACAgainstDiff_WideScopeOnlyForNamedFiles(t *testing.T) {
	d := memDiff(`diff --git a/docs/a.md b/docs/a.md
--- a/docs/a.md
+++ b/docs/a.md
@@ -1,0 +2 @@
+- documents the flag
diff --git a/internal/w.go b/internal/w.go
--- a/internal/w.go
+++ b/internal/w.go
@@ -1,0 +2 @@
+func FrobnicateWidget() {}
`, nil)
	got, ok := VerifyACAgainstDiff("`docs/a.md` covers `FrobnicateWidget`.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "internal/w.go") {
		t.Errorf("named file, token elsewhere: %+v ok=%v, want met citing internal/w.go", got, ok)
	}
	prior := []string{"`docs/a.md` documents the flag."}
	if got, ok := VerifyACAgainstDiff("It covers `FrobnicateWidget`.", prior, d, ACTestState{}); ok {
		t.Errorf("inherited file widened to the whole diff: %+v", got)
	}
	if got, ok := VerifyACAgainstDiff("It covers `FrobnicateWidget`.", nil, d, ACTestState{}); ok {
		t.Errorf("file-less clause resolved from an arbitrary file: %+v", got)
	}
}

// #3030: a key-value span matches the same pair in JSON, YAML or Go-literal
// form, the key case-insensitively and the value exactly.
func TestACSpanMatcher_KeyValue(t *testing.T) {
	match := acSpanMatcher("state: SUCCESS")
	for _, line := range []string{`"state":"SUCCESS"`, `State: "SUCCESS"`, `state: 'SUCCESS'`, `state=SUCCESS`, `{"context":"cla","state":"SUCCESS"}`} {
		if !match(line) {
			t.Errorf("%q did not match", line)
		}
	}
	for _, line := range []string{`"state":"FAILURE"`, `"status":"SUCCESS"`, `substate: SUCCESS`, `state: SUCCESSFUL`} {
		if match(line) {
			t.Errorf("%q matched", line)
		}
	}
	if lit := acSpanMatcher("--dry-run"); !lit("use --dry-run") || lit("use --wet-run") {
		t.Error("a non key-value span must match literally")
	}
}

// #3053: a call span matches declarations and calls with arguments.
func TestACSpanMatcher_Call(t *testing.T) {
	match := acSpanMatcher("name()")
	for _, line := range []string{"name(a, b)", "this.name(x)", "func (s *T) name(", "async name(", "name<T>(", "name (x)", "obj.name("} {
		if !match(line) {
			t.Errorf("%q did not match", line)
		}
	}
	for _, line := range []string{"xname(", "$name(", "name", "names("} {
		if match(line) {
			t.Errorf("%q matched", line)
		}
	}
	for _, tok := range []string{"obj.name()", "Type.Method()"} {
		last := tok[strings.LastIndex(tok, ".")+1 : len(tok)-2]
		if !acSpanMatcher(tok)("x." + last + "(1)") {
			t.Errorf("%q did not match its last segment", tok)
		}
	}
	if acSpanMatcher("repairIfNeeded()")("repairIfNeededLater(") {
		t.Error("a longer name must not match")
	}
	if lit := acSpanMatcher("--dry-run"); !lit("use --dry-run") || lit("use --wet-run") {
		t.Error("a non-call span must match literally")
	}
}

// #3053: replay of #3052 — a delivered function is not reported unmet.
func TestVerifyACAgainstDiff_Issue3052(t *testing.T) {
	decl := "  private async repairIfNeeded(force: boolean): Promise<void> {"
	d := memDiff(`diff --git a/src/repair.ts b/src/repair.ts
--- a/src/repair.ts
+++ b/src/repair.ts
@@ -3 +3 @@
-  /** Repairs. */
+  /** Repairs the marketplace entry when it drifted. */
`, map[string]string{"src/repair.ts": "export class R {\n\n  /** Repairs the marketplace entry when it drifted. */\n" + decl + "\n  }\n}"})
	for _, crit := range []string{
		"`src/repair.ts` keeps `repairIfNeeded()`.",
		"The doc comment in `src/repair.ts` mentions `repairIfNeeded()`.",
	} {
		if got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{}); ok && got.Status == ACUnmet {
			t.Errorf("%q: got %+v, want not unmet", crit, got)
		}
	}
	if got, ok := VerifyACAgainstDiff("`src/repair.ts` keeps `missingFn()`.", nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("missing function: got %+v ok=%v, want unmet", got, ok)
	}
}

func TestACIsPatternSpan(t *testing.T) {
	for tok, want := range map[string]bool{
		"<sha7>": true, "a/**": true, "*.go": true, "(Z+1)": true, "X.Y.(Z+1)-main.g<sha7>": true,
		"--dry-run": false, "f()": false, "Promise<void>": false, "*p": false, "(not-found)": false, "a * b": false, "<sha7>-x": true, "<div>": false, "<Button>": false, "/* x */": false, "**kwargs": false, "<N>.md": true, "dir/*": true, "**/x": true, "<issue-number>": true, "state: SUCCESS": false, "docs/a.md": false,
		"text.length === maxLength": true, "a !== b": true, "x == y": true, "n != 0": true, "i <= n": true, "n >= 1": true, "p && q": true, "a || b": true,
		"() => x": false, "--flag=value": false, "x = 1": false, "||": false, "<=": false,
	} {
		if got := acIsPatternSpan(tok); got != want {
			t.Errorf("acIsPatternSpan(%q) = %v, want %v", tok, got, want)
		}
	}
}

// #3030: replay of #3029 criterion 2 — the pair lands as JSON.
func TestVerifyACAgainstDiff_Issue3029(t *testing.T) {
	line := `{"__typename":"StatusContext","context":"cla","state":"SUCCESS"}`
	d := memDiff(`diff --git a/testdata/rollup.json b/testdata/rollup.json
--- a/testdata/rollup.json
+++ b/testdata/rollup.json
@@ -0,0 +1 @@
+`+line+"\n", map[string]string{"testdata/rollup.json": line})
	got, ok := VerifyACAgainstDiff("The fixture `testdata/rollup.json` holds a status context with `state: SUCCESS`.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet {
		t.Errorf("got %+v ok=%v, want met", got, ok)
	}
}

// #3030: replay of #3033 criterion 1 — placeholder and glob spans never make
// a clause unmet on their own.
func TestVerifyACAgainstDiff_Issue3033(t *testing.T) {
	d := memDiff(`diff --git a/scripts/version.sh b/scripts/version.sh
--- a/scripts/version.sh
+++ b/scripts/version.sh
@@ -0,0 +1 @@
+echo "$major.$minor.$((patch+1))-main.g$(git rev-parse --short=7 HEAD)"
`, map[string]string{"scripts/version.sh": "echo version"})
	crit := "The version is `X.Y.(Z+1)-main.g<sha7>` when `claude-plugins/nightgauge/**` or `.claude-plugin/**` changes"
	for name, c := range map[string]struct {
		text  string
		prior []string
	}{
		"named file": {"`scripts/version.sh` prints " + crit + ".", nil},
		"inherited":  {crit + ".", []string{"`scripts/version.sh` computes the version."}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := VerifyACAgainstDiff(c.text, c.prior, d, ACTestState{}); ok && got.Status == ACUnmet {
				t.Errorf("got %+v, want not unmet", got)
			}
		})
	}
	// A missing literal span still makes the clause unmet.
	if got, ok := VerifyACAgainstDiff("`scripts/version.sh` prints `<sha7>` and `--absent-flag`.", nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("got %+v ok=%v, want unmet", got, ok)
	}
}

// #3049: an unchanged word routes only when it is about a named path or heading.
func TestACUnchangedAboutTarget(t *testing.T) {
	for raw, want := range map[string]bool{
		"`p.go` is unchanged.":                                                      true,
		"`p.go` stays untouched.":                                                   true,
		"The change leaves `p.go` unchanged.":                                       true,
		"Ship it with `p.go` untouched.":                                            true,
		"`p.go` is not modified.":                                                   true,
		"The root `CHANGELOG.md` entry is unchanged.":                               true,
		"The `### Added` / `### Changed` sections in `[Unreleased]` are unchanged.": true,
		"`t.js` covers: unchanged when short enough.":                               false,
		"`t.js` covers the boundary (`a === b` is unchanged).":                      false,
		"`t.js` covers the boundary `a === b` is unchanged.":                        false,
	} {
		if got := acUnchangedAboutTarget(raw); got != want {
			t.Errorf("acUnchangedAboutTarget(%q) = %v, want %v", raw, got, want)
		}
	}
}

// #3049: replay of G1's criterion — a fully delivered test file is not unmet.
func TestVerifyACAgainstDiff_Issue3049(t *testing.T) {
	body := "test('short', () => expect(truncate('ab', 5)).toBe('ab'));\n" +
		"test('long', () => expect(truncate('abcdef', 4)).toBe('abc…'));\n" +
		"test('boundary', () => expect(truncate('abcd', 4)).toBe('abcd'));\n" +
		"test('type', () => expect(() => truncate(1, 4)).toThrow(TypeError));\n" +
		"test('range', () => expect(() => truncate('a', -1)).toThrow(RangeError));"
	var diff strings.Builder
	diff.WriteString("diff --git a/test/truncate.test.js b/test/truncate.test.js\nnew file mode 100644\n--- /dev/null\n+++ b/test/truncate.test.js\n@@ -0,0 +1,5 @@\n")
	for _, l := range strings.Split(body, "\n") {
		diff.WriteString("+" + l + "\n")
	}
	d := memDiff(diff.String(), map[string]string{"test/truncate.test.js": body})
	crit := "`test/truncate.test.js` covers: unchanged when short enough, truncated with `…` when too long, " +
		"the boundary (`text.length === maxLength` is unchanged), the `TypeError` and the `RangeError`."
	if got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{}); ok && got.Status == ACUnmet {
		t.Errorf("got %+v, want not unmet", got)
	}
	// A missing literal beside an expression is still unmet.
	if got, ok := VerifyACAgainstDiff("`test/truncate.test.js` holds `ZeroDivision` when `a === b`.", nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("missing literal: got %+v ok=%v, want unmet", got, ok)
	}
}
