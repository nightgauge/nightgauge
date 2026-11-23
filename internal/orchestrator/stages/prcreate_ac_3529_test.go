package stages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/preflight"
)

const (
	closes3529 = "The description says `Closes #42`."
	refs3529   = "The description says `Refs #42`."
)

func verdicts3529(texts []string, statuses ...string) []ACVerdict {
	out := make([]ACVerdict, len(texts))
	for i, t := range texts {
		out[i] = ACVerdict{Index: i, Text: t, Status: statuses[i], Source: ACSourceValidate}
	}
	return out
}

// #3529 B2: a "the description says `Closes #N`" criterion is judged against
// the keyword pr-create writes; it can never hold its own issue open.
func TestResolveOwnKeywordACs(t *testing.T) {
	cases := []struct {
		name      string
		texts     []string
		statuses  []string
		forceRefs bool
		want      []string // statuses after resolution
	}{
		{"only open is Closes", []string{"Behaviour A holds", closes3529},
			[]string{ACMet, ACUnmet}, false, []string{ACMet, ACMet}},
		{"only criterion is Closes", []string{closes3529},
			[]string{ACNotVerifiable}, false, []string{ACMet}},
		{"Closes with another unmet", []string{"Behaviour A holds", closes3529},
			[]string{ACUnmet, ACUnmet}, false, []string{ACUnmet, ACUnmet}},
		{"Closes under a Refs delivery order (#3510)", []string{"Behaviour A holds", closes3529},
			[]string{ACMet, ACUnmet}, true, []string{ACMet, ACUnmet}},
		{"Refs with another unmet", []string{"Behaviour A holds", refs3529},
			[]string{ACUnmet, ACUnmet}, false, []string{ACUnmet, ACMet}},
		{"Refs under a Refs delivery order", []string{"Behaviour A holds", refs3529},
			[]string{ACMet, ACNotVerifiable}, true, []string{ACMet, ACMet}},
		{"Refs with all else met stays open", []string{"Behaviour A holds", refs3529},
			[]string{ACMet, ACNotVerifiable}, false, []string{ACMet, ACNotVerifiable}},
		{"another issue's keyword untouched", []string{"Behaviour A holds", "The description says `Closes #7`."},
			[]string{ACMet, ACUnmet}, false, []string{ACMet, ACUnmet}},
		{"both keywords untouched", []string{closes3529, refs3529},
			[]string{ACUnmet, ACUnmet}, false, []string{ACUnmet, ACUnmet}},
		{"met Closes stays met under forceRefs", []string{"Behaviour A holds", closes3529},
			[]string{ACMet, ACMet}, true, []string{ACMet, ACMet}},
		{"Fixes is a closing keyword", []string{"Behaviour A holds", "The PR description says `Fixes #42`."},
			[]string{ACMet, ACUnmet}, false, []string{ACMet, ACMet}},
		{"mixed clause untouched", []string{"The description says `Closes #42`, and `a.go` adds `X`."},
			[]string{ACUnmet}, false, []string{ACUnmet}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := verdicts3529(c.texts, c.statuses...)
			got := ResolveOwnKeywordACs(in, 42, c.forceRefs)
			for i, v := range got {
				if v.Status != c.want[i] {
					t.Errorf("verdict %d (%q) = %s, want %s (%+v)", i, v.Text, v.Status, c.want[i], v)
				}
				if in[i].Status != c.statuses[i] {
					t.Errorf("input verdict %d was modified", i)
				}
				if v.Index != i || v.Text != c.texts[i] {
					t.Errorf("verdict %d lost its index or text: %+v", i, v)
				}
				resolved := v.Status != c.statuses[i]
				if resolved && (v.Source != ACSourcePRBody || !strings.HasPrefix(v.Evidence, "the PR body says `")) {
					t.Errorf("resolved verdict %d: source %q evidence %q", i, v.Source, v.Evidence)
				}
				if !resolved && v.Source != ACSourceValidate {
					t.Errorf("unresolved verdict %d changed source to %q", i, v.Source)
				}
			}
		})
	}
}

// fixture3529 writes ac-reconcile and validate context for issue 42 with a
// met behaviour criterion and a not-verifiable `Closes #42` criterion.
func fixture3529(t *testing.T, criteria []string) string {
	t.Helper()
	dir := initRepo(t)
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
	var acs, verdicts []map[string]any
	for i, c := range criteria {
		status := "met"
		if strings.Contains(c, "#42") {
			status = "not_verifiable"
		}
		acs = append(acs, map[string]any{"index": i, "text": c})
		verdicts = append(verdicts, map[string]any{"index": i, "text": c, "status": status, "evidence": "widget_test.go"})
	}
	write("ac-reconcile-42.json", map[string]any{
		"acceptance_criteria": acs,
		"ac_fingerprint":      preflight.ACFingerprint(acCheckboxBody(criteria)),
	})
	write("validate-42.json", map[string]any{"validation_status": "passed", "acceptance_criteria_verdicts": verdicts})
	return dir
}

// #3529 B2 through pr-create's own context read: the PR closes its issue when
// the only open criterion asks for `Closes #42`, and a Refs delivery order
// (#3510) still says Refs.
func TestReadCreateContext_OwnClosesCriterion(t *testing.T) {
	criteria := []string{"Behaviour A of the widget holds", closes3529}
	acs := "## Acceptance Criteria\n\n" + acCheckboxBody(criteria)
	for _, tc := range []struct {
		name, body, keyword string
	}{
		{"closes", acs, "Closes #42"},
		{"refs delivery order", acs + "\n## Delivery order\n\nEach PR refs #42.\n", "Refs #42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture3529(t, criteria)
			stubIssueBody(t, tc.body, nil)
			snap, err := defaultReadCreateContext(context.Background(), dir, 42)
			if err != nil {
				t.Fatal(err)
			}
			snap.IssueNumber = 42
			body := RenderBody(snap)
			if closes := BodyClosesIssue(body, "", 42); closes != (tc.keyword == "Closes #42") {
				t.Errorf("BodyClosesIssue = %v, want the body to say %s:\n%s", closes, tc.keyword, body)
			}
		})
	}
}
