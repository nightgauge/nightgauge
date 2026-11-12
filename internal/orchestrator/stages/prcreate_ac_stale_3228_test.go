package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// stale3228Criteria are 13 criteria no deterministic rule can check, so each
// verdict comes from feature-validate alone.
func stale3228Criteria() []string {
	out := make([]string, 13)
	for i := range out {
		out[i] = fmt.Sprintf("Behaviour %c of the widget holds", 'A'+i)
	}
	return out
}

// stale3228Fixture writes a run's context for issue 42: an ac-reconcile
// report over the first nReport criteria, fingerprinted from them, and a
// validate verdict of met for each of those. The issue body reader is
// installed by the caller.
func stale3228Fixture(t *testing.T, nReport int) string {
	t.Helper()
	dir := initRepo(t)
	all := stale3228Criteria()
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
	for i, c := range all[:nReport] {
		acs = append(acs, map[string]any{"index": i, "text": c})
		verdicts = append(verdicts, map[string]any{"index": i, "text": c, "status": "met", "evidence": "widget_test.go"})
	}
	write("ac-reconcile-42.json", map[string]any{
		"acceptance_criteria": acs,
		"ac_fingerprint":      preflight.ACFingerprint(acCheckboxBody(all[:nReport])),
	})
	write("validate-42.json", map[string]any{"validation_status": "passed", "acceptance_criteria_verdicts": verdicts})
	return dir
}

func stubIssueBody(t *testing.T, body string, err error) {
	t.Helper()
	prev := prCreateIssueBody
	prCreateIssueBody = func(string) IssueBodyReader {
		return func(context.Context, int) (string, error) { return body, err }
	}
	t.Cleanup(func() { prCreateIssueBody = prev })
}

// A re-run whose report judged 9 criteria while the issue now has 13 (#3192's
// shape): the PR lists all 13, the 4 the run never saw are `❔ not verified`
// under the unmet heading, the PR says Refs, and the remainder the post-merge
// hook reads from the body names exactly those 4.
func TestReadCreateContext_StaleReport_ListsBodyCriteriaAndSaysRefs(t *testing.T) {
	dir := stale3228Fixture(t, 9)
	all := stale3228Criteria()
	stubIssueBody(t, "## Acceptance Criteria\n\n"+acCheckboxBody(all), nil)

	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if len(snap.ACVerdicts) != 13 {
		t.Fatalf("verdicts = %d, want the body's 13", len(snap.ACVerdicts))
	}
	for i, v := range snap.ACVerdicts {
		want := ACMet
		if i >= 9 {
			want = ACNotJudged
		}
		if v.Status != want || v.Text != all[i] {
			t.Errorf("verdict %d = %s %q, want %s %q", i, v.Status, v.Text, want, all[i])
		}
	}
	if want := preflight.ACFingerprint("## Acceptance Criteria\n\n" + acCheckboxBody(all)); snap.ACFingerprint != want {
		t.Errorf("ACFingerprint = %q, want the body's %q", snap.ACFingerprint, want)
	}

	body := RenderBody(snap)
	if !strings.Contains(body, "Refs #42") || BodyClosesIssue(body, "", 42) {
		t.Errorf("body must say Refs #42 and never close it:\n%s", body)
	}
	for _, c := range all {
		if !strings.Contains(body, c) {
			t.Errorf("body does not list %q", c)
		}
	}
	if got := strings.Count(body, "❔ not verified"); got != 4 {
		t.Errorf("❔ not verified appears %d times, want 4:\n%s", got, body)
	}
	if got, want := hooks.UnmetCriteria(body), all[9:]; !reflect.DeepEqual(got, want) {
		t.Errorf("partial-delivery remainder = %v, want %v", got, want)
	}
}

// A report whose fingerprint matches the body behaves as before #3228: its
// criteria, its fingerprint, and Closes when every criterion is met.
func TestReadCreateContext_MatchingReport_Unchanged(t *testing.T) {
	dir := stale3228Fixture(t, 13)
	all := stale3228Criteria()
	stubIssueBody(t, "Intro.\n\n## Acceptance Criteria\n\n"+acCheckboxBody(all), nil)

	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if snap.ACScopeUnverified {
		t.Error("ACScopeUnverified = true on a matching fingerprint")
	}
	if want := preflight.ACFingerprint(acCheckboxBody(all)); snap.ACFingerprint != want {
		t.Errorf("ACFingerprint = %q, want the report's %q", snap.ACFingerprint, want)
	}
	for _, v := range snap.ACVerdicts {
		if v.Status != ACMet {
			t.Errorf("verdict %q = %s, want met", v.Text, v.Status)
		}
	}
	body := RenderBody(snap)
	if !strings.Contains(body, "Closes #42") || strings.Contains(body, UnmetACHeading) {
		t.Errorf("a fully met, current report must close the issue:\n%s", body)
	}
}

// A body pr-create cannot read leaves the scope unconfirmed: the report's
// list stands, and the PR says Refs with the reason under the unmet heading.
func TestReadCreateContext_UnreadableBody_SaysRefs(t *testing.T) {
	dir := stale3228Fixture(t, 13)
	stubIssueBody(t, "", errors.New("gh: HTTP 502"))

	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if len(snap.ACVerdicts) != 13 || !snap.ACScopeUnverified {
		t.Fatalf("verdicts = %d, ACScopeUnverified = %v; want the report's 13 and true", len(snap.ACVerdicts), snap.ACScopeUnverified)
	}
	body := RenderBody(snap)
	if !strings.Contains(body, "Refs #42") || BodyClosesIssue(body, "", 42) {
		t.Errorf("an unconfirmed scope must say Refs #42:\n%s", body)
	}
	if !strings.Contains(body, "could not confirm these criteria") {
		t.Errorf("body does not say why it leaves the issue open:\n%s", body)
	}
}

// A body that dropped a criterion still differs from the report: every
// remaining criterion is met, yet the PR says Refs, because pr-create alone
// cannot prove the narrower scope is the one the issue's owner meant.
func TestReadCreateContext_BodyDroppedCriterion_SaysRefs(t *testing.T) {
	dir := stale3228Fixture(t, 13)
	stubIssueBody(t, acCheckboxBody(stale3228Criteria()[:12]), nil)

	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if len(snap.ACVerdicts) != 12 || !IssueClosedBy(snap.ACVerdicts) {
		t.Fatalf("want the body's 12 criteria, all met; got %d", len(snap.ACVerdicts))
	}
	if body := RenderBody(snap); !strings.Contains(body, "Refs #42") {
		t.Errorf("a changed scope must say Refs #42:\n%s", body)
	}
}
