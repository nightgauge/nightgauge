package hooks

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// recordingCommenter implements IssueCommenter.
type recordingCommenter struct {
	subject string
	body    string
	calls   int
	err     error
	// trusted is what TrustedComments returns; readErr fails the read.
	trusted []trust.Comment
	readErr error
}

func (c *recordingCommenter) TrustedComments(context.Context, string, string, int) ([]trust.Comment, error) {
	return c.trusted, c.readErr
}

func (c *recordingCommenter) AddComment(_ context.Context, subjectID, body string) error {
	c.calls++
	c.subject, c.body = subjectID, body
	return c.err
}

const partialBody = "## Summary\n\nImplements #600: thing\n\n" +
	"## Acceptance criteria\n\n- ✅ met: first\n- ❌ unmet: second\n- ❌ unmet: third\n\n" +
	"## ⚠️ Unmet acceptance criteria\n\nThis PR leaves #600 open. These criteria remain:\n\n" +
	"- second\n- third\n\nPart of #12\nRefs #600\n"

func runPartial(t *testing.T, body string, prNumber int) (PostMergeResult, *mockIssueCloser, *mockEpicAutoCloser, *mockBoardSyncer, *recordingCommenter) {
	t.Helper()
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", ParentIssueNumber: 12},
	}}
	closer := &mockIssueCloser{}
	epic := &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped", Reason: "has_open"}}
	board := &mockBoardSyncer{}
	commenter := &recordingCommenter{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: body}
	res := EvaluatePostMerge(context.Background(), fetcher, closer, epic, verifier, board, PostMergeInput{
		IssueNumber:     600,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
		PRNumber:        prNumber,
		Remainder:       commenter,
	})
	return res, closer, epic, board, commenter
}

func TestPostMergeRefsBodyLeavesIssueOpenAndReturnsToReady(t *testing.T) {
	res, closer, epic, board, commenter := runPartial(t, partialBody, 77)

	if closer.called {
		t.Error("CloseIssue must not run for a PR that says Refs")
	}
	if res.IssueClosed || res.SurvivalEligible {
		t.Errorf("IssueClosed=%v SurvivalEligible=%v, want both false", res.IssueClosed, res.SurvivalEligible)
	}
	if res.Reason != "partial_delivery" || !res.PartialDelivery {
		t.Errorf("Reason=%q PartialDelivery=%v, want partial_delivery/true", res.Reason, res.PartialDelivery)
	}
	if board.calledWith.status != "Ready" || board.syncs != 1 {
		t.Errorf("board synced %d time(s), last to %q; want once to Ready", board.syncs, board.calledWith.status)
	}
	if epic.called {
		t.Error("epic rollup must not count a partial delivery")
	}
	if got := strings.Join(res.UnmetCriteria, "|"); got != "second|third" {
		t.Errorf("UnmetCriteria = %q, want second|third", got)
	}
	if commenter.calls != 1 || commenter.subject != "I_node600" {
		t.Fatalf("comment calls=%d subject=%q", commenter.calls, commenter.subject)
	}
	for _, want := range []string{"PR #77", "- second", "- third", "Ready"} {
		if !strings.Contains(commenter.body, want) {
			t.Errorf("comment missing %q:\n%s", want, commenter.body)
		}
	}
	if res.RemainderComment != "posted" || res.Failed {
		t.Errorf("RemainderComment=%q Failed=%v", res.RemainderComment, res.Failed)
	}
	if res.ValidationMarker != ValidationMarkerNotClosing {
		t.Errorf("ValidationMarker = %q, want %q", res.ValidationMarker, ValidationMarkerNotClosing)
	}
}

func TestPostMergeClosesBodyKeepsClosing(t *testing.T) {
	body := strings.Replace(partialBody, "Refs #600", "Closes #600", 1)
	res, closer, _, board, commenter := runPartial(t, body, 77)
	if !closer.called || !res.IssueClosed {
		t.Error("a Closes body must close the issue")
	}
	if board.calledWith.status != "Done" {
		t.Errorf("board status = %q, want Done", board.calledWith.status)
	}
	if commenter.calls != 0 || res.PartialDelivery {
		t.Error("a Closes body is not a partial delivery")
	}
}

func TestPostMergeUnreadBodyKeepsClosing(t *testing.T) {
	// An empty body means the merge info carried none; the hook cannot tell a
	// partial delivery, so it keeps today's close.
	res, closer, _, _, _ := runPartial(t, "", 77)
	if !closer.called || !res.IssueClosed || res.PartialDelivery {
		t.Errorf("closer.called=%v IssueClosed=%v PartialDelivery=%v", closer.called, res.IssueClosed, res.PartialDelivery)
	}
}

func TestPostMergeZeroPRNumberKeepsClosing(t *testing.T) {
	res, closer, _, _, _ := runPartial(t, partialBody, 0)
	if !closer.called || res.PartialDelivery {
		t.Error("without a PR number there is no body to read; the issue closes")
	}
}

// TestRefsMergeEndToEndStaysOpen walks a Refs merge through both closers
// (#2674): the skill's `issue close --pr` consults IsPartialDelivery and skips,
// then the hook keeps the issue open, moves it to Ready once and comments once.
func TestRefsMergeEndToEndStaysOpen(t *testing.T) {
	if !IsPartialDelivery(partialBody, "o/r", 600) {
		t.Fatal("the skill-side close must skip a Refs body")
	}
	res, closer, _, board, commenter := runPartial(t, partialBody, 77)
	if closer.called || res.IssueClosed {
		t.Error("no closer may run for a Refs merge; the issue stays OPEN")
	}
	if board.syncs != 1 || board.calledWith.status != "Ready" {
		t.Errorf("board synced %d time(s) to %q, want once to Ready", board.syncs, board.calledWith.status)
	}
	if commenter.calls != 1 {
		t.Errorf("remainder comments = %d, want 1", commenter.calls)
	}
}

// TestClosesMergeEndToEndCloses is the Closes counterpart: the skill closes
// and the hook moves the board to Done.
func TestClosesMergeEndToEndCloses(t *testing.T) {
	body := strings.Replace(partialBody, "Refs #600", "Closes #600", 1)
	if IsPartialDelivery(body, "o/r", 600) {
		t.Fatal("the skill-side close must close a Closes body")
	}
	res, closer, _, board, commenter := runPartial(t, body, 77)
	if !closer.called || !res.IssueClosed {
		t.Error("a Closes merge must close the issue")
	}
	if board.calledWith.status != "Done" {
		t.Errorf("board status = %q, want Done", board.calledWith.status)
	}
	if commenter.calls != 0 {
		t.Errorf("remainder comments = %d, want 0", commenter.calls)
	}
}

func TestIsPartialDeliveryUnreadBody(t *testing.T) {
	if IsPartialDelivery("", "o/r", 600) || IsPartialDelivery(" \n\t", "o/r", 600) {
		t.Error("an unread body is not a partial delivery")
	}
}

func TestIsPartialDeliveryURLCloser(t *testing.T) {
	if IsPartialDelivery("Closes https://github.com/o/r/issues/600", "o/r", 600) {
		t.Error("a URL-form closer delivers the issue in full")
	}
	if !IsPartialDelivery("Refs https://github.com/o/r/issues/600", "o/r", 600) {
		t.Error("a URL-form Refs is a partial delivery")
	}
	if !IsPartialDelivery("Closes https://github.com/other/repo/issues/600", "o/r", 600) {
		t.Error("a closer naming another repository's #600 does not deliver o/r#600")
	}
}

func TestClosesIssue(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"Closes #600", true},
		{"closed #600", true},
		{"Fixes: #600", true},
		{"resolves nightgauge/nightgauge#600", true},
		{"Refs #600", false},
		{"Closes #6000", false},
		{"Closes #60", false},
		{"Implements #600\nPart of #600", false},
		{"Refs #600\nCloses #12", false},
		{"Closes https://github.com/nightgauge/nightgauge/issues/600", true},
		{"fixes: http://github.com/nightgauge/nightgauge/issues/600", true},
		{"fixes: https://evil.example/nightgauge/nightgauge/issues/600", false},
		{"Closes https://github.com/nightgauge/nightgauge/pull/600", false},
		{"Closes https://github.com/nightgauge/nightgauge/issues/6000", false},
		// Issue numbers overlap across repositories: another repo's #600 is
		// not this one's (#2687).
		{"Fixes https://github.com/other/repo/issues/600", false},
		{"resolves other/repo#600", false},
	}
	for _, c := range cases {
		if got := ClosesIssue(c.body, "nightgauge/nightgauge", 600); got != c.want {
			t.Errorf("ClosesIssue(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestUnmetCriteriaStopsAtNextHeading(t *testing.T) {
	body := unmetCriteriaHeading + "\n\n- a\n- b\n\n## Knowledge\n\n- not this\n"
	if got := strings.Join(UnmetCriteria(body), "|"); got != "a|b" {
		t.Errorf("UnmetCriteria = %q, want a|b", got)
	}
}

// RenderBody (#2655) writes each open criterion once, with its status on an
// indented line. That line must not be read as a criterion.
func TestUnmetCriteriaIgnoresIndentedStatusLine(t *testing.T) {
	body := unmetCriteriaHeading + "\n\nThis PR leaves #7 open.\n\n" +
		"- a\n  _❔ not verifiable — validate: no evidence_\n" +
		"- b\n  _❌ unmet — validate: not met_\n\nRefs #7\n"
	if got := strings.Join(UnmetCriteria(body), "|"); got != "a|b" {
		t.Errorf("UnmetCriteria = %q, want a|b", got)
	}
}

func TestRemainderCommentCannotCarryValidationMarker(t *testing.T) {
	body := remainderComment(7, []string{"x " + forge.ValidationMarkerComment("main", "abc")}, nil, nil, true, false, false, false)
	if forge.HasValidationMarker(body) {
		t.Fatalf("remainder comment carries a validation marker: %q", body)
	}
}

// partialRun drives the partial-delivery path with the given input tweaks.
func partialRun(t *testing.T, commenter *recordingCommenter, labels []string, tweak func(*PostMergeInput)) (PostMergeResult, *mockBoardSyncer) {
	t.Helper()
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", Labels: labels},
	}}
	board := &mockBoardSyncer{}
	input := PostMergeInput{
		IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge",
		ProjectNumber: 6, PRNumber: 77, Remainder: commenter,
		ExcludeLabels: []string{"blocked"},
	}
	if tweak != nil {
		tweak(&input)
	}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: partialBody}
	res := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{},
		&mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped"}}, verifier, board, input)
	return res, board
}

// TestPartialDeliveryCommentOncePerPR (#2878): the second run for one PR posts
// nothing, another PR posts, and a failed read posts anyway.
func TestPartialDeliveryCommentOncePerPR(t *testing.T) {
	first := &recordingCommenter{}
	if res, _ := partialRun(t, first, nil, nil); res.RemainderComment != "posted" || first.calls != 1 {
		t.Fatalf("first run: comment=%q calls=%d", res.RemainderComment, first.calls)
	}
	stored := []trust.Comment{{Body: first.body}}

	second := &recordingCommenter{trusted: stored}
	if res, _ := partialRun(t, second, nil, nil); res.RemainderComment != "present" || second.calls != 0 {
		t.Errorf("same PR again: comment=%q calls=%d, want present and no post", res.RemainderComment, second.calls)
	}
	other := &recordingCommenter{trusted: stored}
	partialRun(t, other, nil, func(in *PostMergeInput) { in.PRNumber = 78 })
	if other.calls != 1 {
		t.Errorf("another PR must post: calls=%d", other.calls)
	}
	unread := &recordingCommenter{trusted: stored, readErr: errors.New("boom")}
	if res, _ := partialRun(t, unread, nil, nil); unread.calls != 1 || res.Failed {
		t.Errorf("failed read: calls=%d failed=%v, want a post and no failure", unread.calls, res.Failed)
	}
}

// TestPartialDeliveryReadyWithheld (#2878): a run that merged nothing, or an
// excluded label, keeps the issue out of Ready; a real merge still writes it.
func TestPartialDeliveryReadyWithheld(t *testing.T) {
	cases := []struct {
		name      string
		labels    []string
		already   bool
		wantReady bool
	}{
		{name: "real merge", wantReady: true},
		{name: "run merged nothing", already: true},
		{name: "excluded label, any case", labels: []string{"Blocked"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commenter := &recordingCommenter{}
			res, board := partialRun(t, commenter, tc.labels, func(in *PostMergeInput) { in.AlreadyMerged = tc.already })
			wrote := board.called
			if wrote != tc.wantReady || res.ReadyWithheld == tc.wantReady {
				t.Errorf("Ready written=%v withheld=%v, want written=%v", wrote, res.ReadyWithheld, tc.wantReady)
			}
			if got := strings.Contains(commenter.body, "back in Ready"); got != tc.wantReady {
				t.Errorf("comment claims Ready=%v, want %v: %s", got, tc.wantReady, commenter.body)
			}
		})
	}
}

// #3012: a Refs merge known to have changed no files delivered nothing, so it
// parks in Backlog instead of looping through Ready. An unknown count keeps
// the Ready move.
func TestPartialDeliveryEmptyMergeParksInBacklog(t *testing.T) {
	cases := []struct {
		name       string
		files      int
		known      bool
		wantStatus string
		wantEmpty  bool
	}{
		{name: "zero files known", files: 0, known: true, wantStatus: "Backlog", wantEmpty: true},
		{name: "two files", files: 2, known: true, wantStatus: "Ready"},
		{name: "count unknown", files: 0, known: false, wantStatus: "Ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board := &mockBoardSyncer{}
			commenter := &recordingCommenter{}
			input := PostMergeInput{
				IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge",
				ProjectNumber: 6, PRNumber: 77, Remainder: commenter,
			}
			issue := &types.Issue{NodeID: "I_node600", Number: 600, State: "OPEN"}
			merge := gh.PRMergeInfo{SHA: "abc", Body: partialBody, ChangedFiles: tc.files, ChangedFilesKnown: tc.known}
			res := partialDelivery(context.Background(), board, input, issue, merge)
			if board.syncs != 1 || board.calledWith.status != tc.wantStatus {
				t.Errorf("board synced %d time(s), last to %q; want once to %s", board.syncs, board.calledWith.status, tc.wantStatus)
			}
			if res.EmptyDelivery != tc.wantEmpty || res.ReadyWithheld != tc.wantEmpty {
				t.Errorf("EmptyDelivery=%v ReadyWithheld=%v, want %v", res.EmptyDelivery, res.ReadyWithheld, tc.wantEmpty)
			}
			if got := strings.Contains(commenter.body, "changed no files"); got != tc.wantEmpty {
				t.Errorf("comment names empty delivery=%v, want %v: %s", got, tc.wantEmpty, commenter.body)
			}
			if strings.Contains(commenter.body, "back in Ready") == tc.wantEmpty {
				t.Errorf("comment Ready claim wrong: %s", commenter.body)
			}
		})
	}
}

// runPartialIssue is runPartial with the fetched issue's state and body set.
func runPartialIssue(t *testing.T, state, issueBody string, closeErr error) (PostMergeResult, *mockIssueCloser, *mockBoardSyncer, *recordingCommenter) {
	t.Helper()
	res, closer, board, commenter, _ := runPartialIssuePR(t, state, issueBody, partialBody, closeErr)
	return res, closer, board, commenter
}

// metOnlyPRBody is a Refs PR body that reports no open criterion.
const metOnlyPRBody = "## Summary\n\nPart of #600\n\n## Acceptance criteria\n\n- ✅ met: code change\n\nRefs #600\n"

// recordingBodyEditor implements IssueBodyEditor.
type recordingBodyEditor struct {
	nodeID, body string
	calls        int
	err          error
}

func (e *recordingBodyEditor) EditIssue(_ context.Context, nodeID, body string) (*types.Issue, error) {
	e.calls++
	e.nodeID, e.body = nodeID, body
	return &types.Issue{}, e.err
}

// runPartialIssuePR is runPartialIssue with the merged PR's body set and a
// recording body editor wired.
func runPartialIssuePR(t *testing.T, state, issueBody, prBody string, closeErr error) (PostMergeResult, *mockIssueCloser, *mockBoardSyncer, *recordingCommenter, *recordingBodyEditor) {
	t.Helper()
	editor := &recordingBodyEditor{}
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: state, Body: issueBody, ParentIssueNumber: 12},
	}}
	closer := &mockIssueCloser{err: closeErr}
	epic := &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped", Reason: "has_open"}}
	board := &mockBoardSyncer{}
	commenter := &recordingCommenter{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: prBody}
	res := EvaluatePostMerge(context.Background(), fetcher, closer, epic, verifier, board, PostMergeInput{
		IssueNumber:     600,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
		PRNumber:        77,
		Remainder:       commenter,
		BodyEditor:      editor,
	})
	return res, closer, board, commenter, editor
}

// #3269 part D: an issue the supervisor already closed with every criterion
// ticked goes to Done even though the merged PR said Refs.
func TestPostMergeRefsPRClosedIssueGoesToDone(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] first\n- [x] second\n- [x] third\n"
	res, _, board, commenter := runPartialIssue(t, "CLOSED", body, errors.New("already closed"))
	if res.PartialDelivery || res.Reason == "partial_delivery" {
		t.Errorf("closed issue reported as partial delivery (left open): %+v", res)
	}
	if !res.IssueClosed {
		t.Error("IssueClosed = false, want true for an already-closed issue")
	}
	if board.calledWith.status != "Done" {
		t.Errorf("board synced to %q, want Done", board.calledWith.status)
	}
	if commenter.calls != 0 {
		t.Errorf("remainder comment posted %d time(s) on a closed issue", commenter.calls)
	}
}

// #3269 part D: an open issue with unticked criteria and a Refs PR stays in
// Ready, and the count is the issue body's, not the PR body's stale list.
func TestPostMergeRefsPROpenIssueCountsCurrentBody(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] first\n- [x] second\n- [ ] third\n"
	res, closer, board, commenter := runPartialIssue(t, "OPEN", body, nil)
	if closer.called {
		t.Error("CloseIssue must not run for an open issue with a Refs PR")
	}
	if !res.PartialDelivery || board.calledWith.status != "Ready" {
		t.Errorf("PartialDelivery=%v board=%q, want true/Ready", res.PartialDelivery, board.calledWith.status)
	}
	if got := strings.Join(res.UnmetCriteria, "|"); got != "second|third" {
		t.Errorf("UnmetCriteria = %q, want second|third: a criterion the PR lists unmet stays even when ticked (#3318)", got)
	}
	if !strings.Contains(commenter.body, "- third") || !strings.Contains(commenter.body, "- second") {
		t.Errorf("remainder comment names the stale list:\n%s", commenter.body)
	}
}

// #3269 part E: a remainder made only of supervisor/owner criteria is not
// re-queued — the row stays In review and the comment says why.
func TestPostMergeOwnerOnlyRemainderStaysInReview(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] code change\n" +
		"- [ ] **Supervisor:** run the failure drill after merge\n" +
		"- [ ] **Owner** signs off the rollout\n"
	res, _, board, commenter, _ := runPartialIssuePR(t, "OPEN", body, metOnlyPRBody, nil)
	if !res.PartialDelivery || !res.OwnerOnlyRemainder || !res.ReadyWithheld {
		t.Errorf("PartialDelivery=%v OwnerOnlyRemainder=%v ReadyWithheld=%v, want all true", res.PartialDelivery, res.OwnerOnlyRemainder, res.ReadyWithheld)
	}
	if board.calledWith.status != "In review" || board.syncs != 1 {
		t.Errorf("board synced %d time(s), last to %q; want once to In review", board.syncs, board.calledWith.status)
	}
	if !strings.Contains(commenter.body, "stays In review and is not re-queued") || strings.Contains(commenter.body, "back in Ready") {
		t.Errorf("comment does not say the issue stays In review:\n%s", commenter.body)
	}
}

// A mixed remainder (one criterion the pipeline can deliver) still returns
// to Ready.
func TestPostMergeMixedRemainderReturnsToReady(t *testing.T) {
	body := "- [ ] **Supervisor:** drill\n- [ ] add the flag\n"
	res, _, board, _ := runPartialIssue(t, "OPEN", body, nil)
	if res.OwnerOnlyRemainder || board.calledWith.status != "Ready" {
		t.Errorf("OwnerOnlyRemainder=%v board=%q, want false/Ready", res.OwnerOnlyRemainder, board.calledWith.status)
	}
}

// #3269 review: an open issue whose body ticks every criterion is not
// re-queued — nothing is left to deliver, so it stays In review.
func TestPostMergeAllTickedOpenIssueStaysInReview(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] first\n- [x] second\n"
	res, closer, board, commenter, _ := runPartialIssuePR(t, "OPEN", body, metOnlyPRBody, nil)
	if closer.called {
		t.Error("CloseIssue must not run for an open issue with a Refs PR")
	}
	if !res.AllCriteriaTicked || !res.ReadyWithheld {
		t.Errorf("AllCriteriaTicked=%v ReadyWithheld=%v, want true/true", res.AllCriteriaTicked, res.ReadyWithheld)
	}
	if board.calledWith.status != "In review" || board.syncs != 1 {
		t.Errorf("board synced %d time(s), last to %q; want once to In review", board.syncs, board.calledWith.status)
	}
	if strings.Contains(commenter.body, "did not list") || strings.Contains(commenter.body, "back in Ready") {
		t.Errorf("comment misdescribes a fully ticked issue:\n%s", commenter.body)
	}
}

// #3297: a genuinely unmet wrapped criterion is quoted whole in the remainder
// comment and round-trips through ParsePartialDeliveryComment.
func TestRemainderQuotesWrappedCriterionWhole(t *testing.T) {
	body := "## Acceptance Criteria\n- [x] done\n- [ ] Unmet criterion that\n  wraps onto a second line.\n"
	unmet := computeRemainder(body, "", nil).Remaining
	want := "Unmet criterion that wraps onto a second line."
	if len(unmet) != 1 || unmet[0] != want {
		t.Fatalf("Remaining = %q, want [%q]", unmet, want)
	}
	comment := remainderComment(3294, unmet, nil, nil, false, false, false, false)
	pr, criteria, ok := ParsePartialDeliveryComment(comment)
	if !ok || pr != 3294 || len(criteria) != 1 || criteria[0] != want {
		t.Errorf("round-trip = %d %q %v, want 3294 [%q]\n%s", pr, criteria, ok, want, comment)
	}
}

// #3318: #2847's body after PR #3317 merged. Numbered criteria 1–4, the
// criterion-3 checkbox and three Part B boxes, all unticked, with the
// original list repeated inside <details>. The PR met five criteria and
// reported two post-merge measurements not verifiable.
const issue2847Body = "## Problem\n\nThe gate burns time.\n\n" +
	"## Acceptance criteria\n\n" +
	"1. ci-local records every failing gate step in the run summary.\n" +
	"2. Fast static checks run before the full gate and stop it on failure.\n" +
	"3. Median gate wall clock falls by a third, measured over 8 runs after merge.\n" +
	"4. Fewer than 1 in 10 pushes fail CI after a green local gate, measured over 20 merges.\n\n" +
	"- [ ] Median gate wall clock falls by a third, measured over 8 runs after merge. (criterion 3 as a checkbox, for ac-mark)\n\n" +
	"### Acceptance criteria (Part B)\n\n" +
	"- [ ] ci-local --fast typechecks touched TypeScript packages.\n" +
	"- [ ] gate-summary scopes whole runs to a build.\n" +
	"- [ ] Fewer than 1 in 10 pushes fail CI after a green local gate, measured over 20 merges.\n\n" +
	"<details><summary>Original list</summary>\n\n" +
	"1. ci-local records every failing gate step in the run summary.\n" +
	"2. A stale criterion nobody tracks any more.\n\n" +
	"</details>\n"

const pr3317Body = "## Summary\n\nci-local --fast typechecks touched packages.\n\n" +
	"## Acceptance criteria\n\n" +
	"- ✅ met: ci-local records every failing gate step in the run summary. (diff: scripts/ci-local.sh)\n" +
	"- ✅ met: Fast static checks run before the full gate and stop it on failure. (diff: scripts/ci-local.sh)\n" +
	"- ✅ met: ci-local --fast typechecks touched TypeScript packages. (test: TestFastTypecheck (scoped))\n" +
	"- ✅ met: gate-summary scopes whole runs to a build. (test: TestGateSummaryScope)\n" +
	"- ✅ met: Fast static checks run before the full gate and stop it on failure (duplicate wording). (diff: x)\n\n" +
	"## ⚠️ Unmet acceptance criteria\n\nThis PR leaves #2847 open. These criteria are unmet or could not be verified:\n\n" +
	"- Median gate wall clock falls by a third, measured over 8 runs after merge.\n" +
	"  _❔ not verifiable — needs: 8 gate runs after merge_\n" +
	"- Fewer than 1 in 10 pushes fail CI after a green local gate, measured over 20 merges.\n" +
	"  _❔ not verifiable — needs: 20 merges_\n\nRefs #2847\n"

func TestPostMergeIssue2847PR3317Remainder(t *testing.T) {
	res, _, board, commenter, editor := runPartialIssuePR(t, "OPEN", issue2847Body, pr3317Body, nil)
	want := []string{
		"Median gate wall clock falls by a third, measured over 8 runs after merge.",
		"Fewer than 1 in 10 pushes fail CI after a green local gate, measured over 20 merges.",
	}
	if strings.Join(res.UnmetCriteria, "|") != strings.Join(want, "|") {
		t.Errorf("UnmetCriteria = %q, want %q", res.UnmetCriteria, want)
	}
	if !strings.Contains(editor.body, "- [x] ci-local --fast typechecks") || !strings.Contains(editor.body, "- [x] gate-summary scopes") {
		t.Errorf("met Part B boxes not ticked:\n%s", editor.body)
	}
	if !strings.Contains(editor.body, "- [ ] Median gate wall clock") || !strings.Contains(editor.body, "- [ ] Fewer than 1 in 10") {
		t.Errorf("unmet boxes were ticked:\n%s", editor.body)
	}
	if strings.Join(intsToStrings(res.TickedCriteria), ",") != "2,3" {
		t.Errorf("TickedCriteria = %v, want [2 3]", res.TickedCriteria)
	}
	if !res.OwnerOnlyRemainder || board.calledWith.status != "In review" || board.syncs != 1 {
		t.Errorf("OwnerOnlyRemainder=%v board %d sync(s) to %q, want true and once to In review", res.OwnerOnlyRemainder, board.syncs, board.calledWith.status)
	}
	for _, c := range want {
		if !strings.Contains(commenter.body, "- "+c) {
			t.Errorf("comment does not list %q:\n%s", c, commenter.body)
		}
	}
	if strings.Contains(commenter.body, "back in Ready") {
		t.Errorf("comment claims Ready:\n%s", commenter.body)
	}
}

// #3318 AC2: a numbered criterion the PR lists unmet is in the remainder and
// the issue returns to Ready.
func TestPostMergeNumberedUnmetCriterionReturnsToReady(t *testing.T) {
	issue := "## Acceptance criteria\n\n1. The hook ticks every met checkbox in the issue body.\n2. The flag is documented.\n"
	pr := "## Acceptance criteria\n\n- ✅ met: The flag is documented. (diff: docs)\n\n" +
		"## ⚠️ Unmet acceptance criteria\n\n- The hook ticks every met checkbox in the issue body.\n  _❌ unmet — test: missing_\n\nRefs #600\n"
	res, _, board, _, editor := runPartialIssuePR(t, "OPEN", issue, pr, nil)
	if got := strings.Join(res.UnmetCriteria, "|"); got != "The hook ticks every met checkbox in the issue body." {
		t.Errorf("UnmetCriteria = %q", got)
	}
	if res.OwnerOnlyRemainder || board.calledWith.status != "Ready" {
		t.Errorf("OwnerOnlyRemainder=%v board=%q, want false/Ready", res.OwnerOnlyRemainder, board.calledWith.status)
	}
	if editor.calls != 0 {
		t.Errorf("numbered items have no box; EditIssue called %d time(s)", editor.calls)
	}
}

// A failed tick fails the hook but keeps the remainder right.
func TestPostMergeTickFailureKeepsRemainder(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", Body: issue2847Body},
	}}
	editor := &recordingBodyEditor{err: errors.New("boom")}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", body: pr3317Body}
	res := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped"}}, verifier, &mockBoardSyncer{}, PostMergeInput{
		IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge", ProjectNumber: 6, PRNumber: 77, BodyEditor: editor,
	})
	if !res.Failed || res.TickedCriteria != nil || len(res.UnmetCriteria) != 2 {
		t.Errorf("Failed=%v Ticked=%v Unmet=%q, want true/nil/2", res.Failed, res.TickedCriteria, res.UnmetCriteria)
	}
	if editor.calls != 1 || editor.nodeID != "I_node600" || res.Error != "boom" {
		t.Errorf("EditIssue calls=%d node=%q Error=%q", editor.calls, editor.nodeID, res.Error)
	}
}

func intsToStrings(in []int) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = strconv.Itoa(n)
	}
	return out
}

// A re-run over an already-merged PR does not tick: the body may have been
// edited after merge.
func TestPostMergeAlreadyMergedDoesNotTick(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", Body: issue2847Body},
	}}
	editor := &recordingBodyEditor{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", body: pr3317Body}
	res := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped"}}, verifier, &mockBoardSyncer{}, PostMergeInput{
		IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge", ProjectNumber: 6, PRNumber: 77, BodyEditor: editor, AlreadyMerged: true,
	})
	if editor.calls != 0 || res.TickedCriteria != nil || len(res.UnmetCriteria) != 2 {
		t.Errorf("calls=%d Ticked=%v Unmet=%q", editor.calls, res.TickedCriteria, res.UnmetCriteria)
	}
}

// #3318 round 3: a numbered criterion PR A met has no box to tick, so PR A's
// marker comment records it; PR B, which does not restate it, must not put it
// back in the remainder.
func TestPostMergeNumberedMetCriterionPersistsAcrossRefsPRs(t *testing.T) {
	issue := "## Acceptance criteria\n\n1. The first criterion holds.\n2. The second criterion holds.\n3. The third criterion holds.\n"
	run := func(prNumber int, prBody string, trusted []trust.Comment) (PostMergeResult, *recordingCommenter, *recordingBodyEditor) {
		t.Helper()
		editor := &recordingBodyEditor{}
		commenter := &recordingCommenter{trusted: trusted}
		fetcher := &mockFetcher{issues: map[string]*types.Issue{
			"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", Body: issue},
		}}
		verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", body: prBody}
		res := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped"}}, verifier, &mockBoardSyncer{}, PostMergeInput{
			IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge", ProjectNumber: 6,
			PRNumber: prNumber, Remainder: commenter, BodyEditor: editor,
		})
		return res, commenter, editor
	}
	unmet3 := "## ⚠️ Unmet acceptance criteria\n\n- The third criterion holds.\n  _❌ unmet — x_\n\n"
	prA := "## Acceptance criteria\n\n- ✅ met: The first criterion holds. (diff: a)\n- ✅ met: The second criterion holds. (diff: b)\n\n" + unmet3 + "Refs #600\n"
	resA, commA, edA := run(77, prA, nil)
	if got := strings.Join(resA.UnmetCriteria, "|"); got != "The third criterion holds." {
		t.Fatalf("after A UnmetCriteria = %q", got)
	}
	if got := ParsePartialDeliveryMet(commA.body); !reflect.DeepEqual(got, []string{"The first criterion holds.", "The second criterion holds."}) {
		t.Fatalf("A's marker met list = %q\n%s", got, commA.body)
	}
	if _, rem, _ := ParsePartialDeliveryComment(commA.body); !reflect.DeepEqual(rem, []string{"The third criterion holds."}) {
		t.Errorf("A's marker remaining list = %q", rem)
	}

	prB := "## Acceptance criteria\n\n- ✅ met: The first criterion holds. (diff: c)\n\n" + unmet3 + "Refs #600\n"
	resB, commB, edB := run(78, prB, []trust.Comment{{Body: commA.body}})
	if !reflect.DeepEqual(resB.UnmetCriteria, []string{"The third criterion holds."}) {
		t.Errorf("after B UnmetCriteria = %q, want exactly criterion 3", resB.UnmetCriteria)
	}
	if commB.calls != 1 {
		t.Errorf("B posted %d comment(s), want 1", commB.calls)
	}
	if edA.calls+edB.calls != 0 {
		t.Errorf("numbered items have no box; EditIssue called %d time(s)", edA.calls+edB.calls)
	}
}

// #3510 AC4/AC5: a Refs merge that leaves nothing unmet on an issue whose
// Delivery order says refs neither closes the issue nor returns it to Ready.
func TestPostMergeDeliveryOrderRefsHoldsReady(t *testing.T) {
	// noVerdictPRBody lists no criteria, so the remainder is not computed
	// from either body: the path that returned such an issue to Ready.
	const noVerdictPRBody = "## Summary\n\nPart of #600\n\nRefs #600\n"
	const refsOrder = "## Delivery order\n\nStep one lands first, and each PR refs #600.\n"
	for name, tc := range map[string]struct {
		issueBody string
		prBody    string
		wantRefs  bool
		wantBoard string
	}{
		"refs, no parseable criteria":                                   {refsOrder, noVerdictPRBody, true, "In review"},
		"refs, every criterion met (already held by AllCriteriaTicked)": {"## Acceptance criteria\n\n- [x] code change\n\n" + refsOrder, metOnlyPRBody, true, "In review"},
		"no refs, no parseable criteria":                                {"## Delivery order\n\nOne PR closes it.\n", noVerdictPRBody, false, "Ready"},
	} {
		t.Run(name, func(t *testing.T) {
			res, closer, board, commenter, _ := runPartialIssuePR(t, "OPEN", tc.issueBody, tc.prBody, nil)
			if closer.called || res.IssueClosed {
				t.Errorf("issue closed (called=%v IssueClosed=%v) by a Refs merge", closer.called, res.IssueClosed)
			}
			if res.DeliveryOrderRefs != tc.wantRefs || res.ReadyWithheld != tc.wantRefs {
				t.Errorf("DeliveryOrderRefs=%v ReadyWithheld=%v, want %v/%v", res.DeliveryOrderRefs, res.ReadyWithheld, tc.wantRefs, tc.wantRefs)
			}
			if board.calledWith.status != tc.wantBoard || board.syncs != 1 {
				t.Errorf("board synced %d time(s), last to %q; want once to %q", board.syncs, board.calledWith.status, tc.wantBoard)
			}
			if back := strings.Contains(commenter.body, "back in Ready"); back == tc.wantRefs {
				t.Errorf("comment back-in-Ready=%v, want %v:\n%s", back, !tc.wantRefs, commenter.body)
			}
		})
	}
}

// #3510 AC4: a Delivery order refs does not hold Ready while a criterion the
// pipeline can deliver is still unmet: the next step is re-dispatched.
func TestPostMergeDeliveryOrderRefsWithUnmetReturnsToReady(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] first\n- [ ] add the flag\n\n## Delivery order\n\nEach PR refs #600.\n"
	res, closer, board, _ := runPartialIssue(t, "OPEN", body, nil)
	if closer.called || !res.DeliveryOrderRefs || res.ReadyWithheld || board.calledWith.status != "Ready" {
		t.Errorf("closed=%v DeliveryOrderRefs=%v ReadyWithheld=%v board=%q, want false/true/false/Ready", closer.called, res.DeliveryOrderRefs, res.ReadyWithheld, board.calledWith.status)
	}
}

// #3510 AC4: an issue body that cannot be read neither closes the issue nor
// moves its row. The issue_fetch_error return predates #3510; this pins it as
// the unreadable-body case of a Refs merge.
func TestPostMergeRefsMergeUnreadableIssueHolds(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{}}
	closer := &mockIssueCloser{}
	board := &mockBoardSyncer{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: metOnlyPRBody}
	res := EvaluatePostMerge(context.Background(), fetcher, closer, &mockEpicAutoCloser{}, verifier, board, PostMergeInput{
		IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge", ProjectNumber: 6, PRNumber: 77,
		Remainder: &recordingCommenter{},
	})
	if res.Reason != "issue_fetch_error" || closer.called || res.IssueClosed || board.syncs != 0 {
		t.Errorf("Reason=%q closed=%v IssueClosed=%v syncs=%d, want issue_fetch_error/false/false/0", res.Reason, closer.called, res.IssueClosed, board.syncs)
	}
}
