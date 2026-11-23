package stages

import (
	"strings"
	"testing"
)

// diff3526 is PR #3526's `scripts/vt-scan.sh` change (merged as 8d39899d),
// cut to the hunks #3519's AC1 reads: the header comment that says
// `POST .../analyse`, and the re-check loop whose unchanged request line
// spells the placeholder `$sha`, and whose unchanged echo carries `!ml`.
const diff3526 = `diff --git a/scripts/vt-scan.sh b/scripts/vt-scan.sh
--- a/scripts/vt-scan.sh
+++ b/scripts/vt-scan.sh
@@ -1,4 +1,5 @@
 # the poll pass the scan waits VT_ML_RECHECK_DELAY once for the batch, asks for
-# one reanalysis of each such file (POST .../analyse, counted as a reanalysis,
+# for one reanalysis of every such file (POST .../analyse, counted as a
+# reanalysis) before it polls any of them.
 while IFS=$'\t' read -r sha before first; do
     vt_request POST "$API_BASE/files/$sha/analyse"
@@ -6,4 +7,4 @@
     expect_analysis "POST /files/$sha/analyse for $path"
     echo "  reanalysing $path ($sha): with only Microsoft !ml findings"
-    await_report "$sha" "$before" reanalysed-after-ml "$ANALYSIS_ID"
+    printf '%s\t%s\n' "$sha" "$ANALYSIS_ID" >>"$ML_SUBMITTED"
 done <"$ML_PENDING"
`

var content3526 = strings.Join([]string{
	"# the poll pass the scan waits VT_ML_RECHECK_DELAY once for the batch, asks for",
	"# for one reanalysis of every such file (POST .../analyse, counted as a",
	"# reanalysis) before it polls any of them.",
	"while IFS=$'\\t' read -r sha before first; do",
	`    vt_request POST "$API_BASE/files/$sha/analyse"`,
	"",
	`    expect_analysis "POST /files/$sha/analyse for $path"`,
	`    echo "  reanalysing $path ($sha): with only Microsoft !ml findings"`,
	`    printf '%s\t%s\n' "$sha" "$ANALYSIS_ID" >>"$ML_SUBMITTED"`,
	`done <"$ML_PENDING"`,
}, "\n")

// #3529 A1: on main this returned unmet with "the diff does not add
// `POST /files/{sha}/analyse` (looked in `scripts/vt-scan.sh` and every other
// changed file)". The cause: a `{name}` placeholder inside a span was matched
// as literal text, so a span whose placeholder the code spells `$sha` was
// judged absent.
func TestVerifyACAgainstDiff_3526BracePlaceholder(t *testing.T) {
	d := memDiff(diff3526, map[string]string{"scripts/vt-scan.sh": content3526})
	ac1 := "AC1: In the first-sight `!ml` re-check, `vt-scan.sh` sends the reanalysis request " +
		"(`POST /files/{sha}/analyse`) for **every** pending file before it polls any of them."
	got, ok := VerifyACAgainstDiff(ac1, nil, d, ACTestState{Passed: true})
	if ok && got.Status == ACUnmet {
		t.Fatalf("AC1 judged unmet from the diff: %+v", got)
	}
}

// #3529: a `{ident}` joined to other text stands for one segment, so it
// matches the code's spelling of that segment, and still its own literal text.
func TestACSpanMatcher_BracePlaceholder(t *testing.T) {
	cases := []struct {
		tok, line string
		want      bool
	}{
		{"POST /files/{sha}/analyse", `vt_request POST "$API_BASE/files/$sha/analyse"`, false},
		{"POST /files/{sha}/analyse", `expect_analysis "POST /files/$sha/analyse for $path"`, true},
		{"POST /files/{sha}/analyse", `POST /files/${sha}/analyse`, true},
		{"POST /files/{sha}/analyse", `POST /files/{sha}/analyse`, true},
		{"POST /files/{sha}/analyse", `POST /files/a/b/analyse`, false},
		{"POST /files/{sha}/analyse", `POST /files//analyse`, false},
		{"/users/{id}", `router.Get("/users/{id}", h)`, true},
		{"/users/{id}", `router.Get("/users/:id", h)`, true},
		{"/users/{id}", `router.Get("/users", h)`, false},
		{"{sessionJwt}", `[{ sessionJwt: [] }]`, true},
		{"{sessionJwt}", `sessionJwtX`, false},
		{"map[string]{}", `x := map[string]{}`, true},
		{"Config{Name}", `Config{Name}`, true},
		{"Config{Name}", `Config{Other}`, false},
		{"config.{env}.yaml", `load("config.prod.yaml")`, true},
		{"config.{env}.yaml", `load("config.yaml")`, false},
		{"release-{version}", `tag release-1.2.3`, true},
		{"release-{version}", `tag release`, false},
		{"/repos/{owner}/{repo}/pulls", `GET /repos/acme/widgets/pulls`, true},
		{"/repos/{owner}/{repo}/pulls", `GET /repos/acme/pulls`, false},
	}
	for _, c := range cases {
		if got := acSpanMatcher(c.tok)(c.line); got != c.want {
			t.Errorf("acSpanMatcher(%q)(%q) = %v, want %v", c.tok, c.line, got, c.want)
		}
	}
}

// acGrepLiteral must hold on every line the matcher accepts, so the grep
// pre-filter drops no placeholder match (#3529).
func TestACGrepLiteral_BracePlaceholder(t *testing.T) {
	for tok, want := range map[string]string{
		"POST /files/{sha}/analyse":   "POST /files",
		"/repos/{owner}/{repo}/pulls": "/repos", // first of the longest runs
		"/{id}":                       "/{id}",  // no literal run: the span itself
		"{sessionJwt}":                "sessionJwt",
	} {
		if got := acGrepLiteral(tok); got != want {
			t.Errorf("acGrepLiteral(%q) = %q, want %q", tok, got, want)
		}
	}
}

// #3529 A2: a backticked token the diff removes from one line and adds on
// another line of the same file is present. This already held on main
// (acAddedToken scans every added line); the test pins it.
func TestVerifyACAgainstDiff_MovedLine(t *testing.T) {
	d := memDiff(`diff --git a/scripts/run.sh b/scripts/run.sh
--- a/scripts/run.sh
+++ b/scripts/run.sh
@@ -1,3 +1,3 @@
-await_report "$sha"
 echo start
+await_report "$sha"
`, map[string]string{"scripts/run.sh": "echo start\nawait_report \"$sha\"\n"})
	got, ok := VerifyACAgainstDiff("`scripts/run.sh` calls `await_report` after `echo start`.",
		nil, d, ACTestState{Passed: true})
	if !ok || got.Status != ACMet {
		t.Fatalf("got %+v ok=%v, want met", got, ok)
	}
}

// #3529 B1: a clause about the pull request's own description, body or title
// is never judged from the diff.
func TestVerifyACAgainstDiff_DescriptionClause(t *testing.T) {
	d := memDiff(`diff --git a/docs/a.md b/docs/a.md
--- a/docs/a.md
+++ b/docs/a.md
@@ -1,0 +2 @@
+- adds the --dry-run flag
`, map[string]string{"docs/a.md": "# A\n- adds the --dry-run flag\n"})
	for _, text := range []string{
		"The description says `Closes #42`.",
		"The description says `Refs #42`.",
		"The PR body says `Refs #42`.",
		"The pull request's title names `--wet-run`.",
		"The PR description says `Closes #42`.",
		// Naming a diffed file made the diff search it for the keyword.
		"The PR description links `docs/a.md` and says `Closes #42`.",
		"The pull request body names `docs/a.md` and says `Refs #42`.",
	} {
		if got, ok := VerifyACAgainstDiff(text, nil, d, ACTestState{Passed: true}); ok {
			t.Errorf("%q: got %+v, want no diff verdict", text, got)
		}
	}
	// A clause about a named file's description is still judged.
	if got, ok := VerifyACAgainstDiff("In `docs/a.md` the description says `--wet-run`.", nil, d,
		ACTestState{Passed: true}); !ok || got.Status != ACUnmet {
		t.Errorf("file clause: got %+v ok=%v, want unmet", got, ok)
	}
}
