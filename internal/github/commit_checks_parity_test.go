package github_test

// End-to-end parity between `nightgauge ci checks-complete` and the
// post-merge hook's verification of a merge commit (#1674, #1681): both are
// driven through a real *github.CIService against one fake GitHub that
// paginates its check-runs and publishes a required context only as a commit
// status. The two paths must reach the same verdict, and the hook must reach
// it on its first read instead of waiting out its budget.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

const parityRules = `[{"type":"required_status_checks","parameters":{"required_status_checks":[
	{"context":"job-01"},{"context":"publication boundary"},{"context":"cla"}]}}]`

// parityServer: 30 successful runs on page 1, then page2Runs on page 2, and a
// `cla` commit status in claState. job-01 (page 1), publication boundary
// (page 2) and cla (status only) are required.
func parityServer(t *testing.T, claState string, page2Runs ...string) *httptest.Server {
	t.Helper()
	page2 := append([]string{"publication boundary:completed:success"}, page2Runs...)
	return httptest.NewServer(gh.PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/rules/branches/main": {parityRules},
		"/repos/o/r/commits/merge/check-runs": {
			gh.CheckRunsPage(30+len(page2), gh.SuccessfulRuns("job", 30)...),
			gh.CheckRunsPage(30+len(page2), page2...),
		},
		"/repos/o/r/commits/merge/status": {
			`{"sha":"merge","statuses":[{"context":"cla","state":"` + claState + `"}]}`,
		},
		"/repos/o/r/actions/runs": {`{"workflow_runs":[]}`},
	}})
}

// checksCompleteVerdict is what `nightgauge ci checks-complete` computes for
// one read: the shared reader, the required set, the cross-checked evaluator.
func checksCompleteVerdict(t *testing.T, svc *gh.CIService) gh.ChecksCompleteVerdict {
	t.Helper()
	ctx := context.Background()
	required, err := svc.GetRequiredCheckNames(ctx, "o", "r", "main")
	if err != nil {
		t.Fatalf("GetRequiredCheckNames: %v", err)
	}
	checks, err := svc.GetCommitChecks(ctx, "o", "r", "merge")
	if err != nil {
		t.Fatalf("GetCommitChecks: %v", err)
	}
	runs, err := svc.GetWorkflowRunsForRef(ctx, "o", "r", "merge")
	if err != nil {
		t.Fatalf("GetWorkflowRunsForRef: %v", err)
	}
	verdict, _ := gh.EvaluateCommitChecksCrossChecked(checks, required, runs)
	return verdict
}

// verifyOnce runs the hook's verification with the pipeline's full default
// budget; any sleep fails the test, so a verdict must come from the first read.
func verifyOnce(t *testing.T, svc *gh.CIService) hooks.MainCheckResult {
	t.Helper()
	wait := hooks.DefaultMainCheckWait()
	wait.Sleep = func(context.Context, time.Duration) error {
		t.Fatal("the hook slept: it did not reach a verdict on its first read")
		return nil
	}
	wait.Progress = func(string) {}
	return hooks.VerifyMergeCommit(context.Background(), svc, "o", "r", "main", "merge", wait)
}

// TestPostMergeParity_RequiredStatusOnlyContextIsGreenOnFirstPoll is the #1674
// regression: `cla` is required and exists only as a commit status, and a
// second required check sits on page 2 of the check-runs. The hook used to
// poll until its 20-minute budget ran out while checks-complete said GREEN.
func TestPostMergeParity_RequiredStatusOnlyContextIsGreenOnFirstPoll(t *testing.T) {
	srv := parityServer(t, "success")
	defer srv.Close()
	svc := gh.NewCIServiceForTest(srv)

	res := verifyOnce(t, svc)
	if res.Verdict != hooks.MainChecksGreen || res.Polls != 1 {
		t.Fatalf("hook verdict = %q after %d poll(s) (%v), want green on the first", res.Verdict, res.Polls, res.Reasons)
	}
	if res.Total != 32 {
		t.Errorf("hook saw %d contexts, want 32 (31 check runs across two pages + 1 status)", res.Total)
	}
	if got := checksCompleteVerdict(t, svc); got != gh.ChecksComplete {
		t.Errorf("checks-complete verdict = %q, want green — the two paths must agree", got)
	}
}

// A failed OPTIONAL check makes the merge commit red on both paths: "main's
// own run is green" means all of it, and required-ness must not hide it.
func TestPostMergeParity_FailedOptionalCheckIsRed(t *testing.T) {
	srv := parityServer(t, "success", "advisory:completed:failure")
	defer srv.Close()
	svc := gh.NewCIServiceForTest(srv)

	res := verifyOnce(t, svc)
	if res.Verdict != hooks.MainChecksRed {
		t.Fatalf("hook verdict = %q (%v), want red", res.Verdict, res.Reasons)
	}
	if len(res.Failing) != 1 || res.Failing[0].Name != "advisory" || res.Failing[0].Required {
		t.Errorf("failing = %+v, want the advisory check, marked not required", res.Failing)
	}
	if got := checksCompleteVerdict(t, svc); got != gh.ChecksIncomplete {
		t.Errorf("checks-complete verdict = %q, want red", got)
	}
}

// A failed required STATUS is red on both paths and is marked required.
func TestPostMergeParity_FailedRequiredStatusIsRed(t *testing.T) {
	srv := parityServer(t, "failure")
	defer srv.Close()
	svc := gh.NewCIServiceForTest(srv)

	res := verifyOnce(t, svc)
	if res.Verdict != hooks.MainChecksRed || !res.AnyRequiredFailing() {
		t.Fatalf("hook = %q failing=%+v, want red with cla required", res.Verdict, res.Failing)
	}
	if got := checksCompleteVerdict(t, svc); got != gh.ChecksIncomplete {
		t.Errorf("checks-complete verdict = %q, want red", got)
	}
}

// A still-running OPTIONAL check keeps both paths not-yet: the hook's single
// read (budget 0) records pending, and names what it is waiting on.
func TestPostMergeParity_RunningOptionalCheckIsNotYet(t *testing.T) {
	srv := parityServer(t, "success", "nightly:in_progress:")
	defer srv.Close()
	svc := gh.NewCIServiceForTest(srv)

	res := hooks.VerifyMergeCommit(context.Background(), svc, "o", "r", "main", "merge",
		hooks.MainCheckWait{Progress: func(string) {}})
	if res.Verdict != hooks.MainChecksPending {
		t.Fatalf("hook verdict = %q, want pending", res.Verdict)
	}
	if !strings.Contains(strings.Join(res.Reasons, " "), "nightly") {
		t.Errorf("reasons = %v, want the running check named", res.Reasons)
	}
	if got := checksCompleteVerdict(t, svc); got != gh.ChecksNotYet {
		t.Errorf("checks-complete verdict = %q, want not-yet", got)
	}
}
