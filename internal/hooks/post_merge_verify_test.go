package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// scriptedChecks is a MainCheckReader that answers each poll from a script.
// The last frame repeats once the script is exhausted, so a test that scripts
// "pending forever" does not have to know the poll count.
type scriptedChecks struct {
	frames   [][]forgetypes.CheckDetail
	errAt    map[int]error // poll index (0-based) -> error
	required []string
	reqErr   error
	polls    int
	reqCalls int
	refs     []string
}

func (s *scriptedChecks) GetIndividualCheckRuns(_ context.Context, _, _, ref string) ([]forgetypes.CheckDetail, error) {
	i := s.polls
	s.polls++
	s.refs = append(s.refs, ref)
	if err, ok := s.errAt[i]; ok {
		return nil, err
	}
	if len(s.frames) == 0 {
		return nil, nil
	}
	if i >= len(s.frames) {
		i = len(s.frames) - 1
	}
	return s.frames[i], nil
}

func (s *scriptedChecks) GetRequiredCheckNames(_ context.Context, _, _, _ string) ([]string, error) {
	s.reqCalls++
	return s.required, s.reqErr
}

func run(name, status, conclusion string) forgetypes.CheckDetail {
	return forgetypes.CheckDetail{Name: name, Status: status, Conclusion: conclusion, DetailsURL: "https://ci/" + name}
}

// noSleep makes the poll loop deterministic: the budget is spent in polls.
func noSleep(context.Context, time.Duration) error { return nil }

// fastWait is a budget of exactly `polls` reads with a grace of `grace` reads.
func fastWait(polls, grace int) MainCheckWait {
	return MainCheckWait{
		Timeout:      time.Duration(polls-1) * time.Second,
		PollInterval: time.Second,
		NoCheckGrace: time.Duration(grace-1) * time.Second,
		Sleep:        noSleep,
	}
}

// The #1038 lesson, pinned: a check-runs list with nothing in it is NOT green.
// GitHub creates check runs seconds after a push; right after a merge the list
// is empty because CI has not started, not because there is nothing to fail.
func TestVerifyMergeCommit_EmptyCheckRunsNeverReadAsGreen(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{}}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(10, 3))

	if res.Verdict == MainChecksGreen {
		t.Fatalf("empty check-runs list read as green — total must be > 0 before anything else is evaluated")
	}
	if res.Verdict != MainChecksNone {
		t.Errorf("Verdict = %q, want %q", res.Verdict, MainChecksNone)
	}
	if res.Polls != 3 {
		t.Errorf("Polls = %d, want the grace (3) and not the whole budget", res.Polls)
	}
	if res.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Total)
	}
}

func TestVerifyMergeCommit_ChecksAppearingInsideGracePromoteToTheFullBudget(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{
		{}, // CI not started yet
		{run("build", "IN_PROGRESS", "")},
		{run("build", "IN_PROGRESS", "")},
		{run("build", "IN_PROGRESS", "")},
		{run("build", "COMPLETED", "SUCCESS")},
	}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(10, 2))

	if res.Verdict != MainChecksGreen {
		t.Fatalf("Verdict = %q, want green: a check appearing inside the grace must keep the wait alive", res.Verdict)
	}
	if res.Polls != 5 {
		t.Errorf("Polls = %d, want 5 (waited past the 2-poll grace once a check existed)", res.Polls)
	}
}

func TestVerifyMergeCommit_PendingIsWaitedOutThenGreen(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{
		{run("build", "QUEUED", ""), run("test", "QUEUED", "")},
		{run("build", "COMPLETED", "SUCCESS"), run("test", "IN_PROGRESS", "")},
		{run("build", "COMPLETED", "SUCCESS"), run("test", "COMPLETED", "SUCCESS"), run("docs", "COMPLETED", "SKIPPED"), run("lint", "COMPLETED", "NEUTRAL")},
	}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(10, 3))

	if res.Verdict != MainChecksGreen {
		t.Fatalf("Verdict = %q, want green", res.Verdict)
	}
	if res.Polls != 3 {
		t.Errorf("Polls = %d, want 3", res.Polls)
	}
	if res.Total != 4 || res.Pending != 0 || res.Bad != 0 {
		t.Errorf("three numbers = (%d, %d, %d), want (4, 0, 0)", res.Total, res.Pending, res.Bad)
	}
	if len(res.Failing) != 0 {
		t.Errorf("Failing = %v, want none", res.Failing)
	}
	if got := reader.refs[0]; got != "abc1234" {
		t.Errorf("check runs read for ref %q, want the merge commit", got)
	}
}

// A conclusion is only evaluated once nothing is pending: a red check alongside
// a still-running one is not yet a verdict, because the operator's idiom reads
// pending==0 BEFORE bad.
func TestVerifyMergeCommit_RedIsOnlyDeclaredOncePendingIsZero(t *testing.T) {
	reader := &scriptedChecks{
		frames: [][]forgetypes.CheckDetail{
			{run("test", "COMPLETED", "FAILURE"), run("build", "IN_PROGRESS", "")},
			{run("test", "COMPLETED", "FAILURE"), run("build", "COMPLETED", "SUCCESS"), run("e2e", "COMPLETED", "TIMED_OUT")},
		},
		required: []string{"test"},
	}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(10, 3))

	if res.Verdict != MainChecksRed {
		t.Fatalf("Verdict = %q, want red", res.Verdict)
	}
	if res.Polls != 2 {
		t.Errorf("Polls = %d, want 2 — the first frame still had a pending check", res.Polls)
	}
	if res.Total != 3 || res.Pending != 0 || res.Bad != 2 {
		t.Errorf("three numbers = (%d, %d, %d), want (3, 0, 2)", res.Total, res.Pending, res.Bad)
	}
	names := res.FailingNames()
	if strings.Join(names, ",") != "e2e,test" {
		t.Errorf("FailingNames = %v, want sorted [e2e test]", names)
	}
	// Required-ness rides on the failing check so the card can pick severity.
	var testCheck, e2eCheck FailingCheck
	for _, f := range res.Failing {
		switch f.Name {
		case "test":
			testCheck = f
		case "e2e":
			e2eCheck = f
		}
	}
	if !testCheck.Required || e2eCheck.Required {
		t.Errorf("Required flags: test=%v e2e=%v, want test required and e2e not", testCheck.Required, e2eCheck.Required)
	}
	if testCheck.Conclusion != "failure" || e2eCheck.Conclusion != "timed_out" {
		t.Errorf("conclusions = %q / %q", testCheck.Conclusion, e2eCheck.Conclusion)
	}
	if testCheck.URL != "https://ci/test" {
		t.Errorf("URL = %q, want the run link", testCheck.URL)
	}
	if reader.reqCalls != 1 {
		t.Errorf("required names looked up %d times, want exactly once and only on red", reader.reqCalls)
	}
}

// Re-runs of one check must not read as N failures (#538).
func TestVerifyMergeCommit_DuplicateRunsOfOneCheckCountOnce(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{
		run("test", "COMPLETED", "FAILURE"),
		run("test", "COMPLETED", "FAILURE"),
		run("build", "COMPLETED", "SUCCESS"),
	}}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(2, 2))

	if res.Verdict != MainChecksRed || res.Bad != 1 || len(res.Failing) != 1 {
		t.Errorf("verdict=%q bad=%d failing=%v, want red / 1 / one entry", res.Verdict, res.Bad, res.Failing)
	}
}

// AGENTS.md's filter verbatim: anything that is not success/skipped/neutral is
// bad. CANCELLED and STALE included — a cancelled run on the commit the pipeline
// just landed is still not a run that passed.
func TestVerifyMergeCommit_ConclusionFilterMatchesAgentsMd(t *testing.T) {
	for _, c := range []string{"FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE"} {
		reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("x", "COMPLETED", c), run("ok", "COMPLETED", "SUCCESS")}}}
		res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(1, 1))
		if res.Verdict != MainChecksRed {
			t.Errorf("conclusion %s: Verdict = %q, want red", c, res.Verdict)
		}
	}
	for _, c := range []string{"SUCCESS", "SKIPPED", "NEUTRAL", "success", " neutral "} {
		reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("x", "COMPLETED", c)}}}
		res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(1, 1))
		if res.Verdict != MainChecksGreen {
			t.Errorf("conclusion %q: Verdict = %q, want green", c, res.Verdict)
		}
	}
}

// Still-pending checks at the end of the budget are not evidence of breakage.
func TestVerifyMergeCommit_BudgetExhaustedWhilePendingIsPendingNotRed(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{
		{run("test", "COMPLETED", "FAILURE"), run("slow", "IN_PROGRESS", "")},
	}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(4, 2))

	if res.Verdict != MainChecksPending {
		t.Fatalf("Verdict = %q, want pending", res.Verdict)
	}
	if res.Polls != 4 {
		t.Errorf("Polls = %d, want the whole budget (4)", res.Polls)
	}
	if len(res.Failing) != 0 {
		t.Errorf("Failing = %v — an unfinished commit must not name failures a card could be raised on", res.Failing)
	}
	if res.Pending != 1 || res.Total != 2 {
		t.Errorf("Total/Pending = %d/%d, want 2/1", res.Total, res.Pending)
	}
	if reader.reqCalls != 0 {
		t.Errorf("required names were looked up on a pending verdict")
	}
}

func TestVerifyMergeCommit_ZeroTimeoutIsExactlyOneRead(t *testing.T) {
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("build", "IN_PROGRESS", "")}}}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", MainCheckWait{Sleep: noSleep})

	if res.Polls != 1 {
		t.Errorf("Polls = %d, want 1 — Timeout 0 is the operator's single read", res.Polls)
	}
	if res.Verdict != MainChecksPending {
		t.Errorf("Verdict = %q, want pending", res.Verdict)
	}
}

func TestVerifyMergeCommit_ReadFailureIsAnErrorVerdictNotAGuess(t *testing.T) {
	reader := &scriptedChecks{
		frames: [][]forgetypes.CheckDetail{{run("build", "IN_PROGRESS", "")}},
		errAt:  map[int]error{1: errors.New("GitHub API returned 502")},
	}

	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(5, 2))

	if res.Verdict != MainChecksError {
		t.Fatalf("Verdict = %q, want error", res.Verdict)
	}
	if !strings.Contains(res.Error, "502") {
		t.Errorf("Error = %q, want the underlying failure", res.Error)
	}
	if res.Polls != 2 {
		t.Errorf("Polls = %d, want 2", res.Polls)
	}
}

func TestVerifyMergeCommit_ContextCancelStopsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("build", "IN_PROGRESS", "")}}}
	wait := fastWait(100, 2)
	wait.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	res := VerifyMergeCommit(ctx, reader, "o", "r", "main", "abc1234", wait)

	if res.Verdict != MainChecksError {
		t.Errorf("Verdict = %q, want error on cancellation", res.Verdict)
	}
	if res.Polls != 1 {
		t.Errorf("Polls = %d, want 1", res.Polls)
	}
}

func TestVerifyMergeCommit_NoReaderOrNoShaIsSkipped(t *testing.T) {
	if res := VerifyMergeCommit(context.Background(), nil, "o", "r", "main", "abc", fastWait(2, 2)); res.Verdict != MainChecksSkipped {
		t.Errorf("nil reader: Verdict = %q, want skipped", res.Verdict)
	}
	reader := &scriptedChecks{}
	if res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "", fastWait(2, 2)); res.Verdict != MainChecksSkipped || reader.polls != 0 {
		t.Errorf("empty sha: Verdict = %q polls=%d, want skipped with no read", res.Verdict, reader.polls)
	}
}

func TestVerifyMergeCommit_RequiredLookupFailureLeavesChecksAdvisory(t *testing.T) {
	reader := &scriptedChecks{
		frames: [][]forgetypes.CheckDetail{{run("test", "COMPLETED", "FAILURE")}},
		reqErr: errors.New("403"),
	}
	res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(1, 1))
	if res.Verdict != MainChecksRed {
		t.Fatalf("Verdict = %q, want red — a failed required lookup does not hide the red", res.Verdict)
	}
	if res.AnyRequiredFailing() {
		t.Error("a check was marked required after the lookup failed")
	}
}

// ── The card ────────────────────────────────────────────────────────────────

func redResult(sha string, failing ...FailingCheck) MainCheckResult {
	return MainCheckResult{Verdict: MainChecksRed, MergeCommitSha: sha, Total: len(failing) + 1, Bad: len(failing), Failing: failing, Polls: 3}
}

func TestBuildMainRedCard_NamesMergeCommitPRAndChecks(t *testing.T) {
	req := BuildMainRedCard("nightgauge", "nightgauge", "main", 1249, 1360,
		redResult("deadbeefcafe", FailingCheck{Name: "e2e", Conclusion: "failure", URL: "https://ci/e2e"}))

	if req.Producer != ProducerMergeCommitChecks {
		t.Errorf("Producer = %q", req.Producer)
	}
	if req.IdempotencyKey != "merge-commit-checks:nightgauge/nightgauge:main" {
		t.Errorf("IdempotencyKey = %q — one card per (repo, branch)", req.IdempotencyKey)
	}
	if !req.Standing || req.Fingerprint == "" {
		t.Error("card must be standing with a fingerprint so a green merge can retract it and a re-observation does not re-alert")
	}
	if !strings.Contains(req.Fingerprint, "deadbeefcafe") || !strings.Contains(req.Fingerprint, "e2e") {
		t.Errorf("Fingerprint = %q, want the merge SHA and the failing set", req.Fingerprint)
	}
	for _, want := range []string{"main", "#1360", `"e2e"`} {
		if !strings.Contains(req.Title, want) {
			t.Errorf("Title %q does not name %s", req.Title, want)
		}
	}
	for _, want := range []string{"deadbee", "#1360", "#1249", "e2e", "https://ci/e2e", "failure"} {
		if !strings.Contains(req.Body, want) {
			t.Errorf("Body does not name %s:\n%s", want, req.Body)
		}
	}
	if req.Context.PR != 1360 || req.Context.Issue != 1249 || req.Context.Repo != "nightgauge/nightgauge" || req.Context.URL != "https://ci/e2e" {
		t.Errorf("Context = %+v", req.Context)
	}
	if req.Severity != attention.SeverityFYI {
		t.Errorf("Severity = %q, want fyi when no required check failed (#1250 semantics)", req.Severity)
	}
	// No repair affordance: nothing in the verb registry fixes a red main.
	for _, opt := range req.Options {
		if opt.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q — the card must not imply Nightgauge can fix main", opt.ID, opt.Verb)
		}
	}
}

func TestBuildMainRedCard_RequiredFailureIsBlockingFleet(t *testing.T) {
	req := BuildMainRedCard("o", "r", "main", 1, 2, redResult("abc",
		FailingCheck{Name: "lint", Conclusion: "failure"},
		FailingCheck{Name: "test", Conclusion: "failure", Required: true}))
	if req.Severity != attention.SeverityBlockingFleet {
		t.Errorf("Severity = %q, want blocking_fleet: a required check is red on main and nothing can land", req.Severity)
	}
	if !strings.Contains(req.Title, "2 checks failed") {
		t.Errorf("Title = %q, want the plural arm", req.Title)
	}
}

// Raise → refresh → update → retract, through the real store: the properties
// the AC asks for are properties of the diff, not of the builder.
func TestReportMainChecks_RedRaisesGreenRetracts(t *testing.T) {
	store := attention.New(t.TempDir())
	failing := FailingCheck{Name: "e2e", Conclusion: "failure"}

	// Pending and no_checks say nothing about the branch: no card.
	for _, v := range []MainCheckVerdict{MainChecksPending, MainChecksNone, MainChecksError, MainChecksSkipped} {
		ReportMainChecks(store, "o", "r", "main", 1, 10, MainCheckResult{Verdict: v, MergeCommitSha: "aaa"})
	}
	if open := listOpen(t, store); len(open) != 0 {
		t.Fatalf("%d card(s) after non-red verdicts, want 0: still-pending checks are not evidence of breakage", len(open))
	}

	// Red raises.
	if note := ReportMainChecks(store, "o", "r", "main", 1, 10, redResult("aaa", failing)); !strings.Contains(note, "created") {
		t.Fatalf("first red: %q, want a created card", note)
	}
	open := listOpen(t, store)
	if len(open) != 1 {
		t.Fatalf("%d open card(s) after red, want 1", len(open))
	}
	id := open[0].ID

	// Same merge re-observed: refreshed in place, never a second card.
	if note := ReportMainChecks(store, "o", "r", "main", 1, 10, redResult("aaa", failing)); !strings.Contains(note, "refreshed") {
		t.Errorf("re-observation: %q, want refreshed", note)
	}
	// A DIFFERENT merge going red is a new fact on the same card identity.
	if note := ReportMainChecks(store, "o", "r", "main", 2, 11, redResult("bbb", failing)); !strings.Contains(note, "updated") {
		t.Errorf("new red merge: %q, want updated", note)
	}
	open = listOpen(t, store)
	if len(open) != 1 || open[0].ID != id {
		t.Fatalf("after two red merges: %d card(s), want the same single card %s", len(open), id)
	}
	if open[0].Context.PR != 11 {
		t.Errorf("card names PR #%d, want the latest red merge #11", open[0].Context.PR)
	}

	// Green retracts.
	note := ReportMainChecks(store, "o", "r", "main", 3, 12, MainCheckResult{Verdict: MainChecksGreen, MergeCommitSha: "ccc", Total: 3})
	if !strings.Contains(note, "retracted") {
		t.Fatalf("green: %q, want a retraction", note)
	}
	if open := listOpen(t, store); len(open) != 0 {
		t.Fatalf("%d open card(s) after green, want 0 — the card must auto-resolve when the branch goes green", len(open))
	}
	got, ok, err := store.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get(%s): ok=%v err=%v", id, ok, err)
	}
	if got.Lifecycle.State != attention.StateAutoResolved {
		t.Errorf("state = %q, want auto_resolved (distinguishable from a human's resolve)", got.Lifecycle.State)
	}

	// A green on an already-green branch is a silent no-op.
	if note := ReportMainChecks(store, "o", "r", "main", 4, 13, MainCheckResult{Verdict: MainChecksGreen, MergeCommitSha: "ddd", Total: 3}); note != "" {
		t.Errorf("green on green: %q, want silence", note)
	}
	// Nil store and no branch are fail-open.
	if note := ReportMainChecks(nil, "o", "r", "main", 1, 1, redResult("x", failing)); note != "" {
		t.Errorf("nil store: %q, want silence", note)
	}
	if note := ReportMainChecks(store, "o", "r", "", 1, 1, redResult("x", failing)); note != "" {
		t.Errorf("no branch: %q, want silence", note)
	}
}

func listOpen(t *testing.T, store *attention.Store) []attention.DecisionRequest {
	t.Helper()
	open, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return open
}

// ── Through EvaluatePostMerge ───────────────────────────────────────────────

// The regression the issue is about: after the pipeline merges, the hook must
// observe the merge commit's own checks and carry the verdict on its result.
// Before #1249 PostMergeResult had no such field and nothing on this path read
// check-runs at all.
func TestEvaluatePostMerge_ObservesMainAfterTheMerge(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1249": {NodeID: "I_1249", Number: 1249},
	}}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "feedface0000", mergedAt: "2026-09-03T10:00:00Z", baseRef: "main"}
	reader := &scriptedChecks{
		frames: [][]forgetypes.CheckDetail{
			{run("test", "IN_PROGRESS", "")},
			{run("test", "COMPLETED", "FAILURE"), run("build", "COMPLETED", "SUCCESS")},
		},
		required: []string{"test"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, verifier, nil, PostMergeInput{
		IssueNumber:     1249,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        1360,
		MainChecks:      reader,
		MainCheckWait:   fastWait(5, 2),
	})

	if result.MainChecks == nil {
		t.Fatal("MainChecks is nil: the hook confirmed the merge and captured its SHA but never observed main")
	}
	if result.MainChecks.Verdict != MainChecksRed {
		t.Errorf("Verdict = %q, want red", result.MainChecks.Verdict)
	}
	if result.MainChecks.MergeCommitSha != "feedface0000" {
		t.Errorf("verified %q, want the merge commit", result.MainChecks.MergeCommitSha)
	}
	if reader.refs[0] != "feedface0000" {
		t.Errorf("check runs read for %q, want the merge commit SHA the breadcrumb captured", reader.refs[0])
	}
	if result.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main", result.BaseRef)
	}
	if got := strings.Join(result.MainChecks.FailingNames(), ","); got != "test" {
		t.Errorf("FailingNames = %q, want test", got)
	}
	// The hook did its job; what failed is main. The card carries that, not
	// the hook's own failure flag — and the merge still seeds survival.
	if result.Failed {
		t.Error("a red main set Failed on the hook result; the hook is non-blocking and did what it was asked")
	}
	if !result.IssueClosed || !result.SurvivalEligible || result.Reason != "no_parent" {
		t.Errorf("reconciliation regressed: closed=%v eligible=%v reason=%q", result.IssueClosed, result.SurvivalEligible, result.Reason)
	}
}

func TestEvaluatePostMerge_NoReaderIsSkippedNeverGreen(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{"o/r#1": {NodeID: "I_1", Number: 1}}}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-09-03T10:00:00Z", baseRef: "main"}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, verifier, nil, PostMergeInput{
		IssueNumber: 1, RepositoryOwner: "o", RepositoryName: "r", PRNumber: 2,
	})

	if result.MainChecks == nil || result.MainChecks.Verdict != MainChecksSkipped {
		t.Errorf("MainChecks = %+v, want an explicit skipped verdict — silence must never read as green", result.MainChecks)
	}
}

func TestEvaluatePostMerge_UnconfirmedMergeIsNotVerified(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{"o/r#1": {NodeID: "I_1", Number: 1}}}
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("test", "COMPLETED", "SUCCESS")}}}

	// PR not merged: the hook refuses before it has a SHA.
	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{},
		&mockPRVerifierWithMerge{state: "OPEN"}, nil, PostMergeInput{
			IssueNumber: 1, RepositoryOwner: "o", RepositoryName: "r", PRNumber: 2, MainChecks: reader, MainCheckWait: fastWait(1, 1),
		})
	if result.MainChecks != nil || reader.polls != 0 {
		t.Errorf("unmerged PR: MainChecks=%+v polls=%d, want nothing observed", result.MainChecks, reader.polls)
	}

	// No PR number: no breadcrumb, so nothing to verify against.
	result = EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, nil, PostMergeInput{
		IssueNumber: 1, RepositoryOwner: "o", RepositoryName: "r", MainChecks: reader, MainCheckWait: fastWait(1, 1),
	})
	if result.MainChecks != nil || reader.polls != 0 {
		t.Errorf("no PR: MainChecks=%+v polls=%d, want nothing observed", result.MainChecks, reader.polls)
	}
}

// The observation is the slow half; it must run after the fast reconciliation,
// so a red main never delays the issue close or the epic rollup — and the
// verdict must still be attached on the epic-error return path.
func TestEvaluatePostMerge_VerifiesMainOnEveryConfirmedReturnPath(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"o/r#5": {NodeID: "I_5", Number: 5, ParentIssueNumber: 9},
	}}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-09-03T10:00:00Z", baseRef: "main"}
	epic := &mockEpicAutoCloser{err: errors.New("boom")}
	order := []string{}
	reader := &scriptedChecks{frames: [][]forgetypes.CheckDetail{{run("t", "COMPLETED", "SUCCESS")}}}
	closer := &recordingCloser{onClose: func() { order = append(order, "close") }}
	wait := fastWait(1, 1)
	wait.Sleep = noSleep

	result := EvaluatePostMerge(context.Background(), fetcher, closer, epic, verifier, nil, PostMergeInput{
		IssueNumber: 5, RepositoryOwner: "o", RepositoryName: "r", PRNumber: 6,
		MainChecks: &orderedReader{inner: reader, onRead: func() { order = append(order, "verify") }}, MainCheckWait: wait,
	})

	if result.Reason != "auto_close_error" {
		t.Fatalf("Reason = %q, want the epic error path", result.Reason)
	}
	if result.MainChecks == nil || result.MainChecks.Verdict != MainChecksGreen {
		t.Errorf("MainChecks = %+v on the epic-error path, want green", result.MainChecks)
	}
	if strings.Join(order, ">") != "close>verify" {
		t.Errorf("order = %v, want the issue closed before main is waited on", order)
	}
}

type recordingCloser struct{ onClose func() }

func (r *recordingCloser) CloseIssue(context.Context, string) error {
	r.onClose()
	return nil
}

type orderedReader struct {
	inner  MainCheckReader
	onRead func()
}

func (o *orderedReader) GetIndividualCheckRuns(ctx context.Context, owner, repo, ref string) ([]forgetypes.CheckDetail, error) {
	o.onRead()
	return o.inner.GetIndividualCheckRuns(ctx, owner, repo, ref)
}

func (o *orderedReader) GetRequiredCheckNames(ctx context.Context, owner, repo, branch string) ([]string, error) {
	return o.inner.GetRequiredCheckNames(ctx, owner, repo, branch)
}
