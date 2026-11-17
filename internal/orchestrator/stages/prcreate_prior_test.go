package stages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/trust"
)

func priorComment(pr string, met ...string) string {
	b := "<!-- nightgauge:partial-delivery pr=" + pr + " -->\n\n" + hooks.RemainingCriteriaHeading + "\n\n- later\n\n" +
		hooks.MetCriteriaHeading + "\n\n"
	for _, m := range met {
		b += "- " + m + "\n"
	}
	return b
}

func TestApplyPriorDeliveries_MergedPriorLiftsNotVerifiable(t *testing.T) {
	in := []ACVerdict{
		{Index: 0, Text: "Add the widget", Status: ACNotVerifiable, Source: ACSourceNone, Step: "run it"},
		{Index: 1, Text: "Document it", Status: ACMet, Source: ACSourceValidate},
	}
	got := ApplyPriorDeliveries(in, []PriorDelivery{{PR: 9, Met: []string{"Add the widget"}}, {PR: 7, Met: []string{"add the widget"}}}, nil, "main", nil)
	if v := got[0]; v.Status != ACMet || v.Source != ACSourcePriorPR || v.MetByPR != 9 || v.Step != "" {
		t.Errorf("verdict 0 = %+v, want met by newest prior PR #9", v)
	}
	if in[0].Status != ACNotVerifiable {
		t.Error("input slice was mutated")
	}
	if !reflect.DeepEqual(got[1], in[1]) {
		t.Errorf("verdict 1 changed: %+v", got[1])
	}
}

func TestApplyPriorDeliveries_UnmetAndNotJudgedNeverLifted(t *testing.T) {
	in := []ACVerdict{
		{Text: "A crit", Status: ACUnmet, Evidence: "abcdef1"},
		{Text: "B crit", Status: ACNotJudged, Evidence: "abcdef1"},
	}
	got := ApplyPriorDeliveries(in, []PriorDelivery{{PR: 3, Met: []string{"A crit", "B crit"}}},
		func(string) bool { return true }, "main", nil)
	for i := range in {
		if got[i].Status != in[i].Status {
			t.Errorf("verdict %d lifted from %s to %s", i, in[i].Status, got[i].Status)
		}
	}
}

// AC2/AC4: a SHA on base lifts; a SHA only on the feature branch does not.
func TestApplyPriorDeliveries_CommitAncestry(t *testing.T) {
	dir := initRepo(t) // base commit on main, now on fix/1179-x
	baseSHA := gitOut(t, dir, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "feature")
	featSHA := gitOut(t, dir, "rev-parse", "HEAD")

	onBase := gitCommitOnBase(dir, "main")
	in := []ACVerdict{
		{Text: "on base", Status: ACNotVerifiable, Evidence: "landed in " + strings.TrimSpace(baseSHA)[:12]},
		{Text: "feature only", Status: ACNotVerifiable, Evidence: "landed in " + strings.TrimSpace(featSHA)[:12]},
		{Text: "not a commit", Status: ACNotVerifiable, Evidence: "see deadbeefcafe"},
	}
	got := ApplyPriorDeliveries(in, nil, onBase, "main", nil)
	if got[0].Status != ACMet || got[0].Source != ACSourcePriorCommit || got[0].MetByCommit == "" {
		t.Errorf("base commit not credited: %+v", got[0])
	}
	if got[1].Status != ACNotVerifiable || got[2].Status != ACNotVerifiable {
		t.Errorf("feature-only or bogus SHA credited: %+v %+v", got[1], got[2])
	}
}

// AC4: a PR closed without merge contributes nothing; a state read error fails.
func TestMergedPriorDeliveries_OnlyMergedPRs(t *testing.T) {
	comments := []trust.Comment{
		{Body: priorComment("5", "Alpha crit")},
		{Body: priorComment("6", "Beta crit")},
		{Body: "no marker here"},
	}
	states := map[int]string{5: "MERGED", 6: "CLOSED"}
	got, err := mergedPriorDeliveries(comments, func(pr int) (string, error) { return states[pr], nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PR != 5 || len(got[0].Met) != 1 || got[0].Met[0] != "Alpha crit" {
		t.Errorf("got %+v, want only PR #5", got)
	}
	if _, err := mergedPriorDeliveries(comments, func(int) (string, error) { return "", errors.New("boom") }); err == nil {
		t.Error("a PR state read error was swallowed")
	}
}

func priorFixture(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	crits := []string{"Alpha behaviour holds", "Beta behaviour holds"}
	write := func(name string, v any) {
		p := pipelineContextPath(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(v)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ac-reconcile-42.json", map[string]any{
		"acceptance_criteria": []map[string]any{{"index": 0, "text": crits[0]}, {"index": 1, "text": crits[1]}},
		"ac_fingerprint":      preflight.ACFingerprint(acCheckboxBody(crits)),
	})
	write("validate-42.json", map[string]any{"validation_status": "passed", "acceptance_criteria_verdicts": []map[string]any{
		{"index": 0, "text": crits[0], "status": "not_verifiable", "evidence": "not in this diff"},
		{"index": 1, "text": crits[1], "status": "met", "evidence": "beta_test.go"},
	}})
	return dir
}

func stubPriors(t *testing.T, priors []PriorDelivery, err error) {
	t.Helper()
	prev := prCreatePriorDeliveries
	prCreatePriorDeliveries = func(string) PriorDeliveryReader {
		return func(context.Context, int) ([]PriorDelivery, error) { return priors, err }
	}
	t.Cleanup(func() { prCreatePriorDeliveries = prev })
}

// AC1/AC3: PR #31 merged with criterion 1 met; this run meets criterion 2.
// The PR closes the issue and names which PR met each criterion.
func TestReadCreateContext_TwoPRIssueCloses(t *testing.T) {
	dir := priorFixture(t)
	stubPriors(t, []PriorDelivery{{PR: 31, Met: []string{"Alpha behaviour holds"}}}, nil)
	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if !snapClosesIssue(snap) {
		t.Fatalf("issue not closed: %+v", snap.ACVerdicts)
	}
	body := RenderBody(snap)
	if !strings.Contains(body, "Closes #42") {
		t.Errorf("body does not close #42:\n%s", body)
	}
	if !strings.Contains(body, "- ✅ met: Alpha behaviour holds (met by #31, merged earlier)\n") {
		t.Errorf("criterion 1 does not name #31:\n%s", body)
	}
	if !strings.Contains(body, "- ✅ met: Beta behaviour holds (this PR — feature-validate: beta_test.go)\n") {
		t.Errorf("criterion 2 does not name this PR:\n%s", body)
	}
}

// A record that cannot be read credits nothing: the PR says Refs.
func TestReadCreateContext_PriorReadErrorSaysRefs(t *testing.T) {
	dir := priorFixture(t)
	stubPriors(t, []PriorDelivery{{PR: 31, Met: []string{"Alpha behaviour holds"}}}, errors.New("forge down"))
	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if snapClosesIssue(snap) {
		t.Error("closed the issue without the earlier PR's record")
	}
	if body := RenderBody(snap); !strings.Contains(body, "Refs #42") || BodyClosesIssue(body, "", 42) {
		t.Errorf("body must say Refs #42:\n%s", body)
	}
}

// A reworded criterion stays not_verifiable; a prior-PR match wins over a
// base commit when both apply.
func TestApplyPriorDeliveries_RewordAndPrecedence(t *testing.T) {
	in := []ACVerdict{
		{Text: "Alpha behaviour holds everywhere", Status: ACNotVerifiable},
		{Text: "Beta crit", Status: ACNotVerifiable, Evidence: "abcdef1234"},
	}
	got := ApplyPriorDeliveries(in, []PriorDelivery{{PR: 4, Met: []string{"Alpha behaviour is kept", "Beta crit"}}},
		func(string) bool { return true }, "main", nil)
	if got[0].MetByPR != 0 {
		t.Errorf("reworded criterion credited to a prior PR: %+v", got[0])
	}
	if got[1].Source != ACSourcePriorPR || got[1].MetByPR != 4 || got[1].MetByCommit != "" {
		t.Errorf("prior PR did not win over the commit: %+v", got[1])
	}
}

func TestMergedPriorDeliveries_OpenEmptyAndDuplicate(t *testing.T) {
	comments := []trust.Comment{
		{Body: priorComment("5", "Alpha crit")},
		{Body: priorComment("5", "Gamma crit")},
		{Body: priorComment("6", "Beta crit")},
		{Body: "<!-- nightgauge:partial-delivery pr=8 -->\n"},
	}
	got, err := mergedPriorDeliveries(comments, func(pr int) (string, error) {
		switch pr {
		case 5:
			return "MERGED", nil
		case 6:
			return "OPEN", nil
		}
		t.Errorf("state read for PR #%d, which lists no criterion", pr)
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PR != 5 || !reflect.DeepEqual(got[0].Met, []string{"Alpha crit", "Gamma crit"}) ||
		!reflect.DeepEqual(got[0].Remaining, []string{"later", "later"}) {
		t.Errorf("got %+v", got)
	}
}

func TestRenderBody_CommitCreditedLine(t *testing.T) {
	s := richSnap()
	s.BaseBranch = "main"
	s.ACVerdicts = []ACVerdict{{Text: "Gamma holds", Status: ACMet, Source: ACSourcePriorCommit, MetByCommit: "abc1234", Evidence: "delivered by abc1234, an ancestor of main"}}
	if body := RenderBody(s); !strings.Contains(body, "- ✅ met: Gamma holds (delivered by abc1234 on main)\n") {
		t.Errorf("commit-credited line wrong:\n%s", body)
	}
}

// AC2 through the snapshot loader: validate evidence citing main's commit.
func TestReadCreateContext_BaseCommitEvidenceCloses(t *testing.T) {
	dir := priorFixture(t)
	sha := gitOut(t, dir, "rev-parse", "main")
	p := pipelineContextPath(dir, "validate-42.json")
	data, _ := json.Marshal(map[string]any{"validation_status": "passed", "acceptance_criteria_verdicts": []map[string]any{
		{"index": 0, "text": "Alpha behaviour holds", "status": "not_verifiable", "evidence": "delivered in " + sha[:10] + ", already on main"},
		{"index": 1, "text": "Beta behaviour holds", "status": "met", "evidence": "beta_test.go"},
	}})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeIssueBase(t, dir, "main")
	stubPriors(t, nil, nil)
	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	if !snapClosesIssue(snap) || snap.ACVerdicts[0].MetByCommit != sha[:10] {
		t.Errorf("base commit not credited: %+v", snap.ACVerdicts)
	}
}

func writeIssueBase(t *testing.T, dir, base string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"issue_number": 42, "title": "x", "branch": "fix/1179-x", "base_branch": base})
	if err := os.WriteFile(pipelineContextPath(dir, "issue-42.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Review #3362: a base SHA cited without a delivery claim, or a criterion
// that needs a runtime step, is not credited to a commit.
func TestApplyPriorDeliveries_CommitNeedsDeliveryClaim(t *testing.T) {
	in := []ACVerdict{
		{Text: "cites base", Status: ACNotVerifiable, Evidence: "cannot verify; compared against merge-base 1a2b3c4d"},
		{Text: "needs a step", Status: ACNotVerifiable, Evidence: "delivered by 1a2b3c4d", Step: "run the daemon"},
		{Text: "claims it", Status: ACNotVerifiable, Evidence: "Implemented in commit 1A2B3C4D"},
	}
	got := ApplyPriorDeliveries(in, nil, func(string) bool { return true }, "main", nil)
	if got[0].Status != ACNotVerifiable || got[1].Status != ACNotVerifiable {
		t.Errorf("credited without a delivery claim or despite a step: %+v / %+v", got[0], got[1])
	}
	if got[2].Status != ACMet || got[2].MetByCommit != "1a2b3c4d" {
		t.Errorf("delivery claim not credited: %+v", got[2])
	}
}

// Review #3362: ac-reconcile joins a checkbox's sub-bullets onto its text; the
// record holds the issue's checkbox line alone, and the criterion still
// matches through the issue's wording.
func TestApplyPriorDeliveries_SubBulletCriterionMatches(t *testing.T) {
	body := "## Acceptance criteria\n\n- [ ] The `widget` renders on every page.\n  - including the footer\n"
	issue := hooks.IssueCriterionTexts(body)
	acText := preflight.ACTexts(body)
	if len(acText) != 1 {
		t.Fatalf("ac-reconcile criteria = %q, want one", acText)
	}
	in := []ACVerdict{{Text: acText[0], Status: ACNotVerifiable}}
	got := ApplyPriorDeliveries(in, []PriorDelivery{{PR: 5, Met: []string{"The `widget` renders on every page."}}}, nil, "main", issue)
	if got[0].Status != ACMet || got[0].MetByPR != 5 {
		t.Errorf("sub-bullet criterion %q (issue %q) not matched to the prior record: %+v", acText[0], issue, got[0])
	}
}

// Review #3362: a criterion strengthened after the record was written is not
// credited by the shorter record entry.
func TestApplyPriorDeliveries_ExtendedCriterionNotCredited(t *testing.T) {
	text := "Add the widget to the dashboard and settings page"
	in := []ACVerdict{{Text: text, Status: ACNotVerifiable}}
	got := ApplyPriorDeliveries(in, []PriorDelivery{{PR: 5, Met: []string{"Add the widget to the dashboard"}}}, nil, "main", []string{text})
	if got[0].Status != ACNotVerifiable {
		t.Errorf("strengthened criterion credited by a shorter record: %+v", got[0])
	}
}

// Review #3362: the newest merged record that names a criterion decides; a
// later record that left it open overrides an older met.
func TestApplyPriorDeliveries_NewestRecordWins(t *testing.T) {
	in := []ACVerdict{{Text: "Widget crit", Status: ACNotVerifiable}, {Text: "Other crit", Status: ACNotVerifiable}}
	priors := []PriorDelivery{
		{PR: 1, Met: []string{"Widget crit", "Other crit"}},
		{PR: 2, Met: nil, Remaining: []string{"Widget crit"}},
	}
	got := ApplyPriorDeliveries(in, priors, nil, "main", nil)
	if got[0].Status != ACNotVerifiable {
		t.Errorf("criterion a newer record left open was credited: %+v", got[0])
	}
	if got[1].MetByPR != 1 {
		t.Errorf("criterion only the older record names was not credited to it: %+v", got[1])
	}
}

// Review #3362: a negated or hedged delivery claim credits no commit.
func TestApplyPriorDeliveries_HedgedCommitClaim(t *testing.T) {
	for _, ev := range []string{
		"could not confirm it was implemented in abc1234",
		"not added in commit abc1234",
		"check whether it landed in abc1234",
		"it wasn't delivered by abc1234",
		"will be delivered by abc1234",
	} {
		got := ApplyPriorDeliveries([]ACVerdict{{Text: "x", Status: ACNotVerifiable, Evidence: ev}}, nil,
			func(string) bool { return true }, "main", nil)
		if got[0].Status != ACNotVerifiable {
			t.Errorf("hedged evidence %q credited a commit: %+v", ev, got[0])
		}
	}
	got := ApplyPriorDeliveries([]ACVerdict{{Text: "x", Status: ACNotVerifiable, Evidence: "Cannot verify from this diff. Delivered by abc1234."}}, nil,
		func(string) bool { return true }, "main", nil)
	if got[0].MetByCommit != "abc1234" {
		t.Errorf("a plain claim after a separate hedged sentence was not credited: %+v", got[0])
	}
}

// AC4 (#3362): a newer record that marks a criterion not verifiable does not
// cancel an older record's met; a newer unmet entry still does.
func TestApplyPriorDeliveries_NotVerifiableDoesNotCancelCredit(t *testing.T) {
	in := []ACVerdict{{Text: "Widget crit", Status: ACNotVerifiable}}
	got := ApplyPriorDeliveries(in, []PriorDelivery{
		{PR: 1, Met: []string{"Widget crit"}},
		{PR: 2, NotVerifiable: []string{"Widget crit"}},
	}, nil, "main", nil)
	if got[0].Status != ACMet || got[0].MetByPR != 1 {
		t.Errorf("a newer not-verifiable entry cancelled #1's credit: %+v", got[0])
	}
	got = ApplyPriorDeliveries(in, []PriorDelivery{
		{PR: 1, Met: []string{"Widget crit"}},
		{PR: 2, Remaining: []string{"Widget crit"}},
	}, nil, "main", nil)
	if got[0].Status != ACNotVerifiable {
		t.Errorf("a newer unmet entry did not cancel #1's credit: %+v", got[0])
	}
}

func nvComment(pr string, met, unmet, notVerifiable []string) string {
	b := "<!-- nightgauge:partial-delivery pr=" + pr + " -->\n\n"
	for _, s := range []struct {
		h    string
		list []string
	}{{hooks.MetCriteriaHeading, met}, {hooks.RemainingCriteriaHeading, unmet}, {hooks.NotVerifiableCriteriaHeading, notVerifiable}} {
		if len(s.list) == 0 {
			continue
		}
		b += s.h + "\n\n"
		for _, c := range s.list {
			b += "- " + c + "\n"
		}
		b += "\n"
	}
	return b
}

// AC4: the reader splits a record's not-verifiable bullets from its unmet
// ones, and the two-PR flow closes the issue end to end.
func TestMergedPriorDeliveries_NotVerifiableSplit(t *testing.T) {
	comments := []trust.Comment{
		{Body: nvComment("1", []string{"Alpha behaviour holds"}, []string{"Beta behaviour holds"}, nil)},
		{Body: nvComment("2", nil, []string{"Beta behaviour holds"}, []string{"Alpha behaviour holds"})},
	}
	priors, err := mergedPriorDeliveries(comments, func(int) (string, error) { return "MERGED", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(priors) != 2 || !reflect.DeepEqual(priors[1].NotVerifiable, []string{"Alpha behaviour holds"}) ||
		!reflect.DeepEqual(priors[1].Remaining, []string{"Beta behaviour holds"}) {
		t.Fatalf("priors = %+v, want PR #2 split into unmet Beta and not-verifiable Alpha", priors)
	}
	dir := priorFixture(t)
	stubPriors(t, priors, nil)
	snap, err := defaultReadCreateContext(context.Background(), dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 42
	body := RenderBody(snap)
	if !strings.Contains(body, "Closes #42") || !strings.Contains(body, "(met by #1, merged earlier)") {
		t.Errorf("PR #2's not-verifiable entry cancelled #1's credit:\n%s", body)
	}
}
