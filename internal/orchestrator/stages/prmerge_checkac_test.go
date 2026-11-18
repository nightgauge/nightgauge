package stages

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

const (
	checkCrit = "`integration-tests` passes against platform `main`."
	codeCrit  = "one code change"
	lintCrit  = "the `lint` check is green"
)

// checkPRBody renders a pr-create body: met criteria first, then the open
// ones under UnmetACHeading with their status lines, then `Refs #7`.
func checkPRBody(met []string, open ...string) string {
	var b strings.Builder
	b.WriteString("## Summary\n\nImplements #7: x\n\n")
	if len(met) > 0 {
		b.WriteString("## Acceptance criteria\n\n")
		for _, m := range met {
			b.WriteString("- ✅ met: " + m + " (this PR — diff: x.go)\n")
		}
		b.WriteString("\n")
	}
	if len(open) > 0 {
		b.WriteString(UnmetACHeading + "\n\nThis PR leaves #7 open. These criteria are unmet or could not be verified:\n\n")
		for _, o := range open {
			b.WriteString("- " + o + "\n  _❔ not verifiable_\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Refs #7\n\n<!-- nightgauge:pipeline stage=pr-create issue=7 -->\n")
	return b.String()
}

func checkIssueBody(criteria ...string) string {
	var b strings.Builder
	b.WriteString("## Acceptance Criteria\n\n")
	for _, c := range criteria {
		b.WriteString("- [ ] " + c + "\n")
	}
	return b.String()
}

// runCheckAC runs pr-merge's direct path with rollup and returns the client.
func runCheckAC(t *testing.T, rollup []PRStatusCheckRow, prBody string, criteria ...string) (*bodyGh, string, PRMergeResult) {
	t.Helper()
	dir := writeBackstopContext(t, criteria...)
	if err := setPRContextCloses(dir, 7, false); err != nil {
		t.Fatal(err)
	}
	snap := cleanOpen
	snap.StatusCheckRollup = rollup
	b := &bodyGh{
		queueGh:   queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:    prBody,
		issueBody: checkIssueBody(criteria...),
	}
	res, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	return b, dir, res
}

func success(names ...string) []PRStatusCheckRow {
	var out []PRStatusCheckRow
	for _, n := range names {
		out = append(out, PRStatusCheckRow{Name: n, Conclusion: "SUCCESS"})
	}
	return out
}

// #3424 AC 0, 1: a criterion naming a check that passed is met, and with
// nothing else open the PR closes the issue before the merge.
func TestCheckAC_MetCheckPromotesToCloses(t *testing.T) {
	b, dir, res := runCheckAC(t, success("integration-tests", "lint"),
		checkPRBody([]string{codeCrit}, checkCrit), codeCrit, checkCrit)
	if res.Path != PathMerged || res.PartialDelivery {
		t.Fatalf("res = %+v", res)
	}
	if strings.Join(b.events, ",") != "edit,merge" {
		t.Errorf("events = %v, want edit then merge", b.events)
	}
	got := b.prBody
	if !BodyClosesIssue(got, "o/r", 7) || strings.Contains(got, "Refs #7") {
		t.Errorf("body does not close #7:\n%s", got)
	}
	if strings.Contains(got, UnmetACHeading) {
		t.Errorf("unmet section kept:\n%s", got)
	}
	if !strings.Contains(got, "- ✅ met: "+checkCrit+" (this PR — pr-merge: check `integration-tests` concluded SUCCESS on PR #42)") {
		t.Errorf("met line missing:\n%s", got)
	}
	if v := readClosesIssue(t, dir); v != true {
		t.Errorf("pr-7.json closes_issue = %v, want true", v)
	}
	// The post-merge hook agrees: nothing remains.
	if rem, found := hooks.RemainingCriteria(checkIssueBody(codeCrit, checkCrit), got); !found || len(rem) != 0 {
		t.Errorf("post-merge remainder = %q found=%v, want none", rem, found)
	}
}

// #3424 AC 0: anything but SUCCESS is unmet, and the PR stays Refs.
func TestCheckAC_NonSuccessCheckIsUnmet(t *testing.T) {
	rollup := []PRStatusCheckRow{{Name: "integration-tests", Conclusion: "NEUTRAL"}}
	b, dir, res := runCheckAC(t, rollup, checkPRBody([]string{codeCrit}, checkCrit), codeCrit, checkCrit)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
	got := b.prBody
	if BodyClosesIssue(got, "o/r", 7) || !strings.Contains(got, "Refs #7") {
		t.Errorf("body closes #7:\n%s", got)
	}
	if !strings.Contains(got, "- "+checkCrit+"\n  _❌ unmet — pr-merge: check `integration-tests` concluded NEUTRAL on PR #42_") {
		t.Errorf("unmet status line missing:\n%s", got)
	}
	if v := readClosesIssue(t, dir); v != false {
		t.Errorf("pr-7.json closes_issue = %v, want false", v)
	}
}

// A FAILURE among rows sharing a name wins, whatever its order.
func TestRejudgeCheckCriteria_FailedCheck(t *testing.T) {
	rollup := []PRStatusCheckRow{{Name: "Integration-Tests", Conclusion: "SUCCESS"}, {Name: "integration-tests", Conclusion: "FAILURE"}}
	body, judged := rejudgeCheckCriteria(checkPRBody(nil, checkCrit), rollup, 42)
	if len(judged) != 1 || judged[0].Met {
		t.Fatalf("judged = %+v, want one unmet", judged)
	}
	if !strings.Contains(body, "_❌ unmet — pr-merge: check `integration-tests` concluded FAILURE on PR #42_") ||
		strings.Contains(body, "not verifiable") {
		t.Errorf("body:\n%s", body)
	}
	if _, ok := promoteRefsToCloses(body, 7); !ok {
		t.Error("Refs #7 line not found")
	}
}

// #3424 AC 2: a criterion naming no check on the PR stays not verifiable,
// and the body is not edited.
func TestCheckAC_MissingCheckLeavesBody(t *testing.T) {
	body := checkPRBody([]string{codeCrit}, checkCrit)
	b, dir, res := runCheckAC(t, success("lint"), body, codeCrit, checkCrit)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
	if len(b.edited) != 0 || b.prBody != body {
		t.Errorf("body edited:\n%s", b.prBody)
	}
	if v := readClosesIssue(t, dir); v != false {
		t.Errorf("pr-7.json closes_issue = %v, want false", v)
	}
}

// #3424 AC 3: a met check beside a still-unmet code criterion: the check
// line is met, the code criterion stays open, and the PR stays Refs.
func TestCheckAC_MixWithUnmetCodeCriterion(t *testing.T) {
	b, dir, _ := runCheckAC(t, success("integration-tests"),
		checkPRBody(nil, codeCrit, checkCrit), codeCrit, checkCrit)
	got := b.prBody
	if BodyClosesIssue(got, "o/r", 7) || !strings.Contains(got, "Refs #7") {
		t.Errorf("body closes #7:\n%s", got)
	}
	if !strings.Contains(got, "## Acceptance criteria\n\n- ✅ met: "+checkCrit) {
		t.Errorf("check criterion not met:\n%s", got)
	}
	if !strings.Contains(got, UnmetACHeading) || !strings.Contains(got, "- "+codeCrit+"\n  _❔ not verifiable_") {
		t.Errorf("code criterion no longer open:\n%s", got)
	}
	if strings.Contains(got, "- "+checkCrit+"\n") {
		t.Errorf("check criterion still listed open:\n%s", got)
	}
	if v := readClosesIssue(t, dir); v != false {
		t.Errorf("pr-7.json closes_issue = %v, want false", v)
	}
	rem, _ := hooks.RemainingCriteria(checkIssueBody(codeCrit, checkCrit), got)
	if len(rem) != 1 || rem[0] != codeCrit {
		t.Errorf("post-merge remainder = %q, want only the code criterion", rem)
	}
}

// A criterion pr-create booked unmet on diff evidence stays unmet even when
// one of its code spans names a passing check.
func TestRejudgeCheckCriteria_BookedUnmetUntouched(t *testing.T) {
	const crit = "`foo.go` defines `Bar` and `lint` passes"
	body := strings.Replace(checkPRBody(nil, crit), "_❔ not verifiable_", "_❌ unmet — diff: `Bar` not found_", 1)
	got, judged := rejudgeCheckCriteria(body, success("lint"), 42)
	if len(judged) != 0 || got != body {
		t.Errorf("judged = %+v, body:\n%s", judged, got)
	}
}

// A green check is not the whole claim: a criterion with another
// requirement, or naming a check absent from the PR, stays not verifiable.
func TestRejudgeCheckCriteria_CheckNotWholeClaim(t *testing.T) {
	for _, crit := range []string{
		"`ServeHTTP` returns 404 for `/x` and `lint` passes",
		"`unit` and `e2e` pass",
	} {
		body := checkPRBody(nil, crit)
		got, judged := rejudgeCheckCriteria(body, success("lint", "unit"), 42)
		if len(judged) != 0 || got != body {
			t.Errorf("%q: judged = %+v", crit, judged)
		}
	}
	_, judged := rejudgeCheckCriteria(checkPRBody(nil, "`unit` and `e2e` pass"),
		append(success("unit"), PRStatusCheckRow{Name: "e2e", Conclusion: "FAILURE"}), 42)
	if len(judged) != 1 || judged[0].Met {
		t.Errorf("failed e2e: judged = %+v, want unmet", judged)
	}
}

// Two check-named criteria, both passed, open the met section when the PR
// had none and close the issue.
func TestCheckAC_AllOpenCriteriaMetWithoutMetSection(t *testing.T) {
	b, _, _ := runCheckAC(t, success("integration-tests", "lint"),
		checkPRBody(nil, checkCrit, lintCrit), checkCrit, lintCrit)
	got := b.prBody
	if !BodyClosesIssue(got, "o/r", 7) || strings.Contains(got, UnmetACHeading) {
		t.Errorf("body:\n%s", got)
	}
	if !strings.Contains(got, "## Acceptance criteria\n\n- ✅ met: "+checkCrit) || !strings.Contains(got, "- ✅ met: "+lintCrit) {
		t.Errorf("met section:\n%s", got)
	}
}

// An issue criterion nobody reconciled keeps Refs even when every listed
// criterion is met.
func TestCheckAC_UncoveredIssueCriterionKeepsRefs(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	_ = setPRContextCloses(dir, 7, false)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	b := &bodyGh{
		queueGh:   queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:    checkPRBody(nil, checkCrit),
		issueBody: checkIssueBody(checkCrit, "added later"),
	}
	if _, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir); err != nil {
		t.Fatal(err)
	}
	if BodyClosesIssue(b.prBody, "o/r", 7) {
		t.Errorf("body closes #7 with an unreconciled criterion:\n%s", b.prBody)
	}
}

// A PR already closing its issue is not touched.
func TestCheckAC_ClosingBodyUntouched(t *testing.T) {
	body := strings.Replace(checkPRBody([]string{checkCrit}), "Refs #7", "Closes #7", 1)
	b, _, _ := runCheckAC(t, success("integration-tests"), body, checkCrit)
	if len(b.edited) != 0 {
		t.Errorf("closing body edited: %v", b.edited)
	}
}

// The queue path gets the promoted body before enqueueing.
func TestCheckAC_PromotionPrecedesEnqueue(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	_ = setPRContextCloses(dir, 7, false)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	b := &bodyGh{
		queueGh: queueGh{
			fakeGh: fakeGh{preMerge: snap},
			status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"},
			final:  github.MergeQueueStatus{State: "MERGED"},
		},
		prBody:    checkPRBody(nil, checkCrit),
		issueBody: checkIssueBody(checkCrit),
	}
	if _, err := newBackstopRunner(b).Run(context.Background(), 7, "", dir); err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.events, ",") != "edit,enqueue" || !BodyClosesIssue(b.prBody, "o/r", 7) {
		t.Errorf("events = %v body:\n%s", b.events, b.prBody)
	}
}

func TestPromoteRefsToCloses_WholeLineOnly(t *testing.T) {
	body := "- criterion naming Refs #7 inline\n\nRefs #7\n"
	got, ok := promoteRefsToCloses(body, 7)
	if !ok || got != "- criterion naming Refs #7 inline\n\nCloses #7\n" {
		t.Errorf("got %q ok=%v", got, ok)
	}
	if _, ok := promoteRefsToCloses("Refs #70\n", 7); ok {
		t.Error("Refs #70 promoted for #7")
	}
}

// Every criterion the PR lists is met, but the issue holds one that
// ac-reconcile never covered: the body stays Refs and closes_issue false.
func TestCheckAC_UnreconciledIssueCriterionKeepsRefs(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	_ = setPRContextCloses(dir, 7, false)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	b := &bodyGh{
		queueGh:   queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:    checkPRBody([]string{codeCrit}, checkCrit),
		issueBody: checkIssueBody(codeCrit, checkCrit),
	}
	if _, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir); err != nil {
		t.Fatal(err)
	}
	if rem, _ := hooks.RemainingCriteria(b.issueBody, b.prBody); len(rem) != 0 {
		t.Fatalf("fixture: remainder %q, want none so only the reconcile guard holds Refs", rem)
	}
	if BodyClosesIssue(b.prBody, "o/r", 7) || readClosesIssue(t, dir) != false {
		t.Errorf("promoted with an unreconciled criterion:\n%s", b.prBody)
	}
}

// A body the AC backstop demoted is not promoted back on the check's
// evidence.
func TestCheckAC_BackstopDemotedStaysRefs(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	b := &bodyGh{
		queueGh: queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:  strings.Replace(checkPRBody(nil, checkCrit), "Refs #7", "Closes #7", 1),
		// "added later" is uncovered, so the backstop demotes; the check
		// criterion would otherwise be judged and the body promoted.
		issueBody: checkIssueBody(checkCrit, "added later"),
	}
	res, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PartialDelivery || BodyClosesIssue(b.prBody, "o/r", 7) || len(b.edited) != 1 {
		t.Errorf("res=%+v edits=%d body:\n%s", res, len(b.edited), b.prBody)
	}
}

// failingBodyGh fails the PR-body edit.
type failingBodyGh struct{ directBodyGh }

func (f failingBodyGh) EditPRBody(context.Context, int, string) error {
	return errors.New("edit refused")
}

// A failed edit leaves the body and pr-{N}.json as they were.
func TestCheckAC_FailedEditKeepsRefs(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	_ = setPRContextCloses(dir, 7, false)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	body := checkPRBody(nil, checkCrit)
	b := &bodyGh{
		queueGh:   queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:    body,
		issueBody: checkIssueBody(checkCrit),
	}
	if _, err := newBackstopRunner(failingBodyGh{directBodyGh{b}}).Run(context.Background(), 7, "", dir); err != nil {
		t.Fatal(err)
	}
	if b.prBody != body || readClosesIssue(t, dir) != false {
		t.Errorf("body or closes_issue changed after a failed edit:\n%s", b.prBody)
	}
}

// A failed issue-body read records the verdicts but keeps Refs.
func TestCheckAC_IssueReadFailureKeepsRefs(t *testing.T) {
	dir := writeBackstopContext(t, checkCrit)
	_ = setPRContextCloses(dir, 7, false)
	snap := cleanOpen
	snap.StatusCheckRollup = success("integration-tests")
	b := &bodyGh{
		queueGh:  queueGh{fakeGh: fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:   checkPRBody(nil, checkCrit),
		issueErr: errors.New("rate limited"),
	}
	if _, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir); err != nil {
		t.Fatal(err)
	}
	if BodyClosesIssue(b.prBody, "o/r", 7) || !strings.Contains(b.prBody, "- ✅ met: "+checkCrit) {
		t.Errorf("body:\n%s", b.prBody)
	}
}

func TestCheckConclusions_WorstWins(t *testing.T) {
	cases := []struct {
		rows []PRStatusCheckRow
		want string
	}{
		{[]PRStatusCheckRow{{"ci", "FAILURE"}, {"ci", "SUCCESS"}}, "FAILURE"},
		{[]PRStatusCheckRow{{"ci", "SUCCESS"}, {"ci", "FAILURE"}}, "FAILURE"},
		{[]PRStatusCheckRow{{"ci", "SUCCESS"}, {"ci", ""}}, ""},
		{[]PRStatusCheckRow{{"ci", "SUCCESS"}, {"CI ", "SUCCESS"}}, "SUCCESS"},
	}
	for i, c := range cases {
		if got := checkConclusions(c.rows)["ci"]; got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
	_, judged := rejudgeCheckCriteria(checkPRBody(nil, "`ci` passes"), []PRStatusCheckRow{{"ci", "SUCCESS"}, {"ci", ""}}, 42)
	if len(judged) != 0 {
		t.Errorf("pending row judged: %+v, want left as pr-create wrote it", judged)
	}
}

// An open bullet with no status line gets one.
func TestRejudgeCheckCriteria_BulletWithoutStatus(t *testing.T) {
	body := UnmetACHeading + "\n\n- `ci` passes\n- other\n\nRefs #7\n"
	got, _ := rejudgeCheckCriteria(body, []PRStatusCheckRow{{"ci", "NEUTRAL"}}, 42)
	if !strings.Contains(got, "- `ci` passes\n  _❌ unmet — pr-merge: check `ci` concluded NEUTRAL on PR #42_\n- other\n") {
		t.Errorf("body:\n%s", got)
	}
}
