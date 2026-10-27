package stages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

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
			name:     "deterministic overrides validate",
			criteria: criteria,
			validate: []ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "unmet"}, {Index: 2, Status: "met"}},
			det:      det(preflight.ACUnsatisfied, preflight.ACSatisfied, preflight.ACUndetectable),
			want:     []string{"unmet/deterministic", "met/deterministic", "met/feature-validate"},
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
			got := ResolveACVerdicts(c.criteria, c.validate, c.det)
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

// githubClosingRE is GitHub's closing-keyword grammar for issue 42. A Refs
// body that matched it would still close the issue at merge.
var githubClosingRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[ \t]*:?[ \t]*(?:[\w.-]+/[\w.-]+)?#42\b`)

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
		}, nil)
	body := RenderBody(s)

	if !strings.Contains(body, "Refs #42\n") {
		t.Errorf("partial delivery must say Refs #42:\n%s", body)
	}
	if githubClosingRE.MatchString(body) {
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
	s.ACVerdicts = ResolveACVerdicts([]string{"a", "b"}, []ACVerdict{{Index: 0, Status: "met"}, {Index: 1, Status: "met"}}, nil)
	body := RenderBody(s)
	if !strings.Contains(body, "Closes #42\n") || strings.Contains(body, UnmetACHeading) {
		t.Errorf("all-met body must close with no unmet section:\n%s", body)
	}
}

func TestRenderBody_NotVerifiableKeepsClosesButLists(t *testing.T) {
	s := richSnap()
	s.ACVerdicts = ResolveACVerdicts([]string{"a", "b"}, []ACVerdict{{Index: 0, Status: "met"}}, nil)
	body := RenderBody(s)
	if !strings.Contains(body, "Closes #42\n") {
		t.Errorf("not_verifiable must keep Closes:\n%s", body)
	}
	if !strings.Contains(body, "- ❔ not verifiable: b\n") {
		t.Errorf("not_verifiable AC must be listed:\n%s", body)
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
	}})
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
	want := []string{"met/deterministic", "unmet/deterministic", "met/feature-validate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
}
