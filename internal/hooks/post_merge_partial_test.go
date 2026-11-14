package hooks

import (
	"context"
	"errors"
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
	body := remainderComment(7, []string{"x " + forge.ValidationMarkerComment("main", "abc")}, true, false, false, false)
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
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: state, Body: issueBody, ParentIssueNumber: 12},
	}}
	closer := &mockIssueCloser{err: closeErr}
	epic := &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped", Reason: "has_open"}}
	board := &mockBoardSyncer{}
	commenter := &recordingCommenter{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: partialBody}
	res := EvaluatePostMerge(context.Background(), fetcher, closer, epic, verifier, board, PostMergeInput{
		IssueNumber:     600,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
		PRNumber:        77,
		Remainder:       commenter,
	})
	return res, closer, board, commenter
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
	if got := strings.Join(res.UnmetCriteria, "|"); got != "third" {
		t.Errorf("UnmetCriteria = %q, want the issue body's one unticked criterion (the PR listed second|third)", got)
	}
	if !strings.Contains(commenter.body, "- third") || strings.Contains(commenter.body, "- second") {
		t.Errorf("remainder comment names the stale list:\n%s", commenter.body)
	}
}

// #3269 part E: a remainder made only of supervisor/owner criteria is not
// re-queued — the row stays In review and the comment says why.
func TestPostMergeOwnerOnlyRemainderStaysInReview(t *testing.T) {
	body := "## Acceptance criteria\n\n- [x] code change\n" +
		"- [ ] **Supervisor:** run the failure drill after merge\n" +
		"- [ ] **Owner** signs off the rollout\n"
	res, _, board, commenter := runPartialIssue(t, "OPEN", body, nil)
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
	res, closer, board, commenter := runPartialIssue(t, "OPEN", body, nil)
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
