package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
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
		{"section missing token", "`docs/a.md` adds a line under `## Usage` citing `#78`.", ACUnmet},
		{"section missing prose ref (#3233)", "`docs/a.md` adds a line under `## Usage` citing #78.", ""},
		{"prose ref missing is context (#3233)", "`docs/a.md` mentions `--dry-run`, as PR #3225 asked.", ""},
		{"prose ref added is evidence (#3233)", "`docs/a.md` mentions `--dry-run` (#77).", ACMet},
		{"code-span ref missing (#3233)", "`docs/a.md` mentions `#3225`.", ACUnmet},
		{"regression quoting #3226 (#3233)", "A regression case in `x_test.go` reproducing PR #3225's criterion adds `TestDryRun`.", ACMet},
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
	got, _ = VerifyACAgainstDiff("`docs/a.md` or `docs/b.md` adds a line under `## Usage` citing `#78`.", nil, d, ACTestState{})
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
		"<sha7>": true, "a/**": false, "*.go": false, "(Z+1)": true, "X.Y.(Z+1)-main.g<sha7>": true,
		"--dry-run": false, "f()": false, "Promise<void>": false, "*p": false, "(not-found)": false, "a * b": false, "<sha7>-x": true, "<div>": false, "<Button>": false, "/* x */": false, "**kwargs": false, "<N>.md": true, "dir/*": false, "**/x": false, "<issue-number>": true, "state: SUCCESS": false, "docs/a.md": false,
		"text.length === maxLength": true, "a !== b": true, "x == y": true, "n != 0": true, "i <= n": true, "n >= 1": true, "p && q": true, "a || b": true,
		"() => x": false, "--flag=value": false, "x = 1": false, "||": false, "<=": false,
	} {
		if got := acIsPatternSpan(tok); got != want {
			t.Errorf("acIsPatternSpan(%q) = %v, want %v", tok, got, want)
		}
	}
	for tok, want := range map[string]bool{"a/**": true, "*.go": true, "dir/*": true, "**/x": true, "/* x */": false, "**kwargs": false, "docs/a.md": false} {
		if got := acIsGlobSpan(tok); got != want {
			t.Errorf("acIsGlobSpan(%q) = %v, want %v", tok, got, want)
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
		"`p.go` is unchanged.":                        true,
		"`p.go` stays untouched.":                     true,
		"The change leaves `p.go` unchanged.":         true,
		"Ship it with `p.go` untouched.":              true,
		"`p.go` is not modified.":                     true,
		"The root `CHANGELOG.md` entry is unchanged.": true,
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

// #3055: code spans follow CommonMark backtick-run matching.
func TestACCodeSpans(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"use `x` here", []string{"x"}},
		{"`` a`b ``", []string{"a`b"}},
		{"names ``` `` `name()` `` ```", []string{"`` `name()` ``"}},
		{"`` `name()` ``", []string{"`name()`"}},
		{"a `` b ` c", nil},
		{"` ` and `y`", []string{" ", "y"}},
		{"``a`b`` then `c`", []string{"a`b", "c"}},
	}
	for _, c := range cases {
		var got []string
		for _, sp := range acCodeSpans(c.in) {
			got = append(got, c.in[sp[2]:sp[3]])
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("acCodeSpans(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := acStripCodeSpans("a `x` b `` y`z `` c"); got != "a   b   c" {
		t.Errorf("acStripCodeSpans = %q", got)
	}
}

func TestACSpanContent(t *testing.T) {
	for in, want := range map[string]string{
		"`` `name()` ``": "name()",
		"`x`":            "x",
		"`a` and `b`":    "`a` and `b`",
		"plain":          "plain",
	} {
		if got := acSpanContent(in); got != want {
			t.Errorf("acSpanContent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := splitACClauses("keeps `` a, and b `` whole"); len(got) != 1 {
		t.Errorf("split inside a multi-backtick span: %q", got)
	}
	if got := splitACClauses("`a` is set, and `b` is set"); len(got) != 2 {
		t.Errorf("split outside spans: %q", got)
	}
}

// #3055: replay of #3054 — a double-backtick span is one span, not prose.
func TestVerifyACAgainstDiff_Issue3054(t *testing.T) {
	const f = "internal/orchestrator/stages/prcreate_acdiff_test.go"
	crit := "Regression test in `" + f + "`: the #3052 shape, where a diff edits the doc " +
		"comment above `private async repairIfNeeded(` in a named TypeScript file and the " +
		"criterion names ``` `` `repairIfNeeded()` `` ``` with that file, does not resolve `ACUnmet`."
	for _, c := range splitACClauses(crit) {
		for _, tok := range parseACClause(c).Tokens {
			if strings.Contains(tok, "with that file") {
				t.Fatalf("prose extracted as a span: %q", tok)
			}
		}
	}
	d := memDiff(`diff --git a/`+f+` b/`+f+`
--- a/`+f+`
+++ b/`+f+`
@@ -1 +1,4 @@
 package stages
+
+// #3052: "private async repairIfNeeded(" must not be ACUnmet.
+func TestRepair(t *testing.T) { repairIfNeeded(true) }
`, nil)
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	if ok && got.Status == ACUnmet {
		t.Fatalf("delivered criterion resolved unmet: %+v", got)
	}
}

// #3154: a single identifier wrapped in `{}`, `<>` or `[]` matches as a whole
// word; #3053's call spans and other spans are unchanged.
func TestACSpanMatcher_WrappedIdentifier(t *testing.T) {
	cases := []struct {
		tok, line string
		want      bool
	}{
		{"{sessionJwt}", "[{ sessionJwt: [] }]", true},
		{"{sessionJwt}", "const sessionJwtX = 1", false},
		{"{sessionJwt}", "  return sessionJwt;", true},
		{"{ sessionJwt }", "sessionJwt", true},
		{"<Button>", "return <Button onClick={f} />", true},
		{"<Button>", "ButtonGroup", false},
		{"[items]", "items.push(x)", true},
		{"[items]", "$items", false},
		{"{a, b}", "a, b", false},
		{"{a, b}", "x = {a, b}", true},
		{"name()", "name(a, b)", true},
		{"name()", "xname(", false},
		{"name()", "name", false},
	}
	for _, c := range cases {
		if got := acSpanMatcher(c.tok)(c.line); got != c.want {
			t.Errorf("acSpanMatcher(%q)(%q) = %v, want %v", c.tok, c.line, got, c.want)
		}
	}
	if acIsPatternSpan("{sessionJwt}") || acIsPatternSpan("<Button>") {
		t.Error("a wrapped identifier must reach the matcher, not be read as a pattern")
	}
	if !acIsPatternSpan("<issue-number>") {
		t.Error("a placeholder must stay a pattern")
	}
}

// #3154: a criterion naming `{sessionJwt}` is met by a diff that adds the
// identifier inside an object literal.
func TestVerifyACAgainstDiff_Issue3154(t *testing.T) {
	d := memDiff(`diff --git a/src/session.ts b/src/session.ts
--- a/src/session.ts
+++ b/src/session.ts
@@ -1 +1 @@
-export const fields = [];
+export const fields = [{ sessionJwt: [] }];
`, map[string]string{"src/session.ts": "export const fields = [{ sessionJwt: [] }];\n"})
	got, ok := VerifyACAgainstDiff("`src/session.ts` adds `{sessionJwt}`.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet {
		t.Errorf("got %+v ok=%v, want met", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`src/session.ts` adds `{sessionToken}`.", nil, d, ACTestState{})
	if ok && got.Status == ACMet {
		t.Errorf("absent identifier: got %+v, want not met", got)
	}
}

func TestACSpanMatcher_PlaceholderCall(t *testing.T) {
	cases := []struct {
		tok, line string
		want      bool
	}{
		{"it(...)", `  it("gives each cell its own clone", async () => {`, true},
		{"obj.run(…)", "x.run(a)", true},
		{"it( ... )", "it(", true},
		{"it(...)", "split(", false},
		{"it(...)", "it", false},
		{"run(ctx, opts)", "run(ctx, opts)", true},
		{"run(ctx, opts)", "run(a)", false},
	}
	for _, c := range cases {
		if got := acSpanMatcher(c.tok)(c.line); got != c.want {
			t.Errorf("acSpanMatcher(%q)(%q) = %v, want %v", c.tok, c.line, got, c.want)
		}
	}
}

func TestVerifyACAgainstDiff_Issue3225(t *testing.T) {
	path := "src/test/worktreeWorkspace.test.ts"
	content := "describe(\"w\", () => {\n  it(\"gives each cell its own clone\", async () => {\n" +
		"    // Cloning is slow on CI; allow 30 s.\n    await run();\n  }, 30_000);\n});"
	d := memDiff(`diff --git a/`+path+` b/`+path+`
--- a/`+path+`
+++ b/`+path+`
@@ -3,2 +3,3 @@
+    // Cloning is slow on CI; allow 30 s.
     await run();
-  });
+  }, 30_000);
`, map[string]string{path: content})
	crit := "The clone-isolation `it(...)` case in `worktreeWorkspace.test.ts` declares an explicit timeout greater than 5000 ms"
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	if ok && got.Status == ACUnmet {
		t.Errorf("got %+v, want not unmet", got)
	}
	if strings.Contains(got.Evidence, "does not add `it(...)`") {
		t.Errorf("evidence still reports it(...) missing: %+v", got)
	}
}

// #3233: a prose #N the diff lacks leaves the clause to validate, so
// validate's met resolves the criterion met instead of the diff's unmet.
func TestResolveACVerdicts_ProseRefKeepsValidateMet(t *testing.T) {
	d := memDiff(`diff --git a/x_test.go b/x_test.go
--- a/x_test.go
+++ b/x_test.go
@@ -1,0 +2 @@
+func TestDryRun(t *testing.T) {}
`, map[string]string{"x_test.go": "package x\nfunc TestDryRun(t *testing.T) {}\n"})
	crit := "A regression case reproducing PR #3225 lands in `x_test.go`."
	validate := []ACVerdict{{Index: 0, Text: crit, Status: ACMet, Evidence: "x_test.go:2 TestDryRun"}}
	got := ResolveACVerdicts([]string{crit}, validate, nil, d.Checker(ACTestState{Passed: true}))
	if len(got) != 1 || got[0].Status != ACMet {
		t.Fatalf("got %+v, want met from validate", got)
	}
}

// #3042 helpers: a repo with a base commit on main and a feature branch.
func repo3042(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "README.md", "base\n")
	for p, c := range files {
		writeFile(t, dir, p, c)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "feat/3042")
	return dir
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "change")
}

func wantStatus(t *testing.T, name string, got ACCheck, ok bool, status string) {
	t.Helper()
	switch {
	case status == "" && ok:
		t.Errorf("%s: got %+v, want undecided", name, got)
	case status != "" && (!ok || got.Status != status):
		t.Errorf("%s: got %+v ok=%v, want %s", name, got, ok, status)
	}
}

// #3042 AC 0: a glob span is decided against the diffed paths.
func TestVerifyACAgainstDiff_GlobSpan(t *testing.T) {
	d := memDiff(`diff --git a/claude-plugins/nightgauge/hooks/a.sh b/claude-plugins/nightgauge/hooks/a.sh
--- a/claude-plugins/nightgauge/hooks/a.sh
+++ b/claude-plugins/nightgauge/hooks/a.sh
@@ -0,0 +1 @@
+echo a
diff --git a/internal/x.go b/internal/x.go
--- a/internal/x.go
+++ b/internal/x.go
@@ -0,0 +1 @@
+package x
`, map[string]string{"claude-plugins/nightgauge/hooks/a.sh": "echo a", "internal/x.go": "package x"})
	got, ok := VerifyACAgainstDiff("The change updates `claude-plugins/nightgauge/**`.", nil, d, ACTestState{})
	wantStatus(t, "matching glob", got, ok, ACMet)
	got, ok = VerifyACAgainstDiff("The change updates `docs/**`.", nil, d, ACTestState{})
	wantStatus(t, "glob matching none", got, ok, ACUnmet)
	got, ok = VerifyACAgainstDiff("`internal/**` is unchanged.", nil, d, ACTestState{})
	wantStatus(t, "unchanged glob with a diffed match", got, ok, ACUnmet)
	got, ok = VerifyACAgainstDiff("`docs/**` is unchanged.", nil, d, ACTestState{})
	wantStatus(t, "unchanged glob without a match", got, ok, ACMet)
	got, ok = VerifyACAgainstDiff("`**/*.go` files are covered.", nil, d, ACTestState{})
	wantStatus(t, "double-star prefix", got, ok, ACMet)
	if got, ok = VerifyACAgainstDiff("The linter skips `**/*.gen.ts`.", nil, d, ACTestState{}); ok {
		t.Errorf("descriptive glob matching none: want undecided, got %+v", got)
	}
}

func TestACRevisionRepo_WholeName(t *testing.T) {
	d := &ACDiff{Workdir: "/w", Repos: map[string]string{
		"nightgauge/nightgauge": "/core", "nightgauge/nightgauge-internal": "/internal"}}
	if p, _, ok := acRevisionRepo(acClause{Prose: "matches nightgauge-internal at the revision"}, d); !ok || p != "/internal" {
		t.Errorf("nightgauge-internal: got %q ok=%v", p, ok)
	}
	if p, _, ok := acRevisionRepo(acClause{Prose: "matches the nightgauge file at the revision"}, d); !ok || p != "/core" {
		t.Errorf("nightgauge: got %q ok=%v", p, ok)
	}
}

// #3042 AC 1: replay of #3062 criterion 1 (PR #3065) — the trigger is a
// nested key under an existing `on:`.
func TestVerifyACAgainstDiff_Issue3062MarketplaceWorkflowCall(t *testing.T) {
	const path = ".github/workflows/marketplace-publish.yml"
	base := "name: publish\non:\n  workflow_dispatch:\n    inputs:\n      registries:\n        type: string\njobs:\n  guard:\n    runs-on: ubuntu-latest\n"
	after := "name: publish\non:\n  workflow_dispatch:\n    inputs:\n      registries:\n        type: string\n  workflow_call:\n    inputs:\n      registries:\n        type: string\njobs:\n  guard:\n    runs-on: ubuntu-latest\n"
	dir := repo3042(t, map[string]string{path: base})
	writeFile(t, dir, path, after)
	d := collect(t, dir)
	crit := "`marketplace-publish.yml` accepts `on: workflow_call` with the same `registries` input, alongside `workflow_dispatch`. Every existing guard step runs unchanged under both triggers: …"
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "#3062 criterion", got, ok, ACMet)

	// Absent trigger: unmet.
	dir = repo3042(t, map[string]string{path: base})
	writeFile(t, dir, path, strings.Replace(base, "type: string\n", "type: string\n        default: npm\n", 1))
	got, ok = VerifyACAgainstDiff("`marketplace-publish.yml` accepts `on: workflow_call`.", nil, collect(t, dir), ACTestState{})
	wantStatus(t, "trigger absent", got, ok, ACUnmet)

	// Malformed YAML: the line matcher stands alone, so a nested key is not found.
	dir = repo3042(t, map[string]string{path: base})
	writeFile(t, dir, path, after+"  bad: [unclosed\n")
	got, ok = VerifyACAgainstDiff("`marketplace-publish.yml` accepts `on: workflow_call`.", nil, collect(t, dir), ACTestState{})
	wantStatus(t, "malformed YAML falls back", got, ok, ACUnmet)
}

// #3042 AC 2: a bare file name resolves to its unique diffed path, and is
// undecided when several diffed paths share it.
func TestVerifyACAgainstDiff_BareFileName(t *testing.T) {
	one := `diff --git a/.github/workflows/release.yml b/.github/workflows/release.yml
--- a/.github/workflows/release.yml
+++ b/.github/workflows/release.yml
@@ -0,0 +1 @@
+  attest: true
`
	d := memDiff(one, map[string]string{".github/workflows/release.yml": "  attest: true"})
	got, ok := VerifyACAgainstDiff("`release.yml` sets `attest: true`.", nil, d, ACTestState{})
	wantStatus(t, "unique", got, ok, ACMet)
	got, ok = VerifyACAgainstDiff("`release.yml` sets `sign: true`.", nil, d, ACTestState{})
	wantStatus(t, "unique, missing token", got, ok, ACUnmet)

	two := one + `diff --git a/templates/release.yml b/templates/release.yml
--- a/templates/release.yml
+++ b/templates/release.yml
@@ -0,0 +1 @@
+  sign: false
`
	d = memDiff(two, map[string]string{".github/workflows/release.yml": "  attest: true", "templates/release.yml": "  sign: false"})
	got, ok = VerifyACAgainstDiff("`release.yml` sets `sign: true`.", nil, d, ACTestState{})
	wantStatus(t, "ambiguous", got, ok, "")
	got, ok = VerifyACAgainstDiff("`release.yml` sets `attest: true`.", []string{"`templates/release.yml` is the template."}, d, ACTestState{})
	wantStatus(t, "ambiguous with inherited prior", got, ok, "")
}

// #3042 AC 5: `...` is a wildcard, never literal.
func TestACSpanMatcher_Ellipsis(t *testing.T) {
	m := acSpanMatcher("sha256=...")
	if !m("<!-- nightgauge-workspace-rules:end sha256=ab12 -->") {
		t.Error("`sha256=...` must match a marker carrying a digest")
	}
	if m("sha256=") || m("no digest here") {
		t.Error("`sha256=...` must need text where the ellipsis stands")
	}
	if acSpanMatcher("{...props}")("<A {x} />") || !acSpanMatcher("{...props}")("<A {...props} />") {
		t.Error("a spread span matches literally")
	}
	if acSpanMatcher("...")("any line") {
		t.Error("a bare ellipsis span is literal, never a wildcard")
	}
	if !acSpanMatcher("run(…)")("run(x)") {
		t.Error("a placeholder call span still matches by name")
	}
}

// #3042 AC 5: replay of the #1054 marker criterion.
func TestVerifyACAgainstDiff_EllipsisMarker(t *testing.T) {
	marker := "<!-- nightgauge-workspace-rules:end sha256=" + strings.Repeat("ab", 32) + " -->"
	d := memDiff("diff --git a/AGENTS.md b/AGENTS.md\n--- a/AGENTS.md\n+++ b/AGENTS.md\n@@ -0,0 +1 @@\n+"+marker+"\n",
		map[string]string{"AGENTS.md": marker})
	got, ok := VerifyACAgainstDiff("`AGENTS.md` ends the block with `<!-- nightgauge-workspace-rules:end sha256=... -->`.", nil, d, ACTestState{})
	wantStatus(t, "full marker", got, ok, ACMet)
	got, ok = VerifyACAgainstDiff("`AGENTS.md` records the `sha256=...` marker.", nil, d, ACTestState{})
	wantStatus(t, "short marker", got, ok, ACMet)
}

// #3042 AC 3: replay of #3064 (PR #3066) — a digest decided by hashing the
// named file at HEAD.
func TestVerifyACAgainstDiff_Issue3064Digest(t *testing.T) {
	const path = "claude-plugins/nightgauge/hooks/workflow-gate.sh"
	content := "#!/usr/bin/env bash\nexec nightgauge hook workflow-gate \"$@\"\n"
	dir := repo3042(t, nil)
	writeFile(t, dir, path, content)
	commitAll(t, dir)
	d := collect(t, dir)
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	crit := "`" + path + "` hashes to the pinned sha256 `" + digest + "`."
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "#3064 digest", got, ok, ACMet)
	got, ok = VerifyACAgainstDiff("`"+path+"` hashes to the pinned sha256 `"+strings.Repeat("0", 64)+"`.", nil, d, ACTestState{})
	wantStatus(t, "wrong digest", got, ok, ACUnmet)
	got, ok = VerifyACAgainstDiff("The hook hashes to the pinned sha256 `"+digest+"`.", nil, d, ACTestState{})
	wantStatus(t, "no named file", got, ok, "")
}

// #3042 AC 4: replay of #1054 (PR #1056) — a file byte-identical to the
// core's file at a commit.
func TestVerifyACAgainstDiff_Issue1054ByteIdentical(t *testing.T) {
	const path = "scripts/post-merge-check.sh"
	script := "#!/usr/bin/env bash\necho check\n"
	core := repo3042(t, map[string]string{path: script})
	sha := strings.TrimSpace(gitOut(t, core, "rev-parse", "HEAD"))
	gitOut(t, core, "checkout", "-q", "main")
	writeFile(t, core, path, "#!/usr/bin/env bash\necho newer\n")
	commitAll(t, core) // the core has moved on; the criterion pins the old commit

	crit := "`" + path + "` is byte-identical to the core's `" + path + "` at `" + sha + "` (`cmp` exits 0) and stays executable."
	dir := repo3042(t, nil)
	writeFile(t, dir, path, script)
	commitAll(t, dir)
	d := collect(t, dir)
	d.Repos = map[string]string{"nightgauge/nightgauge": core}
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "#1054 identical", got, ok, ACMet)

	writeFile(t, dir, path, "#!/usr/bin/env bash\necho drift\n")
	d = collect(t, dir)
	d.Repos = map[string]string{"nightgauge/nightgauge": core}
	got, ok = VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "differs", got, ok, ACUnmet)

	d.Repos = nil
	got, ok = VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "no checkout", got, ok, "")
}

// #3042 AC 4: a marker-bounded range is compared, not the whole file.
func TestVerifyACAgainstDiff_Issue1054WorkspaceMarkers(t *testing.T) {
	block := "<!-- nightgauge-workspace-rules:start -->\n- rule one\n<!-- nightgauge-workspace-rules:end sha256=" + strings.Repeat("cd", 32) + " -->\n"
	core := repo3042(t, map[string]string{"AGENTS.md": "# Core\n\n" + block + "\ncore prose\n"})
	sha := strings.TrimSpace(gitOut(t, core, "rev-parse", "HEAD"))
	dir := repo3042(t, nil)
	writeFile(t, dir, "AGENTS.md", "# Dashboard\n\n"+block+"\ndashboard prose\n")
	d := collect(t, dir)
	d.Repos = map[string]string{"nightgauge/nightgauge": core}
	crit := "In `AGENTS.md` the lines from `<!-- nightgauge-workspace-rules:start -->` through `<!-- nightgauge-workspace-rules:end sha256=... -->` are byte-identical to the core's block at `" + sha + "`."
	got, ok := VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "#1054 markers", got, ok, ACMet)

	writeFile(t, dir, "AGENTS.md", strings.Replace("# Dashboard\n\n"+block, "rule one", "rule 1", 1))
	d = collect(t, dir)
	d.Repos = map[string]string{"nightgauge/nightgauge": core}
	got, ok = VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "block differs", got, ok, ACUnmet)
}

func TestParseACClause_RevisionNeedsAdjacentWording(t *testing.T) {
	if c := parseACClause("The log at startup prints `abc1234`."); len(c.Revisions) != 0 {
		t.Errorf("a distant `at` must not make a revision: %+v", c.Revisions)
	}
	if c := parseACClause("`a.txt` matches the core's file at `abc1234`."); len(c.Revisions) != 1 {
		t.Errorf("adjacent `at` names a revision: %+v", c.Revisions)
	}
}

// Regression (#3385): a criterion asking for a test to be removed is met when
// the diff deletes it, unmet only while it is still defined, and never unmet
// merely because the test is absent.
func TestVerifyACAgainstDiff_RemovalCriterion(t *testing.T) {
	dir := changelogRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "parity_test.go", "package w\n\nfunc TestCheckParity_Foo(t *testing.T) {}\n\nfunc TestCheckParity_Bar(t *testing.T) {}\n")
	writeFile(t, dir, "keep_test.go", "package w\n\nfunc TestKeepMe(t *testing.T) {}\n")
	writeFile(t, dir, "half_test.go", "package w\n\nfunc TestHalf_Keep(t *testing.T) {}\n\nfunc TestHalf_Gone(t *testing.T) {}\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "tests")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	gitOut(t, dir, "merge", "-q", "main")
	gitOut(t, dir, "rm", "-q", "parity_test.go")
	writeFile(t, dir, "half_test.go", "package w\n\nfunc TestHalf_Keep(t *testing.T) {}\n")
	// A string literal holding the definition text is not a definition.
	writeFile(t, dir, "lit_test.go", "package w\n\nvar src = \"func TestCheckParity_Foo(t *testing.T) {}\"\n")
	d := collect(t, dir)
	failed := ACTestState{Source: "feature-validate unit tests"}

	got, ok := VerifyACAgainstDiff("`TestCheckParity_*` is removed.", nil, d, failed)
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "parity_test.go") {
		t.Errorf("removed prefix: got %+v ok=%v, want met citing parity_test.go", got, ok)
	}
	got, ok = VerifyACAgainstDiff("The `TestCheckParity_*` tests are removed.", nil, d, failed)
	if !ok || got.Status != ACMet {
		t.Errorf("removed with noun: got %+v ok=%v, want met", got, ok)
	}
	got, ok = VerifyACAgainstDiff("Delete `TestCheckParity_Foo`.", nil, d, failed)
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "parity_test.go") {
		t.Errorf("removed exact: got %+v ok=%v, want met", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`TestKeepMe` is removed.", nil, d, failed)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "keep_test.go:3") {
		t.Errorf("still defined: got %+v ok=%v, want unmet citing keep_test.go:3", got, ok)
	}
	if got, ok = VerifyACAgainstDiff("`TestNeverExisted` is removed.", nil, d, failed); ok && got.Status == ACUnmet {
		t.Errorf("absent, never in the diff: got %+v, must not be unmet", got)
	}
	// A partial prefix removal: the diff deletes TestHalf_Gone but
	// TestHalf_Keep survives, so the clause is unmet.
	got, ok = VerifyACAgainstDiff("`TestHalf_*` is removed.", nil, d, failed)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "half_test.go:3") {
		t.Errorf("partial prefix removal: got %+v ok=%v, want unmet citing half_test.go:3", got, ok)
	}
	// The target is the span the removal verb governs, not the first span.
	got, ok = VerifyACAgainstDiff("`TestKeepMe` stays and `TestCheckParity_Foo` is deleted.", nil, d, failed)
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "TestCheckParity_Foo") {
		t.Errorf("governed span: got %+v ok=%v, want met on TestCheckParity_Foo", got, ok)
	}
	// A clause that also asks for an addition is not decided by the removal.
	if got, ok = VerifyACAgainstDiff("Add `TestKeepMe` and remove `TestCheckParity_Foo`.", nil, d, failed); ok && got.Status != "" {
		t.Errorf("mixed add/remove: got %+v, want the judge", got)
	}
	// A removal scoped to a file in the diff is never answered by presence.
	if got, ok = VerifyACAgainstDiff("`TestHalf_Gone` is removed from `half_test.go`.", nil, d, failed); ok && got.Status == ACUnmet {
		t.Errorf("path-scoped removal: got %+v, must not be unmet", got)
	}
	// A test criterion that only mentions deletion is not a removal request.
	if got, ok = VerifyACAgainstDiff("`TestKeepMe` checks deleted rows are skipped.", nil, d, ACTestState{Passed: true}); ok && strings.Contains(got.Evidence, "still defined") {
		t.Errorf("mention of deletion routed as removal: got %+v", got)
	}
	got, ok = VerifyACAgainstDiff("`TestMissing` covers the deleted-file case.", nil, d, ACTestState{Passed: true})
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "not defined") {
		t.Errorf("non-removal missing test: got %+v ok=%v, want unmet", got, ok)
	}
}

// Regression (#3385): the other removal targets — a Python test, a bare Go
// identifier (receiver and prefix forms) — and the clauses that must not be
// routed as removals.
func TestVerifyACAgainstDiff_RemovalTargets(t *testing.T) {
	dir := changelogRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "test_util.py", "def test_foo():\n    pass\n")
	writeFile(t, dir, "h.go", "package w\n\nfunc (r *T) helperFn() {}\n\nfunc helperA() {}\n")
	writeFile(t, dir, "k.go", "package w\n\nfunc keepFn() {}\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "seed")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	gitOut(t, dir, "merge", "-q", "main")
	gitOut(t, dir, "rm", "-q", "test_util.py", "h.go")
	d := collect(t, dir)
	none := ACTestState{}

	for _, text := range []string{"`test_foo` is removed.", "`helperFn` is removed.", "`helper*` is removed."} {
		got, ok := VerifyACAgainstDiff(text, nil, d, none)
		if !ok || got.Status != ACMet {
			t.Errorf("%q: got %+v ok=%v, want met", text, got, ok)
		}
	}
	got, ok := VerifyACAgainstDiff("`keepFn` is removed.", nil, d, none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "k.go:3") {
		t.Errorf("still defined: got %+v ok=%v, want unmet citing k.go:3", got, ok)
	}
	// A named path sends the clause to the token rules, not acRemoved.
	if got, ok = VerifyACAgainstDiff("`keepFn` is removed from `k.go`.", nil, d, none); ok && strings.Contains(got.Evidence, "still defined") {
		t.Errorf("path-named removal routed to acRemoved: %+v", got)
	}
	// An additive criterion naming a removal-ish symbol is never met by absence.
	if got, ok = VerifyACAgainstDiff("function `RemoveStale` is added.", nil, d, none); ok && got.Status == ACMet {
		t.Errorf("additive RemoveStale met by its absence: %+v", got)
	}
}

// Regression (#3385 review): a path-scoped test removal is judged in the named
// file only, so a same-named test in another package never makes it unmet.
func TestVerifyACAgainstDiff_RemovalScopedToPath(t *testing.T) {
	dir := changelogRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a/a_test.go", "package a\n\nfunc TestParse(t *testing.T) {}\n\nfunc TestKeep(t *testing.T) {}\n")
	writeFile(t, dir, "b/b_test.go", "package b\n\nfunc TestParse(t *testing.T) {}\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "seed")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	gitOut(t, dir, "merge", "-q", "main")
	writeFile(t, dir, "a/a_test.go", "package a\n\nfunc TestKeep(t *testing.T) {}\n")
	d := collect(t, dir)
	none := ACTestState{}

	got, ok := VerifyACAgainstDiff("`TestParse` is removed from `a/a_test.go`.", nil, d, none)
	if !ok || got.Status != ACMet || strings.Contains(got.Evidence, "b/b_test.go") {
		t.Errorf("scoped removal: got %+v ok=%v, want met without citing b/b_test.go", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`TestKeep` is removed from `a/a_test.go`.", nil, d, none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "a/a_test.go:3") {
		t.Errorf("still in the named file: got %+v ok=%v, want unmet citing a/a_test.go:3", got, ok)
	}
	// Unscoped, the same-named test elsewhere still counts.
	got, ok = VerifyACAgainstDiff("`TestParse` is removed.", nil, d, none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "b/b_test.go:3") {
		t.Errorf("unscoped: got %+v ok=%v, want unmet citing b/b_test.go:3", got, ok)
	}
}

// Regression (#3385 Part 2): "no longer contains X" is met when X is absent
// from the named file on the branch, never judged by the diff adding X.
func TestVerifyACAgainstDiff_NegativeContent(t *testing.T) {
	dir := changelogRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "docs/X.md", "# X\n\nRun `git diff --diff-filter=U`.\n\nAgain --diff-filter=U here.\n")
	writeFile(t, dir, "docs/Gone.md", "uses --old-flag\n")
	writeFile(t, dir, "src.go", "package w\n// fooMarker\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "seed")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	gitOut(t, dir, "merge", "-q", "main")
	writeFile(t, dir, "docs/X.md", "# X\n\nRun `git diff --name-only`.\n")
	gitOut(t, dir, "rm", "-q", "docs/Gone.md")
	d := collect(t, dir)
	none := ACTestState{}

	got, ok := VerifyACAgainstDiff("`docs/X.md` no longer contains `--diff-filter=U`.", nil, d, none)
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "removed it from `docs/X.md`") {
		t.Errorf("both removed: got %+v ok=%v, want met", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`docs/Gone.md` no longer contains `--old-flag`.", nil, d, none)
	if !ok || got.Status != ACMet {
		t.Errorf("deleted file: got %+v ok=%v, want met", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`git grep -n fooMarker` returns no match.", nil, d, none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "src.go:2") {
		t.Errorf("grep present: got %+v ok=%v, want unmet citing src.go:2", got, ok)
	}
	got, ok = VerifyACAgainstDiff("`git grep -n neverThere` returns no match.", nil, d, none)
	if !ok || got.Status != ACMet {
		t.Errorf("grep absent: got %+v ok=%v, want met", got, ok)
	}
	// A test asserting an absence asks for the test, not for the absence.
	if got, ok = VerifyACAgainstDiff("`TestRedact` in `src.go` asserts the log does not contain `fooMarker`.", nil, d, none); ok && got.Status != "" {
		t.Errorf("test asserting absence: got %+v, want the judge", got)
	}
	// A positive criterion still goes to the token rule.
	got, ok = VerifyACAgainstDiff("`docs/X.md` contains `--name-only`.", nil, d, none)
	if !ok || got.Status != ACMet || strings.Contains(got.Evidence, "absent") {
		t.Errorf("positive: got %+v ok=%v, want met by the token rule", got, ok)
	}

	// The second of two paths still holds the token.
	got, ok = VerifyACAgainstDiff("`docs/Gone.md` and `src.go` no longer contain `fooMarker`.", nil, d, none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "src.go:2") {
		t.Errorf("second path: got %+v ok=%v, want unmet citing src.go:2", got, ok)
	}
	// No path and no git grep span: never met by the positive token rule.
	if got, ok = VerifyACAgainstDiff("The docs no longer mention `--name-only`.", nil, d, none); ok && got.Status == ACMet {
		t.Errorf("negative clause met by an added token: %+v", got)
	}

	// One occurrence left: unmet citing its line.
	writeFile(t, dir, "docs/X.md", "# X\n\nRun `git diff --name-only`.\n\nAgain --diff-filter=U here.\n")
	got, ok = VerifyACAgainstDiff("`docs/X.md` no longer contains `--diff-filter=U`.", nil, collect(t, dir), none)
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "docs/X.md:5") {
		t.Errorf("one left: got %+v ok=%v, want unmet citing docs/X.md:5", got, ok)
	}
}

func TestACGitGrepPattern(t *testing.T) {
	for tok, want := range map[string]string{
		"git grep -n foo":            "foo",
		"git grep -nF foo":           "foo",
		"git grep -e foo":            "foo",
		"git grep -n foo -- docs/":   "",
		"git grep -e foo src/":       "",
		"git grep -nF foo docs/x.md": "",
		"git grep -i FOO":            "",
		"git grep -n os.Exit":        "",
		"git grep -nF os.Exit":       "os.Exit",
		"git grep -w foo":            "",
		"git grep -E fo+":            "",
		"git grep 'two words'":       "",
		"git grep -A 3 foo":          "",
		"git grep -e a -e b":         "",
		"git grep -n":                "",
	} {
		if got := acGitGrepPattern(tok); got != want {
			t.Errorf("acGitGrepPattern(%q) = %q, want %q", tok, got, want)
		}
	}
}

// crit3422 is the #3422 criterion pr-create booked unmet on a diff that left
// the constant alone (#3423).
const crit3422 = "`DiffSecretsExcludePattern` keeps its current value, so the pinned test in " +
	"`internal/scan/diffscan_test.go` still passes unchanged"

const diffscanBase = "package scan\n\n" +
	"// DiffSecretsExcludePattern is the pinned exclude filter.\n" +
	"const DiffSecretsExcludePattern = `^(?:\\+\\+\\+|---) `\n\n" +
	"func matchLine(s string) bool {\n\treturn s != \"\"\n}\n"

const diffscanTestBase = "package scan\n\nimport \"testing\"\n\n" +
	"func TestPinnedPattern(t *testing.T) {\n\tif DiffSecretsExcludePattern == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n"

// scanRepo builds a repo whose main holds the #3422 files, with a feature
// branch checked out.
func scanRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "internal/scan/diffscan.go", diffscanBase)
	writeFile(t, dir, "internal/scan/diffscan_test.go", diffscanTestBase)
	writeFile(t, dir, "docs/SCAN.md", "# Scan\n\n* `DiffSecretsExcludePattern` excludes diff headers.\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "fix/3420")
	return dir
}

// #3422 regression: a changed sibling function and an added test leave an
// untouched constant preserved; editing the constant's line is unmet.
func TestVerifyACAgainstDiff_Issue3422Preservation(t *testing.T) {
	dir := scanRepo(t)
	writeFile(t, dir, "internal/scan/diffscan.go",
		strings.Replace(diffscanBase, "return s != \"\"", "return strings.TrimSpace(s) != \"\"", 1))
	writeFile(t, dir, "internal/scan/diffscan_test.go", diffscanTestBase+
		"\nfunc TestMatchLineBlank(t *testing.T) {\n\tif matchLine(\" \") {\n\t\tt.Fatal(\"blank\")\n\t}\n}\n")
	got, ok := VerifyACAgainstDiff(crit3422, nil, collect(t, dir), ACTestState{})
	if !ok || got.Status != ACMet {
		t.Fatalf("unchanged constant: got %+v ok=%v, want met", got, ok)
	}

	writeFile(t, dir, "internal/scan/diffscan.go",
		strings.Replace(diffscanBase, "`^(?:\\+\\+\\+|---) `", "`^(?:\\+\\+\\+) `", 1))
	got, ok = VerifyACAgainstDiff(crit3422, nil, collect(t, dir), ACTestState{})
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "internal/scan/diffscan.go:4") {
		t.Fatalf("edited constant: got %+v ok=%v, want unmet naming diffscan.go:4", got, ok)
	}
}

// "keep `X` and add `Y`" is judged on the addition: met only when `Y` is
// added, and never unmet because of the preserved `X`.
func TestVerifyACAgainstDiff_PreservationMixedClause(t *testing.T) {
	crit := "Keep `DiffSecretsExcludePattern` and add `matchHeader` in `internal/scan/diffscan.go`"
	dir := scanRepo(t)
	writeFile(t, dir, "internal/scan/diffscan.go", diffscanBase+"\nfunc matchHeader() {}\n")
	if got, ok := VerifyACAgainstDiff(crit, nil, collect(t, dir), ACTestState{}); !ok || got.Status != ACMet {
		t.Fatalf("addition present: got %+v ok=%v, want met", got, ok)
	}
	writeFile(t, dir, "internal/scan/diffscan.go", diffscanBase+"\nfunc other() {}\n")
	if got, ok := VerifyACAgainstDiff(crit, nil, collect(t, dir), ACTestState{}); !ok || got.Status != ACUnmet ||
		strings.Contains(got.Evidence, "DiffSecretsExcludePattern") {
		t.Fatalf("addition missing: got %+v ok=%v, want unmet on `matchHeader` only", got, ok)
	}
}

// A Markdown bullet is content: an edit to a bullet holding the preserved span
// is caught, while a Go comment naming it is not a change to it (#3423).
func TestVerifyACAgainstDiff_PreservationCommentsByLanguage(t *testing.T) {
	crit := "`DiffSecretsExcludePattern` remains as documented"
	dir := scanRepo(t)
	writeFile(t, dir, "docs/SCAN.md", "# Scan\n\n* `DiffSecretsExcludePattern` excludes diff headers and hunks.\n")
	got, ok := VerifyACAgainstDiff(crit, nil, collect(t, dir), ACTestState{})
	if !ok || got.Status != ACUnmet || !strings.Contains(got.Evidence, "docs/SCAN.md") {
		t.Fatalf("doc bullet edit: got %+v ok=%v, want unmet naming docs/SCAN.md", got, ok)
	}

	dir = scanRepo(t)
	writeFile(t, dir, "internal/scan/diffscan.go",
		strings.Replace(diffscanBase, "is the pinned exclude filter", "is the pinned exclude filter (#3420)", 1))
	if got, ok := VerifyACAgainstDiff(crit, nil, collect(t, dir), ACTestState{}); !ok || got.Status != ACMet {
		t.Fatalf("Go comment edit: got %+v ok=%v, want met", got, ok)
	}
}

func TestACCommentLine(t *testing.T) {
	for _, c := range []struct {
		path, line string
		want       bool
	}{
		{"a.go", "// X", true},
		{"a.go", " * X", true},
		{"a.go", "*p = X", false},
		{"a.ts", "/* X */", true},
		{"a.yml", "# X", true},
		{"Makefile", "# X", true},
		{"a.go", "# X", false},
		{"a.md", "* `X` bullet", false},
		{"a.md", "# X heading", false},
		{"a.md", "<!-- X -->", true},
		{"a.md", "// X", false},
	} {
		if got := acCommentLine(c.path, c.line); got != c.want {
			t.Errorf("acCommentLine(%q, %q) = %v, want %v", c.path, c.line, got, c.want)
		}
	}
}

// mergeRepo commits base on main, merges a branch applying change with a
// real merge commit, and returns the repo and that merge's SHA.
func mergeRepo(t *testing.T, change func(dir string)) (string, string) {
	t.Helper()
	dir := scanRepo(t)
	change(dir)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "change")
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "README.md", "main moved on\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "main")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "fix/3420")
	return dir, strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
}

func judgeOnMerge(t *testing.T, dir, merge, crit string) (bool, string) {
	t.Helper()
	d, err := CollectACDiffRange(context.Background(), dir, merge+"^1", merge)
	if err != nil {
		t.Fatal(err)
	}
	return VerifyACOnMerge(crit, d)
}

// The merged-diff judge answers only preservation and removal, and anything
// it cannot decide is unmet (#3423).
func TestVerifyACOnMerge(t *testing.T) {
	sibling := func(dir string) {
		writeFile(t, dir, "internal/scan/diffscan.go", strings.Replace(diffscanBase,
			"return s != \"\"", "return len(s) > 0", 1)+"\nfunc TestOld() {}\n")
	}
	dir, merge := mergeRepo(t, sibling)
	if met, ev := judgeOnMerge(t, dir, merge, crit3422); !met {
		t.Errorf("preservation over a sibling-only merge: unmet (%s), want met", ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "`NoSuchName` keeps its value"); met || !strings.Contains(ev, "NoSuchName") {
		t.Errorf("span on neither side: met=%v (%s), want unmet", met, ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "`matchLine` is removed"); met || !strings.Contains(ev, "still") {
		t.Errorf("removal of a span still present: met=%v (%s), want unmet", met, ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "`neverDefined` is removed"); met || !strings.HasPrefix(ev, "undecidable:") {
		t.Errorf("never-defined removal target: met=%v (%s), want undecidable", met, ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "Add `matchHeader` to `internal/scan/diffscan.go`"); met {
		t.Errorf("presence criterion: met (%s), want unmet", ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "The scanner is faster"); met || !strings.HasPrefix(ev, "undecidable:") {
		t.Errorf("criterion with no span: met=%v (%s), want undecidable", met, ev)
	}

	dir, merge = mergeRepo(t, func(dir string) {
		writeFile(t, dir, "internal/scan/diffscan.go",
			strings.Replace(diffscanBase, "`^(?:\\+\\+\\+|---) `", "`^---`", 1))
	})
	if met, ev := judgeOnMerge(t, dir, merge, crit3422); met || !strings.Contains(ev, "diffscan.go") {
		t.Errorf("merge edited the span's line: met=%v (%s), want unmet", met, ev)
	}

	dir, merge = mergeRepo(t, func(dir string) {
		writeFile(t, dir, "internal/scan/diffscan.go", strings.Replace(diffscanBase,
			"func matchLine(s string) bool {\n\treturn s != \"\"\n}\n", "", 1))
	})
	if met, ev := judgeOnMerge(t, dir, merge, "`matchLine` is removed"); !met {
		t.Errorf("removal present at ^1 and gone at merge: unmet (%s), want met", ev)
	}
}

func TestCollectACDiffRange_MissingCommit(t *testing.T) {
	dir := scanRepo(t)
	if _, err := CollectACDiffRange(context.Background(), dir, "0123456789abcdef0123456789abcdef01234567^1",
		"0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("a merge commit missing from the object store must be an error")
	}
	if met, ev := VerifyACOnMerge(crit3422, nil); met || !strings.HasPrefix(ev, "undecidable:") {
		t.Errorf("nil diff: met=%v (%s), want undecidable", met, ev)
	}
}

// "`p` is unchanged" on a merged diff: met when the merge left p alone and p
// exists, unmet when it changed p, undecidable when p is absent.
func TestVerifyACOnMerge_PathPreservation(t *testing.T) {
	dir, merge := mergeRepo(t, func(dir string) {
		writeFile(t, dir, "internal/scan/diffscan.go", diffscanBase+"\nfunc extra() {}\n")
	})
	if met, ev := judgeOnMerge(t, dir, merge, "`docs/SCAN.md` is unchanged"); !met {
		t.Errorf("untouched path: unmet (%s), want met", ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "`internal/scan/diffscan.go` is unchanged"); met || !strings.Contains(ev, "diffscan.go") {
		t.Errorf("changed path: met=%v (%s), want unmet naming it", met, ev)
	}
	if met, ev := judgeOnMerge(t, dir, merge, "`docs/MISSING.md` is unchanged"); met || !strings.HasPrefix(ev, "undecidable:") {
		t.Errorf("absent path: met=%v (%s), want undecidable", met, ev)
	}
}

func TestCollectACDiffRange_RejectsBadArguments(t *testing.T) {
	dir := scanRepo(t)
	for _, c := range [][3]string{{"", "HEAD^1", "HEAD"}, {dir, "", "HEAD"}, {dir, "HEAD^1", ""}, {dir, "--output=/tmp/x", "HEAD"}, {dir, "HEAD^1", "-p"}} {
		if _, err := CollectACDiffRange(context.Background(), c[0], c[1], c[2]); err == nil {
			t.Errorf("CollectACDiffRange(%q, %q, %q): want an error", c[0], c[1], c[2])
		}
	}
	for _, rev := range []string{"", "--all"} {
		if _, _, err := acGitGrepRev(dir, rev, "-F", "x", nil); err == nil {
			t.Errorf("acGitGrepRev rev %q: want an error", rev)
		}
	}
}

// Both collectors parse the same diff whatever prefixes the git config asks
// for (#3505). diff.mnemonicPrefix renames only a working-tree diff's
// prefixes; git already gives a commit range a/ and b/ under it.
func TestCollectACDiff_PinnedPrefixes(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := slugifyRepo(t)
	addTruncate(t, dir)
	gitOut(t, dir, "rm", "-q", "test/other.test.js")
	gitOut(t, dir, "mv", "test/get.test.js", "test/fetch.test.js")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "change")
	// Tracked, so it reaches the parser; an untracked file does not.
	writeFile(t, dir, "test/budget.test.js", "check(widget());\ncheck(budget());\n")

	both := func() (worktree, commits map[string]*ACFileDiff) {
		t.Helper()
		r, err := CollectACDiffRange(context.Background(), dir, "HEAD^1", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return collect(t, dir).Files, r.Files
	}
	wantWorktree, wantCommits := both()
	for path, status := range map[string]string{
		"src/index.js":          "modified",
		"test/truncate.test.js": "added",
		"test/other.test.js":    "deleted",
		"test/fetch.test.js":    "renamed",
	} {
		for name, files := range map[string]map[string]*ACFileDiff{"worktree": wantWorktree, "range": wantCommits} {
			if f := files[path]; f == nil || f.Status != status {
				t.Fatalf("baseline %s: %s = %+v, want %s", name, path, f, status)
			}
		}
	}
	if f := wantWorktree["test/budget.test.js"]; f == nil || len(f.Added) == 0 {
		t.Fatalf("baseline worktree lacks the uncommitted edit: %+v", f)
	}

	same := func(name string, got, want map[string]*ACFileDiff) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: read %d files, want %d", name, len(got), len(want))
		}
		for path, w := range want {
			if !reflect.DeepEqual(got[path], w) {
				t.Errorf("%s: %s = %+v, want %+v", name, path, got[path], w)
			}
		}
	}
	for _, key := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		gitOut(t, dir, "config", key, "true")
		worktree, commits := both()
		same(key+": CollectACDiff", worktree, wantWorktree)
		same(key+": CollectACDiffRange", commits, wantCommits)
		gitOut(t, dir, "config", "--unset", key)
	}
}

// A rewritten definition is seen, and unmet, under diff.noprefix (#3505).
func TestVerifyACOnMerge_NoPrefix(t *testing.T) {
	dir := slugifyRepo(t)
	gitOut(t, dir, "config", "diff.noprefix", "true")
	writeFile(t, dir, "src/index.js", "function slugify(s, sep) {}\n\nmodule.exports = { slugify };\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "rewrite slugify")
	commit := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	if met, ev := judgeOnMerge(t, dir, commit, "`slugify` is unchanged"); met || !strings.Contains(ev, "src/index.js") {
		t.Errorf("rewritten definition: met=%v (%s), want unmet naming src/index.js", met, ev)
	}
}

// A path git C-quotes is read like any other, so a change beside an ordinary
// file cannot hide in it (#3505).
func TestVerifyACOnMerge_QuotedPaths(t *testing.T) {
	dir := slugifyRepo(t)
	const edited, gone, from, to = `src/q"x.js`, `src/back\slash.js`, "src/tab\there.js", `src/to "b.js`
	writeFile(t, dir, edited, "function kebab(s) {}\n")
	writeFile(t, dir, gone, "gone();\n")
	writeFile(t, dir, from, "moved();\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "quoted paths")
	base := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))

	writeFile(t, dir, edited, "function kebab(s, sep) {}\n")
	writeFile(t, dir, "o.txt", "other\n")
	gitOut(t, dir, "rm", "-q", "--", gone)
	gitOut(t, dir, "mv", "--", from, to)
	gitOut(t, dir, "add", "o.txt")
	worktree, err := CollectACDiff(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if !worktree.Uncommitted[edited] {
		t.Errorf("%s is not uncommitted: %v", edited, worktree.Uncommitted)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "change")
	commit := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	merged, err := CollectACDiffRange(context.Background(), dir, base, commit)
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]*ACDiff{"worktree": worktree, "range": merged} {
		for path, status := range map[string]string{edited: "modified", gone: "deleted", to: "renamed"} {
			if f := d.Files[path]; f == nil || f.Status != status {
				t.Errorf("%s: %q = %+v, want %s", name, path, f, status)
			}
		}
		if f := d.Files[to]; f != nil && f.OldPath != from {
			t.Errorf("%s: renamed from %q, want %q", name, f.OldPath, from)
		}
	}
	if met, ev := VerifyACOnMerge("`kebab` is unchanged", merged); met || !strings.Contains(ev, edited) {
		t.Errorf("rewritten definition: met=%v (%s), want unmet naming %s", met, ev, edited)
	}
}

// No section takes another's place, whatever bytes its path holds (#3505).
func TestParseUnifiedDiff_SectionsKeepTheirPath(t *testing.T) {
	const victim = "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"
	for name, c := range map[string]struct {
		diff string
		want []string
	}{
		"non-UTF-8 quoted names": {
			"diff --git \"a/q\\\"\xff\" \"b/q\\\"\xff\"\n" + "diff --git \"a/q\\\"\xfe\" \"b/q\\\"\xfe\"\n",
			[]string{"q\"\xff", "q\"\xfe"},
		},
		"edit of a name holding b/": {
			victim + "diff --git a/x b/f b/x b/f\n--- a/x b/f\t\n+++ b/x b/f\t\n@@ -1 +1 @@\n-a\n+b\n",
			[]string{"f", "x b/f"},
		},
		"deletion of a name holding b/": {
			victim + "diff --git a/x b/f b/x b/f\ndeleted file mode 100644\n",
			[]string{"f", "x b/f"},
		},
		"rename to a name holding b/": {
			victim + "diff --git a/o b/x b/f\nrename from o\nrename to x b/f\n",
			[]string{"f", "x b/f"},
		},
		"type change": {
			"diff --git a/f b/f\ndeleted file mode 100644\n--- a/f\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n" +
				"diff --git a/f b/f\nnew file mode 120000\n--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+target\n",
			[]string{"f"},
		},
	} {
		files := parseUnifiedDiff(c.diff)
		if len(files) != len(c.want) {
			t.Errorf("%s: read %d files, want %d", name, len(files), len(c.want))
		}
		for _, p := range c.want {
			f := files[p]
			if f == nil {
				t.Errorf("%s: no %q", name, p)
			} else if p == "f" && !reflect.DeepEqual(f.Removed, []string{"old"}) {
				t.Errorf("%s: f lost its removed line: %+v", name, f)
			}
		}
	}
}

// A merged diff with no files proves nothing kept (#3505).
func TestVerifyACOnMerge_NoFiles(t *testing.T) {
	dir := slugifyRepo(t)
	for name, files := range map[string]map[string]*ACFileDiff{"nil": nil, "empty": {}} {
		d := &ACDiff{Files: files, Workdir: dir, BaseRev: "HEAD", FromRev: "HEAD", ToRev: "HEAD"}
		if met, ev := VerifyACOnMerge("`slugify` is unchanged", d); met || !strings.HasPrefix(ev, "undecidable:") {
			t.Errorf("%s files: met=%v (%s), want undecidable", name, met, ev)
		}
	}
}

func TestACPreservedTokens(t *testing.T) {
	for _, c := range []struct {
		text string
		want string // comma-joined governed spans
		pres bool   // acIsPreservation
	}{
		{"`X` keeps its current value", "X", true},
		{"Keep `X` and add `Y`", "X", true},
		{"`X` is unchanged", "X", true},
		{"The change does not change `X`", "X", true},
		{"`X`, and many other words here, stays", "", false},
		{"`X` is mentioned before a sentence. It stays", "", false},
		{"`npm test` still passes", "", false},
		{"`docs/A.md` is unchanged", "", false},
		{"`TestPinned` still passes", "TestPinned", false},
		{"`go test -run TestX` still passes", "", false},
	} {
		cl := parseACClause(c.text)
		if got := strings.Join(acPreservedTokens(cl), ","); got != c.want {
			t.Errorf("acPreservedTokens(%q) = %q, want %q", c.text, got, c.want)
		}
		if got := acIsPreservation(cl); got != c.pres {
			t.Errorf("acIsPreservation(%q) = %v, want %v", c.text, got, c.pres)
		}
	}
}

// The three criteria of the G1 fixture, verbatim (#3501).
var g1ACs = []string{
	"`test/truncate.test.js` covers: unchanged when short enough, truncated with " +
		"`…` when too long, the boundary (`text.length === maxLength` is unchanged), " +
		"the `TypeError` and the `RangeError`.",
	"`npm test` passes with the existing `slugify` tests untouched.",
	"`README.md` lists `truncate` next to `slugify`.",
}

const slugifyTest = "const { slugify } = require(\"../src\");\n" +
	"test(\"lower-cases\", () => equal(slugify(\"Hi\"), \"hi\"));\n" +
	"test(\"hyphenates\", () => equal(slugify(\"a b\"), \"a-b\"));\n"

// slugifyRepo builds the G1 shape: slugify and its test on main, with a
// feature branch checked out.
func slugifyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "src/index.js", "function slugify(s) {}\n\nmodule.exports = { slugify };\n")
	writeFile(t, dir, "README.md", "# Fixture\n\n`src/index.js` exports `slugify`.\n")
	writeFile(t, dir, "test/slugify.test.js", slugifyTest)
	writeFile(t, dir, "test/case conversion.js", "// helpers\ncheck(kebab(\"A B\"));\n")
	writeFile(t, dir, "test/other.test.js", "check(other());\n")
	writeFile(t, dir, "test/get.test.js", "check(get());\n")
	writeFile(t, dir, "test/budget.test.js", "check(widget());\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "feat/truncate")
	return dir
}

// onBase runs change on main, commits it and restarts the feature branch there.
func onBase(t *testing.T, dir string, change func(t *testing.T, dir string)) {
	t.Helper()
	gitOut(t, dir, "checkout", "-q", "main")
	change(t, dir)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base change")
	gitOut(t, dir, "checkout", "-q", "-B", "feat/truncate")
}

// addTruncate makes the change the G1 issue asks for.
func addTruncate(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "src/index.js",
		"function slugify(s) {}\n\nfunction truncate(s, n) {}\n\nmodule.exports = { slugify, truncate };\n")
	writeFile(t, dir, "README.md", "# Fixture\n\n`src/index.js` exports `slugify` and `truncate`.\n")
	writeFile(t, dir, "test/truncate.test.js", "// unchanged when short, boundary text.length === maxLength\n"+
		"equal(truncate(\"hello\", 2), \"h…\");\nthrows(TypeError);\nthrows(RangeError);\n")
}

// "`X` tests untouched" preserves X's test files, not every line naming X
// (#3501).
func TestVerifyACAgainstDiff_TestScopedPreservation(t *testing.T) {
	const slug = "test/slugify.test.js"
	const strict = "changes a line containing"
	const stillPass = "`slugify` tests still pass"
	const mustPass = "the `slugify` tests must remain and pass"
	editTest := func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }
	deleteTest := func(t *testing.T, dir string) { gitOut(t, dir, "rm", "-q", slug) }
	emptyTest := func(t *testing.T, dir string) { writeFile(t, dir, slug, "") }
	oversized := strings.Repeat("// filler\n", acReadMaxBytes/10+1)
	addOversized := func(t *testing.T, dir string) { writeFile(t, dir, "test/z.test.js", oversized+"slugify(1)\n") }
	unknownLayout := func(t *testing.T, dir string) {
		gitOut(t, dir, "rm", "-q", slug)
		writeFile(t, dir, "src/case.rs", "#[test]\nfn keeps() { slugify(1) }\n")
	}
	const example = "# Fixture\n\n    test(\"example\", () => slugify(1))\n"
	rewrite := func(t *testing.T, dir string) { writeFile(t, dir, "src/index.js", "function slugify(s) { throw s }\n") }
	write := func(p, body string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) { writeFile(t, dir, p, body) }
	}
	move := func(from, to string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(to)), 0o755); err != nil {
				t.Fatal(err)
			}
			gitOut(t, dir, "mv", from, to)
		}
	}
	const untouched = "the `slugify` tests are untouched"
	const swift = "Tests/SlugifyTests.swift"
	for _, tc := range []struct {
		name   string
		crit   string // default: criterion 2
		only   bool   // change is the whole diff, without addTruncate
		base   func(t *testing.T, dir string)
		change func(t *testing.T, dir string)
		want   string
		names  string
	}{
		{name: "criterion 1", crit: g1ACs[0], want: ACMet},
		{name: "criterion 2", want: ACMet, names: slug},
		{name: "criterion 3", crit: g1ACs[2], want: ACMet},
		{name: "modified", want: ACUnmet, names: "modifies `" + slug + "`",
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "comment only", want: ACUnmet, names: slug,
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, "// note\n"+slugifyTest) }},
		{name: "span removed", want: ACUnmet, names: slug,
			change: func(t *testing.T, dir string) {
				writeFile(t, dir, slug, strings.ReplaceAll(slugifyTest, "slugify", "kebab"))
			}},
		{name: "deleted", want: ACUnmet, names: "deletes `" + slug + "`",
			change: func(t *testing.T, dir string) { gitOut(t, dir, "rm", "-q", slug) }},
		{name: "renamed with edits", want: ACUnmet, names: "renames `" + slug + "`",
			change: func(t *testing.T, dir string) {
				gitOut(t, dir, "mv", slug, "test/slug.test.js")
				writeFile(t, dir, "test/slug.test.js", slugifyTest+"check(1);\n")
			}},
		{name: "untracked and rewritten", want: ACUnmet, names: slug,
			change: func(t *testing.T, dir string) {
				gitOut(t, dir, "rm", "-q", "--cached", slug)
				writeFile(t, dir, slug, "check(1);\n")
			}},
		{name: "unrelated test edit", want: ACMet, names: slug,
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/other.test.js", "check(other(2));\n") }},
		{name: "content match, path with a space", crit: "The `kebab` tests are untouched",
			want: ACUnmet, names: "test/case conversion.js",
			change: func(t *testing.T, dir string) {
				writeFile(t, dir, "test/case conversion.js", "// helpers\ncheck(kebab(\"C D\"));\n")
			}},
		{name: "content match kept", crit: "The `kebab()` tests are untouched", want: ACMet, names: "test/case conversion.js"},
		{name: "pure rename", want: ACUnmet, names: "renames `" + slug + "`",
			change: func(t *testing.T, dir string) { gitOut(t, dir, "mv", slug, "test/slug.test.js") }},
		{name: "path match only", crit: "The `conversion` specs are untouched", want: ACMet, names: "test/case conversion.js"},
		{name: "both readings met", crit: "`kebab` tests are untouched and keep `kebab`",
			want: ACMet, names: "changes no line containing"},
		{name: "tests and a second span", crit: "`exports` tests are untouched and keep `Fixture`",
			want: ACUnmet, names: strict},
		{name: "name inside another word", crit: "`get()` tests are untouched", want: ACMet, names: "test/get.test.js",
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/budget.test.js", "check(total(2));\n") }},
		{name: "still pass, case appended", crit: "The `slugify` tests still pass",
			want: ACUnmet, names: "modifies `" + slug + "`",
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "still pass, implementation changed", crit: "The `slugify` tests still pass",
			want: ACMet, names: slug,
			change: func(t *testing.T, dir string) { writeFile(t, dir, "src/index.js", "function slugify(s) { throw s }\n") }},
		{name: "stay green", crit: "The `slugify` tests stay green", want: ACMet, names: slug},
		{name: "still pass unchanged", crit: "The `slugify` test suite still passes unchanged",
			want: ACUnmet, names: "modifies `" + slug + "`",
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "untouched and still pass", crit: "The `slugify` tests untouched and still pass", want: ACMet, names: slug},
		{name: "test as a modifier", crit: "The `slugify` test flag is unchanged",
			want: ACUnmet, names: "changes a line containing"},
		{name: "spec that may not be a test", crit: "The `slugify` spec is unchanged", want: ACUnmet, names: strict},
		{name: "spec named by no changed file", crit: "The `kebab` spec is unchanged", want: ACMet, names: "test/case conversion.js"},
		{name: "spec, test file edited", crit: "The `slugify` spec is unchanged", want: ACUnmet, names: slug,
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "bare name inside another word", crit: "The `get` tests are untouched", want: ACMet, names: "test/get.test.js",
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/budget.test.js", "check(widget(1));\n") }},
		{name: "coordinated after", crit: "The `slugify` tests and behavior remain unchanged", want: ACUnmet, names: strict},
		{name: "coordinated object", crit: "Keep `slugify` tests and implementation", want: ACUnmet, names: strict},
		{name: "coordinated before", crit: "The implementation and `slugify` tests are untouched", want: ACUnmet, names: strict},
		{name: "coordinated, test edited", crit: "The `slugify` tests and behavior remain unchanged", want: ACUnmet, names: slug,
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "tests as a modifier", crit: "The `slugify` tests flag is unchanged",
			want: ACUnmet, names: "changes a line containing"},
		{name: "keep passing", crit: "Keep the `slugify` tests passing",
			want: ACUnmet, names: "modifies `" + slug + "`",
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "keep green", crit: "Keep `slugify` tests green",
			want: ACUnmet, names: "modifies `" + slug + "`",
			change: func(t *testing.T, dir string) { writeFile(t, dir, slug, slugifyTest+"check(1);\n") }},
		{name: "specs may be documents", crit: "The `slugify` specs are unchanged", want: ACUnmet, names: strict},
		{name: "spec file may be a document", crit: "The `slugify` spec file is unchanged", want: ACUnmet, names: strict},
		{name: "document named for the span", crit: "The `kebab` spec is unchanged", want: ACMet, names: "changes no line containing",
			change: func(t *testing.T, dir string) { writeFile(t, dir, "docs/kebab.yaml", "type: string\n") }},
		{name: "no base test", crit: "The `truncate` tests are untouched", want: ACUnmet, names: strict},
		{name: "scoped and unscoped readings", crit: "`slugify` tests are untouched and keep `slugify`",
			want: ACUnmet, names: "changes a line containing"},
		{name: "tests and behavior, only a new test names it", crit: "The `kebab` tests and behavior remain unchanged",
			want: ACUnmet, names: strict,
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/new.test.js", "check(kebab(1));\n") }},

		// A qualified test noun is scoped.
		{name: "qualified", crit: "The existing `slugify` unit tests are untouched", want: ACMet, names: slug},
		{name: "qualified, test edited", crit: "The existing `slugify` unit tests are untouched",
			want: ACUnmet, names: "modifies `" + slug + "`", change: editTest},
		{name: "two qualifiers", crit: "The `slugify` browser regression tests are untouched", want: ACMet, names: slug},
		{name: "two qualifiers, test edited", crit: "The `slugify` browser regression tests are untouched",
			want: ACUnmet, names: "modifies `" + slug + "`", change: editTest},
		{name: "qualified object", crit: "Keep the `slugify` integration tests", want: ACMet, names: slug},
		{name: "qualified result", crit: "`slugify` unit tests remain green", want: ACMet, names: slug},
		{name: "qualified result, test edited", crit: "`slugify` unit tests remain green",
			want: ACUnmet, names: "modifies `" + slug + "`", change: editTest},
		{name: "qualified result object", crit: "Keep the `slugify` unit tests passing", want: ACMet, names: slug},
		{name: "qualified result object, test edited", crit: "Keep the `slugify` unit tests passing",
			want: ACUnmet, names: "modifies `" + slug + "`", change: editTest},

		// A word that starts a subordinate clause after the span keeps the span.
		{name: "once", crit: "Keep `slugify` once tests pass",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "whenever", crit: "Keep `slugify` whenever tests pass",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "whilst", crit: "Keep `slugify` whilst tests pass",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "provided", crit: "Keep `slugify` provided tests pass",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "unlisted word, finite result", crit: "Keep `slugify` frozen tests must pass",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "unlisted word, finite predicate", crit: "Keep `slugify` frozen tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "conjunction without a verb", crit: "Keep `slugify` once tests green",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "conjunction after the subject", crit: "`slugify` once tests pass remains unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},

		// A lone suite may be no test suite, and an excluded one is not kept.
		{name: "lone suite", crit: "The `slugify` suite is unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "qualified lone suite", crit: "The `slugify` cipher suite is unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "exclusion word", crit: "`slugify` unlike tests are unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "plural tests, singular verb", crit: "`slugify` frozen tests is unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},

		// A clause that preserves anything else keeps the strict verdict.
		{name: "and before the span", crit: "The implementation and the associated `slugify` tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "preservation word before the span", crit: "Behavior is unchanged with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "criterion 2, implementation rewritten", only: true, change: rewrite, want: ACMet, names: slug},
		{name: "preserved before the span", crit: "Behavior is preserved with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "keep with another object", crit: "Keep behavior with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "or before the span", crit: "The API or the `slugify` tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "comma and before the span", crit: "The implementation, and the associated `slugify` tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "qualified, and before the span", crit: "The API and the `slugify` unit tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "keep takes the tests", crit: "Keep the `slugify` tests untouched",
			only: true, change: rewrite, want: ACMet, names: slug},
		{name: "semicolon starts a clause", crit: "`npm test` passes; the `slugify` tests are untouched",
			only: true, change: rewrite, want: ACMet, names: slug},

		// Only a known lead, tail and sibling clause leave the tests alone kept.
		{name: "kept thing after comma and", crit: "Keep the `slugify` tests, and the implementation, untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "so is the next clause", crit: "The `slugify` tests are untouched, and so is the implementation",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "semicolon continues", crit: "The `slugify` tests are untouched; so is the implementation",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "semicolon before", crit: "The API is as before; the `slugify` tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "alongside", crit: "The implementation alongside the `slugify` tests is untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "slash", crit: "The implementation/`slugify` tests are untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "in addition to", crit: "The implementation in addition to the `slugify` tests is untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "must not change", crit: "The API must not change with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "no change", crit: "No behavior change with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "identical", crit: "Behavior is identical with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "as is", crit: "The API as is with `slugify` tests untouched",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "and before the kept word", crit: "The `slugify` tests and is unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "verb as a qualifier", crit: "`slugify` ignoring tests is unchanged",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "result and more", crit: "Keep `slugify` tests passing and its API stable",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "still pass and more", crit: "The `slugify` tests still pass and its API stays",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "result, next clause", crit: "Keep the `slugify` tests passing, and its API too",
			only: true, change: rewrite, want: ACUnmet, names: strict},
		{name: "result clause after", crit: "The `slugify` tests are untouched, and `npm test` passes",
			only: true, change: rewrite, want: ACMet, names: slug},

		// Rule 1: no decision from the scope is the strict verdict.
		{name: "rule 1a, oversized test edited", crit: "The `slugify` tests are untouched",
			only: true, base: addOversized, want: ACUnmet, names: strict + " `slugify` at `test/z.test.js",
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/z.test.js", oversized+"slugify(2)\n") }},
		{name: "rule 1a, oversized test after a kept one", crit: "The `slugify` tests are untouched",
			base: addOversized, want: ACUnmet, names: strict},
		{name: "rule 1b, no test holds the span", crit: "The `slugify` tests are untouched",
			base: deleteTest, want: ACUnmet, names: strict},
		{name: "rule 1b, unknown layout only", crit: "The `slugify` tests are untouched",
			only: true, base: unknownLayout, want: ACUnmet, names: strict + " `slugify` at `src/case.rs",
			change: func(t *testing.T, dir string) {
				writeFile(t, dir, "src/case.rs", "#[test]\nfn keeps() { slugify(2) }\n")
			}},
		{name: "rule 1b, strict is met", crit: "The `slugify` tests are untouched",
			only: true, base: deleteTest, want: ACMet, names: "changes no line containing",
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/other.test.js", "check(other(2));\n") }},

		// Rule 2: a test file is known by its path, and a document is never one.
		{name: "rule 2, README test example", crit: "the `slugify` tests are untouched",
			only: true, want: ACMet, names: slug,
			base:   func(t *testing.T, dir string) { writeFile(t, dir, "README.md", example) },
			change: func(t *testing.T, dir string) { writeFile(t, dir, "README.md", example+"`slugify` is exported.\n") }},
		{name: "rule 2, test plan", crit: "the `slugify` tests are untouched",
			only: true, want: ACMet, names: slug,
			base:   func(t *testing.T, dir string) { writeFile(t, dir, "docs/test-plan.md", "Test `slugify`.\n") },
			change: func(t *testing.T, dir string) { writeFile(t, dir, "docs/test-plan.md", "Test `slugify` twice.\n") }},
		{name: "rule 2, document in a test directory", crit: "the `slugify` tests are untouched",
			only: true, want: ACMet, names: slug,
			base:   func(t *testing.T, dir string) { writeFile(t, dir, "test/README.md", "`slugify` cases\n") },
			change: func(t *testing.T, dir string) { writeFile(t, dir, "test/README.md", "All `slugify` cases\n") }},
		{name: "rule 2, test in a docs package", crit: untouched, only: true,
			base:   write("internal/docs/slug_test.go", "slugify(1)\n"),
			change: write("internal/docs/slug_test.go", "slugify(2)\n"),
			want:   ACUnmet, names: "modifies `internal/docs/slug_test.go`"},
		{name: "rule 2, code and README only", crit: untouched, only: true, want: ACMet, names: slug,
			change: func(t *testing.T, dir string) {
				rewrite(t, dir)
				writeFile(t, dir, "README.md", "`slugify` is exported.\n")
			}},

		// Rule 2: a touched test-like path the list does not know is strict.
		{name: "test-like, Tests/*.swift", crit: untouched, only: true,
			base: write(swift, "slugify(1)\n"), change: write(swift, "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `" + swift},
		{name: "test-like, *Test.java", crit: untouched, only: true,
			base: write("src/SlugifyTest.java", "slugify(1)\n"), change: write("src/SlugifyTest.java", "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `src/SlugifyTest.java"},
		{name: "test-like, *.cy.js", crit: untouched, only: true,
			base: write("cypress/slugify.cy.js", "slugify(1)\n"), change: write("cypress/slugify.cy.js", "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `cypress/slugify.cy.js"},
		{name: "test-like, e2e", crit: untouched, only: true,
			base: write("e2e/slugify.js", "slugify(1)\n"), change: write("e2e/slugify.js", "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `e2e/slugify.js"},
		{name: "test-like, deleted", crit: untouched, only: true,
			base:   write(swift, "slugify(1)\n"),
			change: func(t *testing.T, dir string) { gitOut(t, dir, "rm", "-q", swift) },
			want:   ACUnmet, names: "removes a line containing `slugify` from `" + swift},
		{name: "test-like, emptied", crit: untouched, only: true,
			base: write(swift, "slugify(1)\n"), change: write(swift, ""),
			want: ACUnmet, names: "removes a line containing `slugify` from `" + swift},
		{name: "test-like, renamed away", crit: untouched,
			base: write(swift, "slugify(1)\n"), change: move(swift, "checks/slug.swift"),
			want: ACUnmet, names: strict},
		{name: "test-like, renamed into", crit: untouched,
			base: write("checks/slug.js", "slugify(1)\n"), change: move("checks/slug.js", "e2e/slug.js"),
			want: ACUnmet, names: strict},
		{name: "test-like, edit adds the span", crit: untouched, only: true,
			base: write("e2e/case.js", "check(1)\n"), change: write("e2e/case.js", "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `e2e/case.js"},
		{name: "test-like, span in the path only", crit: untouched,
			base: write("e2e/slugify.js", "check(1)\n"), change: write("e2e/slugify.js", "check(2)\n"),
			want: ACUnmet, names: strict},
		{name: "test-like, span inside a word", crit: untouched,
			base: write("e2e/case.js", "check(1)\nreslugify()\n"), change: write("e2e/case.js", "check(2)\nreslugify()\n"),
			want: ACUnmet, names: strict},
		{name: "test-like, unreadable", crit: untouched,
			base: write("e2e/case.js", oversized), change: write("e2e/case.js", oversized+"check(2)\n"),
			want: ACUnmet, names: strict},
		{name: "test-like, strict is met", crit: untouched, only: true,
			base: write("e2e/case.js", "check(1)\nslugify()\n"), change: write("e2e/case.js", "check(2)\nslugify()\n"),
			want: ACMet, names: "changes no line containing"},
		{name: "test-like by substring", crit: untouched, only: true,
			base: write("src/latest.js", "slugify(1)\n"), change: write("src/latest.js", "slugify(2)\n"),
			want: ACUnmet, names: strict + " `slugify` at `src/latest.js"},
		{name: "test-like without the span", crit: untouched,
			base: write("e2e/case.js", "check(1)\n"), change: write("e2e/case.js", "check(2)\n"),
			want: ACMet, names: slug},
		{name: "test-like, not touched", crit: untouched,
			base: write(swift, "slugify(1)\n"), want: ACMet, names: slug},
		{name: "test-like, added", crit: untouched,
			change: write("Tests/NewTests.swift", "slugify(1)\n"), want: ACMet, names: slug},
		{name: "test-like beside an edited test", crit: untouched, only: true,
			base: write(swift, "slugify(1)\n"),
			change: func(t *testing.T, dir string) {
				writeFile(t, dir, swift, "slugify(2)\n")
				editTest(t, dir)
			},
			want: ACUnmet, names: "modifies `" + slug + "`"},

		// Rule 3: a kept result is judged like any other scoped clause.
		{name: "rule 3, still pass, deleted", crit: stillPass, change: deleteTest,
			want: ACUnmet, names: "deletes `" + slug + "`"},
		{name: "rule 3, still pass, emptied", crit: stillPass, change: emptyTest,
			want: ACUnmet, names: "modifies `" + slug + "`"},
		{name: "rule 3, still pass, only deleted", crit: stillPass, only: true, change: deleteTest,
			want: ACUnmet, names: "deletes `" + slug + "`"},
		{name: "rule 3, still pass, kept", crit: stillPass, want: ACMet, names: slug},
		{name: "rule 3, remain and pass, deleted", crit: mustPass, change: deleteTest,
			want: ACUnmet, names: "deletes `" + slug + "`"},
		{name: "rule 3, remain and pass, emptied", crit: mustPass, change: emptyTest,
			want: ACUnmet, names: "modifies `" + slug + "`"},
		{name: "rule 3, remain and pass, only emptied", crit: mustPass, only: true, change: emptyTest,
			want: ACUnmet, names: "modifies `" + slug + "`"},
		{name: "rule 3, remain and pass, kept", crit: mustPass, want: ACMet, names: slug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := slugifyRepo(t)
			if tc.base != nil {
				onBase(t, dir, tc.base)
			}
			if !tc.only {
				addTruncate(t, dir)
			}
			if tc.change != nil {
				tc.change(t, dir)
			}
			crit, prior := tc.crit, []string(nil)
			switch crit {
			case "":
				crit, prior = g1ACs[1], g1ACs[:1]
			case g1ACs[2]:
				prior = g1ACs[:2]
			}
			got, ok := VerifyACAgainstDiff(crit, prior, collect(t, dir), ACTestState{})
			if !ok || got.Status != tc.want || !strings.Contains(got.Evidence, tc.names) {
				t.Fatalf("got %+v ok=%v, want %q naming %q", got, ok, tc.want, tc.names)
			}
		})
	}
}

// A failed read gives the strict verdict, never met or undecided (#3501).
func TestACTestsPreserved_NoBase(t *testing.T) {
	dir := slugifyRepo(t)
	addTruncate(t, dir)
	for name, spoil := range map[string]func(d *ACDiff){
		"empty base":   func(d *ACDiff) { d.BaseRev = "" },
		"option base":  func(d *ACDiff) { d.BaseRev = "--all" },
		"unknown base": func(d *ACDiff) { d.BaseRev = "no-such-rev" },
		"no workdir":   func(d *ACDiff) { d.Workdir = "" },
	} {
		d := collect(t, dir)
		spoil(d)
		if r := acTestsPreserved("slugify", d); r.ok {
			t.Errorf("%s: got %+v, want no decision", name, r)
		}
		r := acPreserved(parseACClause(g1ACs[1]), d)
		if r.status != ACUnmet || !strings.Contains(r.evidence, "changes a line containing") {
			t.Errorf("%s: got %+v, want the strict unmet", name, r)
		}
	}
}

// git grep skips a blob it cannot read and exits as if it had searched it
// (#3501).
func TestACTestsPreserved_UnreadableBlob(t *testing.T) {
	const lost = "test/z.test.js"
	for _, tc := range []struct {
		name      string
		otherKept bool
	}{{"only candidate", false}, {"another test kept", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := slugifyRepo(t)
			onBase(t, dir, func(t *testing.T, dir string) {
				writeFile(t, dir, lost, "check(slugify(\"lost\"))\n")
				if !tc.otherKept {
					gitOut(t, dir, "rm", "-q", "test/slugify.test.js")
				}
			})
			writeFile(t, dir, lost, "check(slugify(\"found\"))\n")
			// Committed, so only git grep has to read the lost blob: a dirty
			// file makes the git diff that lists touched paths fail first.
			gitOut(t, dir, "commit", "-qam", "edit")
			d := collect(t, dir)
			sha := strings.TrimSpace(gitOut(t, dir, "rev-parse", d.BaseRev+":"+lost))
			if err := os.Remove(filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])); err != nil {
				t.Fatal(err)
			}
			r := acPreserved(parseACClause("The `slugify` tests are untouched"), d)
			if r.status != ACUnmet || !strings.Contains(r.evidence, "changes a line containing") {
				t.Errorf("got %+v, want the strict unmet", r)
			}
		})
	}
}

func TestACPreservedSpans_TestNoun(t *testing.T) {
	for text, want := range map[string]bool{
		"`X` test is unchanged":            true,
		"`X` tests are untouched":          true,
		"`X` Tests are untouched":          true,
		"`X` test files are unchanged":     true,
		"`X` test suite is unchanged":      true,
		"`X` suite is unchanged":           true,
		"`X` spec is unchanged":            true,
		"`X` specs are unchanged":          true,
		"Keep `X` tests.":                  true,
		"The existing `X` tests stay":      true,
		"`X` test cases are unchanged":     true,
		"Keep `X` test, as before":         true,
		"`X` test flag is unchanged":       false,
		"`X` test helper stays":            false,
		"`X` spec version is unchanged":    false,
		"`X` suite name is unchanged":      false,
		"`X` is unchanged":                 false,
		"`X` tests-only mode is unchanged": false,
		"`X` testing stays":                false,
		"`X`'s tests are unchanged":        false,
		"Keep `X`: tests depend on it":     false,

		"`X` unit tests are untouched":               true,
		"`X` browser regression tests are untouched": true,
		"`X` stress tests are unchanged":             true,
		"Keep all the existing `X` tests untouched":  true,
	} {
		got := acPreservedSpans(parseACClause(text))
		if len(got) != 1 || got[0].testScoped != want {
			t.Errorf("acPreservedSpans(%q) = %+v, want testScoped=%v", text, got, want)
		}
	}
	// These may govern no span at all; none may be read as tests.
	for _, text := range []string{
		"`X` fast browser regression tests are untouched",
		"`X` end-to-end tests are unchanged",
		"`X` and tests are unchanged",
		"Keep `X` out of tests",
		"`X` during tests is unchanged",
		"`X` needs tests and is unchanged",
		"`X` gained tests and is unchanged",
		"`X` currently tests are unchanged",
		"`X` unchanged tests",
		"`X` unit test flag is unchanged",
		"`X` unit spec is unchanged",
		"Behavior is preserved, `X` tests untouched",
	} {
		for _, s := range acPreservedSpans(parseACClause(text)) {
			if s.testScoped {
				t.Errorf("acPreservedSpans(%q) = %+v, want no testScoped reading", text, s)
			}
		}
	}
}

func TestParseACClauses_AfterAnd(t *testing.T) {
	got := parseACClauses("`a, and b` is set, and `c` stays; `d` and `e` stay, and `f` stays")
	want := []bool{false, true, false, true}
	if len(got) != len(want) {
		t.Fatalf("got %d clauses, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.AfterAnd != want[i] {
			t.Errorf("clause %q: AfterAnd=%v, want %v", c.Raw, c.AfterAnd, want[i])
		}
	}
}

func TestACTestFileRE(t *testing.T) {
	for p, want := range map[string]bool{
		"test/a.js": true, "pkg/tests/a": true, "src/__tests__/a.ts": true, "spec/a.rb": true,
		"pkg/a_test.go": true, "src/a.test.js": true, "src/a.spec.ts": true, "py/test_a.py": true,
		"src/a.js": false, "latest/a.js": false, "src/contest.js": false, "testdata/a.txt": false,
		"src/test_dir/a.js": false, "specs/a.md": false, "src/testing.go": false, "test": false,
	} {
		if got := acTestFileRE.MatchString(p); got != want {
			t.Errorf("test file %q = %v, want %v", p, got, want)
		}
	}
}

// A document is never a test file, wherever it lives (#3501).
func TestACIsTestFile(t *testing.T) {
	for p, want := range map[string]bool{
		"test/a.js": true, "pkg/a_test.go": true, "test/docstring.py": true, "src/docs.test.js": true,
		"test/README.md": false, "test/A.MD": false, "test/a.markdown": false, "test/a.rst": false,
		"test/a.adoc": false, "test/notes.txt": false, "docs/test-plan.md": false, "src/case.rs": false,
		"internal/docs/slug_test.go": true, "docs/test/a.js": true, "doc/a_test.go": true, "pkg/Docs/a.test.js": true,
		"Tests/SlugifyTests.swift": false, "src/SlugifyTest.java": false,
		"cypress/slugify.cy.js": false, "e2e/slugify.js": false,
	} {
		if got := acIsTestFile(p); got != want {
			t.Errorf("acIsTestFile(%q) = %v, want %v", p, got, want)
		}
	}
}

// The test-like list is closed (#3501): five substrings of the path, and no
// documentation extension.
func TestACIsTestLike(t *testing.T) {
	for p, want := range map[string]bool{
		"src/checks/slug.js": false, "src/index.js": false, "docs/slug.go": false, "src/latest.js": true,
		"Tests/SlugifyTests.swift": true, "src/SlugifyTest.java": true,
		"cypress/slugify.cy.js": true, "e2e/slugify.js": true,
		"src/slug.CY.ts": true, "E2E/slug.js": true, "Cypress/slug.js": true, "Sources/CaseSpec.scala": true,
		"test/a.js": true, "pkg/tests/a": true, "src/__tests__/a.ts": true, "spec/a.rb": true,
		"pkg/a_test.go": true, "src/a.test.js": true, "src/a.spec.ts": true, "py/test_a.py": true,
		"internal/docs/slug_test.go": true, "docs/test-plan.md": false,
		"spec/a.markdown": false, "e2e/a.rst": false,
		"cypress/a.adoc": false, "src/a.cy.txt": false, "Tests/A.MD": false,
	} {
		if got := acIsTestLike(p); got != want {
			t.Errorf("acIsTestLike(%q) = %v, want %v", p, got, want)
		}
	}
}

// The pre-filter must not drop a line the span matcher accepts (#3501).
func TestACGrepLiteral(t *testing.T) {
	for tok, line := range map[string]string{
		"slugify": "slugify(x)", "obj.name()": "other.name (1)", "{sessionJwt}": "[{ sessionJwt: [] }]",
		"mode:fast": `"Mode": "fast"`, "begin-…-end": "begin-42-end",
	} {
		if !acSpanMatcher(tok)(line) || !strings.Contains(line, acGrepLiteral(tok)) {
			t.Errorf("span %q: literal %q misses %q", tok, acGrepLiteral(tok), line)
		}
	}
}

func TestVerifyACOnMerge_TestScopedPreservation(t *testing.T) {
	for _, touched := range []bool{false, true} {
		dir := slugifyRepo(t)
		addTruncate(t, dir)
		if touched {
			writeFile(t, dir, "test/slugify.test.js", slugifyTest+"check(1);\n")
		}
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "commit", "-qm", "change")
		gitOut(t, dir, "checkout", "-q", "main")
		gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feat/truncate")
		met, ev := judgeOnMerge(t, dir, "HEAD", g1ACs[1])
		if met == touched || !strings.Contains(ev, "test/slugify.test.js") {
			t.Errorf("touched=%v: met=%v (%s)", touched, met, ev)
		}
		// The first clause is met on the merge, so only the split-off one can fail.
		const joined = "`Fixture` is unchanged, and the `slugify` tests are untouched"
		if met, ev := judgeOnMerge(t, dir, "HEAD", joined); met || !touched && !strings.Contains(ev, "changes a line containing") {
			t.Errorf("touched=%v: %q met=%v (%s), want the strict verdict", touched, joined, met, ev)
		}
	}
}

// The merged-diff judge fails a kept result on a deleted test (#3501).
func TestVerifyACOnMerge_ResultOnlyTestScope(t *testing.T) {
	dir := slugifyRepo(t)
	gitOut(t, dir, "rm", "-q", "test/slugify.test.js")
	gitOut(t, dir, "commit", "-qm", "delete")
	gitOut(t, dir, "checkout", "-q", "main")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feat/truncate")
	met, ev := judgeOnMerge(t, dir, "HEAD", "`slugify` tests still pass")
	if met || !strings.Contains(ev, "deletes `test/slugify.test.js`") {
		t.Errorf("met=%v (%s), want unmet naming the test", met, ev)
	}
}

// git quotes such a path in diff headers, so "untouched" cannot be read and
// the strict verdict stands (#3501).
func TestACTestsPreserved_QuotedPath(t *testing.T) {
	dir := slugifyRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, `test/say "hi".js`, "greet()\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "quoted")
	gitOut(t, dir, "checkout", "-q", "-B", "feat/truncate")
	addTruncate(t, dir)
	d := collect(t, dir)
	if r := acTestsPreserved("greet", d); r.ok {
		t.Errorf("got %+v, want no decision", r)
	}
	r := acPreserved(parseACClause("The `greet` tests are untouched"), d)
	if r.status != ACMet || !strings.Contains(r.evidence, "changes no line containing") {
		t.Errorf("got %+v, want the strict met", r)
	}
}

// A kept file that sorts first must not hide an edited one whose path git
// quotes (#3501).
func TestACTestsPreserved_EditedQuotedPath(t *testing.T) {
	for _, odd := range []string{`test/z "q".test.js`, "test/z\rq.test.js"} {
		dir := slugifyRepo(t)
		gitOut(t, dir, "checkout", "-q", "main")
		writeFile(t, dir, odd, "slugify()\n")
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "commit", "-qm", "quoted")
		gitOut(t, dir, "checkout", "-q", "-B", "feat/truncate")
		addTruncate(t, dir)
		writeFile(t, dir, odd, "slugify(1)\n")
		if r := acTestsPreserved("slugify", collect(t, dir)); r.status == ACMet {
			t.Errorf("%q: got %+v, want no met", odd, r)
		}
	}
}

// A layout the test-file rule does not know: alone it leaves the strict
// verdict, and beside a kept test it does so only when its path is test-like
// (#3501).
func TestVerifyACAgainstDiff_UnrecognisedTestFile(t *testing.T) {
	const slug = "test/slugify.test.js"
	for _, tc := range []struct {
		path, body string
		testLike   bool
	}{
		{path: "Tests/SlugifyTests.swift", testLike: true},
		{path: "src/FooTest.java", testLike: true},
		{path: "Sources/CaseSpec.scala", testLike: true},
		{path: "src/slugify.cy.ts", testLike: true},
		{path: "src/SlugifyIT.java"},
		{path: "t/basic.t"},
		{path: "features/case.feature"},
		{path: "src/case.rs", body: "#[cfg(test)]\nmod checks {}\n"},
		{path: "src/case.py", body: "def test_case():\n    pass\n"},
		{path: "src/case.ts", body: "if (import.meta.vitest) {}\n"},
		{path: "src/Case.java", body: "@Test\nvoid keeps() {}\n"},
	} {
		p, body := tc.path, tc.body
		for _, knownKept := range []bool{false, true} {
			dir := slugifyRepo(t)
			onBase(t, dir, func(t *testing.T, dir string) {
				writeFile(t, dir, p, body+"slugify(1)\n")
				if !knownKept {
					gitOut(t, dir, "rm", "-q", slug)
				}
			})
			writeFile(t, dir, p, body+"slugify(2)\n")
			got, ok := VerifyACAgainstDiff("The `slugify` tests are untouched", nil, collect(t, dir), ACTestState{})
			want, names := ACUnmet, "changes a line containing `slugify` at `"+p
			if knownKept && !tc.testLike {
				want, names = ACMet, slug
			}
			if !ok || got.Status != want || !strings.Contains(got.Evidence, names) {
				t.Errorf("%s, knownKept=%v: got %+v ok=%v, want %s naming %q", p, knownKept, got, ok, want, names)
			}
		}
	}
}

// A touched test-like path git quotes cannot be read from the diff, nor can
// a rename's quoted base path, so neither is met by the scope (#3501).
func TestACTestsPreserved_TestLikeQuotedPath(t *testing.T) {
	for name, tc := range map[string]struct{ base, to string }{
		"edited":            {base: `e2e/z "q".js`},
		"renamed into e2e/": {base: `z "q".js`, to: "e2e/z.js"},
	} {
		dir := slugifyRepo(t)
		onBase(t, dir, func(t *testing.T, dir string) {
			writeFile(t, dir, tc.base, "slugify()\n")
			writeFile(t, dir, "e2e/other.js", "check()\n")
		})
		addTruncate(t, dir)
		if tc.to != "" {
			gitOut(t, dir, "mv", tc.base, tc.to)
		} else {
			writeFile(t, dir, tc.base, "slugify(1)\n")
		}
		d := collect(t, dir)
		if r := acTestsPreserved("slugify", d); r.ok {
			t.Errorf("%s: got %+v, want no decision", name, r)
		}
		if r := acPreserved(parseACClause(g1ACs[1]), d); r.status != ACUnmet {
			t.Errorf("%s: got %+v, want the strict unmet", name, r)
		}
	}
}

// The merged-diff judge has the same test-like rule (#3501).
func TestVerifyACOnMerge_TestLike(t *testing.T) {
	const p = "Tests/SlugifyTests.swift"
	dir := slugifyRepo(t)
	onBase(t, dir, func(t *testing.T, dir string) { writeFile(t, dir, p, "slugify(1)\n") })
	writeFile(t, dir, p, "slugify(2)\n")
	gitOut(t, dir, "commit", "-qam", "edit")
	gitOut(t, dir, "checkout", "-q", "main")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feat/truncate")
	met, ev := judgeOnMerge(t, dir, "HEAD", "the `slugify` tests are untouched")
	if met || !strings.Contains(ev, p) {
		t.Errorf("met=%v (%s), want the strict unmet naming the file", met, ev)
	}
}

// git quotes the new path, so the diff's headers never record the rename (#3501).
func TestACTestsPreserved_RenamedToQuotedPath(t *testing.T) {
	for _, otherKept := range []bool{false, true} {
		dir := slugifyRepo(t)
		if otherKept {
			gitOut(t, dir, "checkout", "-q", "main")
			writeFile(t, dir, "test/a.test.js", "slugify()\n")
			gitOut(t, dir, "add", "-A")
			gitOut(t, dir, "commit", "-qm", "second")
			gitOut(t, dir, "checkout", "-q", "-B", "feat/truncate")
		}
		addTruncate(t, dir)
		gitOut(t, dir, "mv", "test/slugify.test.js", `test/z "q".test.js`)
		r := acTestsPreserved("slugify", collect(t, dir))
		if r.status != ACUnmet || !strings.Contains(r.evidence, "test/slugify.test.js") {
			t.Errorf("otherKept=%v: got %+v, want unmet naming the base path", otherKept, r)
		}
	}
}
