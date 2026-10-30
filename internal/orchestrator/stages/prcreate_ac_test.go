package stages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

func TestResolveACVerdicts(t *testing.T) {
	criteria := []string{"first", "second", "third"}
	det := func(classes ...string) *preflight.ACReconcileResult {
		r := &preflight.ACReconcileResult{}
		for i, c := range classes {
			r.AcceptanceCriteria = append(r.AcceptanceCriteria, preflight.ReconciledCriterion{
				Index: i, Classification: c, Reason: "rule says " + c, Evidence: []string{},
			})
		}
		return r
	}
	cases := []struct {
		name     string
		criteria []string
		validate []ACVerdict
		det      *preflight.ACReconcileResult
		want     []string // status/source per criterion
	}{
		{
			name:     "an unmet from any source wins",
			criteria: criteria,
			validate: []ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "unmet"}, {Index: 2, Status: "met"}},
			det:      det(preflight.ACUnsatisfied, preflight.ACSatisfied, preflight.ACUndetectable),
			want:     []string{"unmet/deterministic", "unmet/feature-validate", "met/feature-validate"},
		},
		{
			name:     "validate only",
			criteria: criteria,
			validate: []ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "not_verifiable"}, {Index: 2, Status: "unmet"}},
			want:     []string{"met/feature-validate", "not_verifiable/feature-validate", "unmet/feature-validate"},
		},
		{
			name:     "neither source",
			criteria: criteria,
			det:      det(preflight.ACUndetectable, preflight.ACPartial, preflight.ACUndetectable),
			want:     []string{"not_verifiable/none", "not_verifiable/none", "not_verifiable/none"},
		},
		{
			name:     "synonyms and unknown status",
			criteria: criteria,
			validate: []ACVerdict{{Index: 0, Status: "Satisfied"}, {Index: 1, Status: "unsatisfied"}, {Index: 2, Status: "maybe"}},
			want:     []string{"met/feature-validate", "unmet/feature-validate", "not_verifiable/feature-validate"},
		},
		{
			name:     "zero criteria",
			validate: []ACVerdict{{Index: 0, Status: "met"}},
			want:     nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveACVerdicts(c.criteria, c.validate, c.det, nil)
			var flat []string
			for i, v := range got {
				if v.Index != i || v.Text != c.criteria[i] {
					t.Errorf("verdict %d = index %d text %q", i, v.Index, v.Text)
				}
				flat = append(flat, v.Status+"/"+v.Source)
			}
			if !reflect.DeepEqual(flat, c.want) {
				t.Errorf("got %v, want %v", flat, c.want)
			}
		})
	}
}

// githubCloses is an oracle for GitHub's documented closing-keyword grammar:
// one of the nine keywords, an optional colon, then `#N`, `owner/repo#N` or an
// issue URL. It scans the text by hand instead of sharing forge's regular
// expression, so a form the matcher and the defanger both miss still fails
// the tests that assert against it. A Refs body it accepts would still close
// issue n at merge.
func githubCloses(body string, n int) bool {
	lower := strings.ToLower(body)
	num := strconv.Itoa(n)
	isWord := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z'
	}
	// endsRef reports whether rest begins with the issue number as a whole number.
	endsRef := func(rest string) bool {
		return strings.HasPrefix(rest, num) && (len(rest) == len(num) || !isWord(rest[len(num)]))
	}
	for _, kw := range []string{"close", "closes", "closed", "fix", "fixes", "fixed", "resolve", "resolves", "resolved"} {
		for i := 0; ; {
			j := strings.Index(lower[i:], kw)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(kw)
			i = start + 1
			if (start > 0 && isWord(lower[start-1])) || (end < len(lower) && isWord(lower[end])) {
				continue
			}
			rest := strings.TrimLeft(lower[end:], " \t")
			rest = strings.TrimLeft(strings.TrimPrefix(rest, ":"), " \t")
			if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
				// https://github.com/owner/repo/issues/N; GitHub acts on no other host.
				parts := strings.SplitN(rest[strings.Index(rest, "//")+2:], "/", 5)
				if len(parts) == 5 && parts[0] == "github.com" && parts[3] == "issues" && endsRef(parts[4]) {
					return true
				}
				continue
			}
			// #N, or owner/repo#N with no whitespace inside the reference.
			if hash := strings.IndexByte(rest, '#'); hash >= 0 &&
				!strings.ContainsAny(rest[:hash], " \t\n") &&
				(hash == 0 || strings.Count(rest[:hash], "/") == 1) && endsRef(rest[hash+1:]) {
				return true
			}
		}
	}
	return false
}

func TestGithubClosesOracle(t *testing.T) {
	for body, want := range map[string]bool{
		"Closes #42":    true,
		"fixes: o/r#42": true,
		"Resolved https://github.com/o/r/issues/42": true,
		"Refs #42":                              false,
		"Closes #420":                           false,
		"Closes https://github.com/o/r/pull/42": false,
		"disclosed #42":                         false,
	} {
		if got := githubCloses(body, 42); got != want {
			t.Errorf("githubCloses(%q) = %v, want %v", body, got, want)
		}
	}
}

// Regression fixture (#2595): an issue with three ACs and a diff that meets
// one must produce Refs, not Closes, and name the two that remain.
func TestRenderBody_PartialDeliveryRefsNotCloses(t *testing.T) {
	s := richSnap()
	s.NativeParent = 7
	s.ACVerdicts = ResolveACVerdicts(
		[]string{"Add the widget", "Document the widget", "Test the widget"},
		[]ACVerdict{
			{Index: 0, Status: "met", Evidence: "widget.go:12"},
			{Index: 1, Status: "unmet", Evidence: "no docs change"},
			{Index: 2, Status: "unmet"},
		}, nil, nil)
	body := RenderBody(s)

	if !strings.Contains(body, "Refs #42\n") {
		t.Errorf("partial delivery must say Refs #42:\n%s", body)
	}
	if githubCloses(body, 42) {
		t.Errorf("partial delivery body carries a closing keyword for #42:\n%s", body)
	}
	if !strings.Contains(body, "Part of #7\n") {
		t.Errorf("Part of the parent is unchanged:\n%s", body)
	}
	_, unmet, ok := strings.Cut(body, UnmetACHeading+"\n")
	if !ok {
		t.Fatalf("body has no unmet section:\n%s", body)
	}
	for _, want := range []string{"- Document the widget\n", "- Test the widget\n"} {
		if !strings.Contains(unmet, want) {
			t.Errorf("unmet section missing %q", want)
		}
	}
	if strings.Contains(unmet, "- Add the widget") {
		t.Error("the met criterion is listed as unmet")
	}
	if !strings.Contains(body, "- ✅ met: Add the widget (feature-validate: widget.go:12)\n") {
		t.Errorf("AC section missing the met line with evidence:\n%s", body)
	}
}

func TestRenderBody_AllMetCloses(t *testing.T) {
	s := richSnap()
	s.ACVerdicts = ResolveACVerdicts([]string{"a", "b"}, []ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "met"}}, nil, nil)
	body := RenderBody(s)
	if !strings.Contains(body, "Closes #42\n") || strings.Contains(body, UnmetACHeading) {
		t.Errorf("all-met body must close with no unmet section:\n%s", body)
	}
}

func TestIssueClosedBy(t *testing.T) {
	met := ACVerdict{Status: ACMet}
	nv := ACVerdict{Status: ACNotVerifiable}
	un := ACVerdict{Status: ACUnmet}
	for name, tc := range map[string]struct {
		in   []ACVerdict
		want bool
	}{
		"nil":                {nil, false},
		"all met":            {[]ACVerdict{met, met}, true},
		"all not verifiable": {[]ACVerdict{nv, nv}, false},
		"met and nv":         {[]ACVerdict{met, nv}, false},
		"any unmet":          {[]ACVerdict{met, un}, false},
	} {
		if got := IssueClosedBy(tc.in); got != tc.want {
			t.Errorf("%s: IssueClosedBy = %v, want %v", name, got, tc.want)
		}
	}
}

func TestNormalizeACStatus_NegativeSynonyms(t *testing.T) {
	for _, s := range []string{"not met", "Not-Met", "partial", "partially met", "partially_met", "partially-met", "missing"} {
		if got := NormalizeACStatus(s); got != ACUnmet {
			t.Errorf("NormalizeACStatus(%q) = %q, want unmet", s, got)
		}
	}
}

func TestResolveACVerdicts_NegativeEvidenceFolds(t *testing.T) {
	got := ResolveACVerdicts([]string{"a"}, []ACVerdict{{Index: 0, Status: "not_verifiable", Evidence: "criterion 2 is not met"}}, nil, nil)
	if got[0].Status != ACUnmet {
		t.Errorf("negative evidence must resolve unmet, got %q", got[0].Status)
	}
	det := &preflight.ACReconcileResult{AcceptanceCriteria: []preflight.ReconciledCriterion{{Index: 0, Classification: preflight.ACSatisfied, Reason: "found"}}}
	got = ResolveACVerdicts([]string{"a"}, []ACVerdict{{Index: 0, Status: "not_verifiable", Evidence: "not met"}}, det, nil)
	if got[0].Status != ACUnmet || got[0].Source != ACSourceValidate {
		t.Errorf("folded unmet must win over deterministic satisfied (#2665), got %+v", got[0])
	}
}

// openBullets mirrors hooks.UnmetCriteria: bullet lines under the heading.
func openBullets(body string) []string {
	i := strings.Index(body, UnmetACHeading)
	if i < 0 {
		return nil
	}
	sec := body[i+len(UnmetACHeading):]
	if j := strings.Index(sec, "\n## "); j >= 0 {
		sec = sec[:j]
	}
	var out []string
	for _, l := range strings.Split(sec, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "- ") && !strings.HasPrefix(l, " ") {
			out = append(out, strings.TrimPrefix(t, "- "))
		}
	}
	return out
}

func TestRenderBody_NotVerifiableRefsAndListsOnce(t *testing.T) {
	s := richSnap()
	s.ACVerdicts = ResolveACVerdicts([]string{"alpha crit", "beta crit"}, nil, nil, nil)
	body := RenderBody(s)
	if !strings.Contains(body, "Refs #42\n") || BodyClosesIssue(body, "o/r", 42) {
		t.Errorf("all-not-verifiable must say Refs:\n%s", body)
	}
	if strings.Contains(body, "## Acceptance criteria") {
		t.Errorf("no met AC must render no met section:\n%s", body)
	}
	for _, c := range []string{"alpha crit", "beta crit"} {
		if n := strings.Count(body, c); n != 1 {
			t.Errorf("%q rendered %d times:\n%s", c, n, body)
		}
	}
	if got := openBullets(body); len(got) != 2 || got[0] != "alpha crit" || got[1] != "beta crit" {
		t.Errorf("open bullets = %q", got)
	}
}

func TestRenderBody_MixedRefsEachOnce(t *testing.T) {
	s := richSnap()
	s.ACVerdicts = ResolveACVerdicts([]string{"alpha crit", "beta crit"}, []ACVerdict{{Index: 0, Status: "met"}}, nil, nil)
	body := RenderBody(s)
	if !strings.Contains(body, "Refs #42\n") || !strings.Contains(body, "- ✅ met: alpha crit\n") {
		t.Errorf("mixed body wrong:\n%s", body)
	}
	for _, c := range []string{"alpha crit", "beta crit"} {
		if n := strings.Count(body, c); n != 1 {
			t.Errorf("%q rendered %d times", c, n)
		}
	}
	if got := openBullets(body); len(got) != 1 || got[0] != "beta crit" {
		t.Errorf("open bullets = %q", got)
	}
}

func TestRenderBody_ZeroACsRefs(t *testing.T) {
	body := RenderBody(richSnap())
	if !strings.Contains(body, "Refs #42\n") || !strings.Contains(body, "no acceptance criteria were found") {
		t.Errorf("zero-AC body must say Refs:\n%s", body)
	}
}

func TestRenderBody_NoACsKeepsBytes(t *testing.T) {
	body := RenderBody(richSnap())
	if strings.Contains(body, "## Acceptance criteria") {
		t.Errorf("no ACs must render no AC section:\n%s", body)
	}
}

// AC 3 (#2595): "not run" and a pass count never appear together.
func TestUnitTestsLabel_NeverPairsNotRunWithCounts(t *testing.T) {
	cases := []struct {
		validatePassed bool
		passed, failed int
		want           string
	}{
		{false, 0, 0, "not run"},
		{true, 0, 0, "passed"},
		{true, 829, 0, "passed (829 passed, 0 failed)"},
		{false, 829, 0, "feature-dev reported 829 passed, 0 failed; not confirmed by feature-validate"},
		{true, 10, 2, "failed (10 passed, 2 failed)"},
		{false, 0, 3, "failed (0 passed, 3 failed)"},
	}
	for _, c := range cases {
		s := PRCreateSnapshot{UnitTestsPassed: c.validatePassed, TestsPassed: c.passed, TestsFailed: c.failed}
		got := unitTestsLabel(s)
		if got != c.want {
			t.Errorf("unitTestsLabel(%v,%d,%d) = %q, want %q", c.validatePassed, c.passed, c.failed, got, c.want)
		}
		if strings.Contains(got, "not run") && regexp.MustCompile(`\d`).MatchString(got) {
			t.Errorf("label pairs not run with a count: %q", got)
		}
	}
}

// The reader resolves verdicts from ac-reconcile + validate context and
// re-runs the deterministic rules on the branch tree.
func TestReadCreateContext_ResolvesACVerdicts(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "present.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		t.Helper()
		p := pipelineContextPath(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(v)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ac-reconcile-42.json", map[string]any{"acceptance_criteria": []map[string]any{
		{"index": 0, "text": "File present.md exists"},
		{"index": 1, "text": "File missing.md exists"},
		{"index": 2, "text": "The widget behaves"},
	}, "ac_fingerprint": "sha256:abc"})
	write("validate-42.json", map[string]any{
		"validation_status": "passed",
		"acceptance_criteria_verdicts": []map[string]any{
			{"index": 0, "status": "unmet"},
			{"index": 1, "status": "met"},
			{"index": 2, "status": "met", "evidence": "widget_test.go"},
		},
	})

	snap, err := defaultReadCreateContext(dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range snap.ACVerdicts {
		got = append(got, v.Status+"/"+v.Source)
	}
	want := []string{"unmet/feature-validate", "unmet/deterministic", "met/feature-validate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
	if snap.ACFingerprint != "sha256:abc" {
		t.Errorf("ACFingerprint = %q, want the ac-reconcile value (#2623)", snap.ACFingerprint)
	}
}

// pr-{N}.json carries the fingerprint when one was recorded and omits the
// key otherwise, so the resume gate reads "absent" rather than "".
func TestWritePRContext_ACFingerprint(t *testing.T) {
	for _, fp := range []string{"sha256:abc", ""} {
		dir := initRepo(t)
		if err := defaultWritePRContext(dir, prContextPayload{IssueNumber: 5, PRNumber: 6, ACFingerprint: fp}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pipelineContextPath(dir, "pr-5.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		_ = json.Unmarshal(data, &doc)
		got, ok := doc["ac_fingerprint"]
		if fp == "" && ok {
			t.Errorf("empty fingerprint must be omitted, got %v", got)
		}
		if fp != "" && got != fp {
			t.Errorf("ac_fingerprint = %v, want %s", got, fp)
		}
	}
}

func TestResolveACVerdicts_DiffAndTextMatching(t *testing.T) {
	criteria := []string{"first crit", "second crit", "third crit"}
	diff := func(results map[string]ACCheck) ACDiffChecker {
		return func(text string) (ACCheck, bool) {
			r, ok := results[text]
			return r, ok
		}
	}
	// A 1-based validate list that carries text lands on the right criterion.
	got := ResolveACVerdicts(criteria, []ACVerdict{
		{Index: 1, Text: "first  crit", Status: "unmet"},
		{Index: 2, Text: "second crit", Status: "met"},
		{Index: 3, Text: "third crit", Status: "met"},
	}, nil, nil)
	if got[0].Status != ACUnmet || got[1].Status != ACMet || got[2].Status != ACMet {
		t.Errorf("text matching: %+v", got)
	}

	got = ResolveACVerdicts(criteria,
		[]ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "not_verifiable"}},
		nil,
		diff(map[string]ACCheck{
			"first crit":  {Status: ACUnmet, Evidence: "`a.md` adds no line under ## X"},
			"second crit": {Status: ACMet, Evidence: "`a.md:3` adds 1 line(s)"},
			"third crit":  {Status: ACNotVerifiable, Step: "a physical device"},
		}))
	want := []string{"unmet/diff", "met/diff", "not_verifiable/diff"}
	for i, v := range got {
		if v.Status+"/"+v.Source != want[i] {
			t.Errorf("criterion %d = %s/%s, want %s", i, v.Status, v.Source, want[i])
		}
	}
	if got[2].Step != "a physical device" {
		t.Errorf("runtime step lost: %+v", got[2])
	}

	s := richSnap()
	s.ACVerdicts = got
	body := RenderBody(s)
	if !strings.Contains(body, "_❔ not verifiable — needs: a physical device_") {
		t.Errorf("not-verifiable row must name its step:\n%s", body)
	}
}

// End to end (#2665): the #2661 artifacts as they were written — a stored
// criteria list doubled by a pre-#2655 parser — still give five met rows,
// each once, and Closes #2661. The drifted validate verdicts that run wrote
// are now rejected (#2671); see TestReadCreateContext_RejectsMisplacedACVerdicts.
func TestReadCreateContext_Issue2661ClosesFromDiff(t *testing.T) {
	dir := changelogRepo(t)
	addBullet2656(t, dir)
	gitOut(t, dir, "commit", "-qam", "docs(#2661): add the #2656 fix")
	write := func(name string, v any) {
		t.Helper()
		p := pipelineContextPath(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(v)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stored []map[string]any
	for i, ac := range append(append([]string{}, issue2661ACs...), issue2661ACs...) {
		stored = append(stored, map[string]any{"index": i, "text": ac})
	}
	write("ac-reconcile-2661.json", map[string]any{"acceptance_criteria": stored})
	write("issue-2661.json", map[string]any{"issue_number": 2661, "branch": "docs/2661-changelog", "base_branch": "main"})
	write("validate-2661.json", map[string]any{
		"validation_status": "passed",
	})

	snap, err := defaultReadCreateContext(dir, 2661)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 2661
	if len(snap.ACVerdicts) != 5 {
		t.Fatalf("got %d verdicts, want 5: %+v", len(snap.ACVerdicts), snap.ACVerdicts)
	}
	for i, v := range snap.ACVerdicts {
		if v.Status != ACMet {
			t.Errorf("AC%d = %+v, want met", i+1, v)
		}
	}
	body := RenderBody(snap)
	if !strings.Contains(body, "Closes #2661\n") || strings.Contains(body, UnmetACHeading) {
		t.Errorf("all five met must close the issue:\n%s", body)
	}
	if n := strings.Count(body, "- ✅ met: "); n != 5 {
		t.Errorf("%d met rows, want 5:\n%s", n, body)
	}
	for _, ac := range issue2661ACs {
		if n := strings.Count(body, ac); n != 1 {
			t.Errorf("criterion rendered %d times: %q", n, ac)
		}
	}
}

// #2671: verdicts under the wrong key fail pr-create upstream instead of
// rendering every criterion not_verifiable; the correct shape still reads.
func TestReadCreateContext_RejectsMisplacedACVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		body    map[string]any
		wantErr string
	}{
		{"wrong key", map[string]any{"acceptance_criteria": []map[string]any{
			{"index": 1, "verdict": "met"}, {"index": 2, "verdict": "met"}, {"index": 3, "verdict": "met"},
			{"index": 4, "verdict": "met"}, {"index": 5, "verdict": "met"},
		}}, "acceptance_criteria_verdicts"},
		{"verdict field", map[string]any{"acceptance_criteria_verdicts": []map[string]any{
			{"index": 0, "text": "x", "verdict": "met"},
		}}, "the field is status"},
		{"correct shape", map[string]any{"acceptance_criteria_verdicts": []map[string]any{
			{"index": 0, "text": "x", "status": "met", "evidence": "diff"},
		}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := changelogRepo(t)
			p := pipelineContextPath(dir, "validate-7.json")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			c.body["validation_status"] = "passed"
			data, _ := json.Marshal(c.body)
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			snap, err := defaultReadCreateContext(dir, 7)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("correct shape must read: %v", err)
				}
				if !snap.HasValidate {
					t.Fatal("validate context not read")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

// TestResolveACVerdicts_Issue2672Verdicts is the #2679 regression: #2678's
// six met verdicts close #2672 whether its criteria were stored whole or with
// AC2 truncated at the inline `<details>`. Verdict indices are 1-based so
// only text matching can place them.
func TestResolveACVerdicts_Issue2672Verdicts(t *testing.T) {
	whole := []string{
		"An issue body whose acceptance criteria are numbered items (`1. ...`, `1) ...`, `1. **AC1:** ...`) yields one criterion per item in `parseACs`.",
		"Checkbox bullets (`- [ ]`, `* [x]`) keep parsing exactly as before, including the `<details>` exclusion and de-duplication.",
		"A body with no checkboxes and no numbered items still yields zero criteria and `no-acs-detected`.",
		"Re-running `nightgauge preflight ac-reconcile` on the #2665 body reports detected criteria instead of `no-acs-detected`.",
		"Go unit tests cover numbered, parenthesised, bold-prefixed, and mixed-list bodies.",
		"The SDK mirror of the parser (`packages/nightgauge-sdk/src/preflight/`) accepts the same forms, or its divergence from the Go port is documented.",
	}
	var validate []ACVerdict
	for i, text := range whole {
		validate = append(validate, ACVerdict{Index: i + 1, Text: text, Status: "met", Evidence: "verified"})
	}
	// A re-formatted verdict text: no backticks, no trailing period.
	validate[2].Text = "A body with no checkboxes and no numbered items still yields zero criteria and no-acs-detected"
	truncated := append([]string(nil), whole...)
	truncated[1] = "Checkbox bullets (`- [ ]`, `* [x]`) keep parsing exactly as before, including the `"
	for name, criteria := range map[string][]string{"whole": whole, "truncated": truncated} {
		t.Run(name, func(t *testing.T) {
			s := richSnap()
			s.ACVerdicts = ResolveACVerdicts(criteria, validate, nil, nil)
			if open := OpenACs(s.ACVerdicts); open != nil {
				t.Fatalf("open criteria = %+v", open)
			}
			if !IssueClosedBy(s.ACVerdicts) {
				t.Fatal("IssueClosedBy = false")
			}
			if body := RenderBody(s); !strings.Contains(body, "Closes #42\n") || strings.Contains(body, UnmetACHeading) {
				t.Errorf("body must close with no unmet section:\n%s", body)
			}
		})
	}
}

// TestResolveACVerdicts_TextBeatsIndex guards the #2679 fallbacks: verdicts
// listed in swapped order still land on their own criteria.
func TestResolveACVerdicts_TextBeatsIndex(t *testing.T) {
	got := ResolveACVerdicts([]string{"alpha works", "beta works"}, []ACVerdict{
		{Index: 0, Text: "Beta works.", Status: "unmet"},
		{Index: 1, Text: "`alpha` works", Status: "met"},
	}, nil, nil)
	if got[0].Status != ACMet || got[1].Status != ACUnmet {
		t.Errorf("verdicts = %+v", got)
	}
}

func TestAcMatchKey(t *testing.T) {
	if a, b := acMatchKey("Uses **`Foo`**  _now_."), acMatchKey("uses foo now"); a != b {
		t.Errorf("%q != %q", a, b)
	}
}

// TestACTestState_UnconfirmedRunNeverPasses: feature-dev's own counts stand
// in for a test run only when feature-validate never ran and no built suite
// went unexecuted, so an unconfirmed run cannot close an issue (#2665).
func TestACTestState_UnconfirmedRunNeverPasses(t *testing.T) {
	idle := deliverable.Finding{Artifacts: []deliverable.TestArtifact{{}}}
	cases := []struct {
		name string
		snap PRCreateSnapshot
		want bool
	}{
		{"validate passed", PRCreateSnapshot{HasValidate: true, UnitTestsPassed: true, TestsPassed: 3}, true},
		{"dev counts, no validate", PRCreateSnapshot{TestsPassed: 3}, true},
		{"dev counts, validate did not confirm", PRCreateSnapshot{HasValidate: true, TestsPassed: 3}, false},
		{"dev counts, suite never executed", PRCreateSnapshot{TestsPassed: 3, UnverifiedDeliverable: idle}, false},
		{"dev failures", PRCreateSnapshot{TestsPassed: 3, TestsFailed: 1}, false},
		{"nothing recorded", PRCreateSnapshot{}, false},
	}
	for _, tc := range cases {
		if got := acTestState(tc.snap).Passed; got != tc.want {
			t.Errorf("%s: Passed = %v, want %v", tc.name, got, tc.want)
		}
	}
}
